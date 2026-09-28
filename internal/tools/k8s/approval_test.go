//go:build with_k8s

package k8s

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// approvalHarness wires a real approval.Manager into a K8sTool and records
// every notice delivered to the "human" (conduit-c8ct).
type approvalHarness struct {
	tool    *K8sTool
	mgr     *approval.Manager
	origin  approval.Origin
	mu      sync.Mutex
	notices []approval.Notice
}

func newApprovalHarness(t *testing.T, requireApproval []string) *approvalHarness {
	t.Helper()
	h := &approvalHarness{tool: setupTestTool(t)}
	h.tool.security = NewSecurityEngine(SecurityConfig{RequireApproval: requireApproval})
	h.mgr = approval.NewManager(approval.Config{})
	t.Cleanup(h.mgr.Close)
	h.tool.services = &types.ToolServices{Approvals: h.mgr}
	h.origin = approval.Origin{
		Source: "telegram", ChannelID: "telegram", UserID: "owner", SessionKey: "s1",
		Notify: func(_ context.Context, n approval.Notice) error {
			h.mu.Lock()
			h.notices = append(h.notices, n)
			h.mu.Unlock()
			return nil
		},
	}
	return h
}

func (h *approvalHarness) interactive() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), h.origin)
}

func (h *approvalHarness) reply(text string) bool {
	return h.mgr.HandleReply(context.Background(), approval.Inbound{
		ChannelID: h.origin.ChannelID, UserID: h.origin.UserID, SessionKey: h.origin.SessionKey,
		Text: text, Notify: h.origin.Notify,
	})
}

func (h *approvalHarness) allNotices() []approval.Notice {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]approval.Notice(nil), h.notices...)
}

var codeRe = regexp.MustCompile(`APPROVAL NEEDED \[([A-Z0-9]{6})\]`)

func (h *approvalHarness) promptCode(t *testing.T) (string, string) {
	t.Helper()
	for _, n := range h.allNotices() {
		if m := codeRe.FindStringSubmatch(n.Text); m != nil {
			return m[1], n.Text
		}
	}
	t.Fatal("no approval prompt was delivered")
	return "", ""
}

func podExists(t *testing.T, tool *K8sTool, name string) bool {
	t.Helper()
	c, err := tool.clients.GetClient("test-cluster")
	require.NoError(t, err)
	_, err = c.clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

func deleteArgs(name string) map[string]interface{} {
	return map[string]interface{}{"action": "delete", "resource": "pods", "name": name}
}

func TestK8sApproval_DefaultTierFromConfig(t *testing.T) {
	cfg := &config.KubernetesConfig{Enabled: true}
	tool, err := NewK8sTool(nil, cfg)
	require.NoError(t, err)
	assert.True(t, tool.security.ClassifyOperation("delete", "pods", "default").RequiresApproval,
		"absent require_approval must default to the dangerous tier")
	assert.False(t, tool.security.ClassifyOperation("scale", "deployments", "default").RequiresApproval)

	cfg.RequireApproval = []string{}
	tool, err = NewK8sTool(nil, cfg)
	require.NoError(t, err)
	assert.False(t, tool.security.ClassifyOperation("delete", "pods", "default").RequiresApproval,
		"explicit empty require_approval disables gating")

	cfg.RequireApproval = []string{"modify"}
	tool, err = NewK8sTool(nil, cfg)
	require.NoError(t, err)
	assert.True(t, tool.security.ClassifyOperation("scale", "deployments", "default").RequiresApproval)
}

func TestK8sApproval_NeverRunsWithoutApprovedCode(t *testing.T) {
	h := newApprovalHarness(t, []string{"dangerous"})

	res, err := h.tool.Execute(h.interactive(), deleteArgs("web-abc123"))
	require.NoError(t, err)
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.Equal(t, "pending", res.Data["approval_status"])
	code, prompt := h.promptCode(t)
	assert.NotContains(t, res.Content, code, "the approval code must be withheld from the model")
	assert.True(t, podExists(t, h.tool, "web-abc123"), "gated delete ran before approval")

	// A wrong code and a bare "yes" approve nothing.
	assert.True(t, h.reply("YES ZZZ222"))
	assert.False(t, h.reply("yes"))
	h.mgr.Wait()
	assert.True(t, podExists(t, h.tool, "web-abc123"))

	// Denial consumes the code; a later approval of the same code is void.
	assert.True(t, h.reply("NO "+code))
	assert.True(t, h.reply("YES "+code))
	h.mgr.Wait()
	assert.True(t, podExists(t, h.tool, "web-abc123"), "denied delete must never run")
	assert.Empty(t, h.mgr.Pending("s1"))

	// The prompt names cluster, namespace, verb and resource.
	for _, want := range []string{
		"Kubernetes DELETE pods/web-abc123 in namespace default on cluster test-cluster",
		"Cluster: test-cluster", "Namespace: default", "Verb: delete", "Resource: pods/web-abc123",
		"Risk: dangerous tier",
	} {
		assert.Contains(t, prompt, want)
	}
}

func TestK8sApproval_ApprovedCodeRunsExactlyFrozenOp(t *testing.T) {
	h := newApprovalHarness(t, []string{"dangerous"})

	args := deleteArgs("web-abc123")
	res, err := h.tool.Execute(h.interactive(), args)
	require.NoError(t, err)
	require.Equal(t, "pending", res.Data["approval_status"])

	// Mutating the caller's args after the request must not change what runs.
	args["name"] = "api-def456"

	code, _ := h.promptCode(t)
	assert.True(t, h.reply("YES "+code))
	h.mgr.Wait()

	assert.False(t, podExists(t, h.tool, "web-abc123"), "approved delete did not run")
	assert.True(t, podExists(t, h.tool, "api-def456"), "a different object than the approved one was touched")

	var done string
	for _, n := range h.allNotices() {
		if strings.HasPrefix(n.Text, "Approved (") {
			done = n.Text
		}
	}
	assert.Contains(t, done, "Deleted pods/web-abc123 in namespace default on cluster test-cluster")

	// Single use: replaying the code does nothing more.
	assert.True(t, h.reply("YES "+code))
	h.mgr.Wait()
	assert.True(t, podExists(t, h.tool, "api-def456"))
}

func TestK8sApproval_NonInteractiveFailsClosed(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"cron":      approval.WithNonInteractive(context.Background(), "cron"),
		"heartbeat": approval.WithNonInteractive(context.Background(), "heartbeat"),
		"mcp":       approval.WithNonInteractive(context.Background(), "mcp"),
		"unknown":   context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newApprovalHarness(t, []string{"dangerous"})
			res, err := h.tool.Execute(ctx, deleteArgs("web-abc123"))
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Contains(t, res.Error, "NOT RUN")
			assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
			assert.True(t, podExists(t, h.tool, "web-abc123"))
			assert.Empty(t, h.mgr.Pending("s1"))
			assert.Empty(t, h.allNotices())
		})
	}
}

func TestK8sApproval_NoApproverFailsClosed(t *testing.T) {
	h := newApprovalHarness(t, []string{"dangerous"})
	h.tool.services = nil
	res, err := h.tool.Execute(h.interactive(), deleteArgs("web-abc123"))
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_no_approver", res.Data["approval_status"])
	assert.True(t, podExists(t, h.tool, "web-abc123"))
}

func TestK8sApproval_OpsOutsideTierUnaffected(t *testing.T) {
	h := newApprovalHarness(t, []string{"dangerous"})
	// Non-interactive on purpose: an ungated op must not consult approval.
	ctx := approval.WithNonInteractive(context.Background(), "cron")

	res, err := h.tool.Execute(ctx, map[string]interface{}{
		"action": "scale", "resource": "deploy", "name": "web", "replicas": float64(2),
	})
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "Scaled")

	res, err = h.tool.Execute(ctx, map[string]interface{}{"action": "get", "resource": "pods"})
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)
	assert.Empty(t, h.allNotices())
}

func TestK8sApproval_ReadTierCanBeGated(t *testing.T) {
	h := newApprovalHarness(t, []string{"read"})
	res, err := h.tool.Execute(h.interactive(), map[string]interface{}{"action": "get", "resource": "pods", "name": "web-abc123"})
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Data["approval_status"])
	code, _ := h.promptCode(t)
	assert.True(t, h.reply("YES "+code))
	h.mgr.Wait()
	assert.Contains(t, h.allNotices()[len(h.allNotices())-1].Text, "Retrieved pods/web-abc123")
}

func TestK8sApproval_ExecPromptRedactsSecrets(t *testing.T) {
	h := newApprovalHarness(t, []string{"dangerous"})
	res, err := h.tool.Execute(h.interactive(), map[string]interface{}{
		"action": "exec", "name": "web-abc123", "container": "web",
		"command": "env DB_PASSWORD=hunter2hunter2 psql --token abcdef123456 -c 'select 1'",
	})
	require.NoError(t, err)
	require.Equal(t, "pending", res.Data["approval_status"])
	_, prompt := h.promptCode(t)
	assert.NotContains(t, prompt, "hunter2hunter2")
	assert.NotContains(t, prompt, "abcdef123456")
	assert.Contains(t, prompt, "Command: env DB_PASSWORD=[redacted] psql --token [redacted] -c 'select 1'")
	assert.Contains(t, prompt, "Container: web")
	assert.Contains(t, prompt, "Verb: exec")
}
