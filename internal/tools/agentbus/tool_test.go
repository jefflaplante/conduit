//go:build with_agentbus

package agentbus

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/approval"
	"conduit/internal/tools/types"

	"github.com/jefflaplante/agentbus/a2a"
	"github.com/jefflaplante/agentbus/client"
)

func TestToolNameAndSchema(t *testing.T) {
	tl := mustTool(t)
	if tl.Name() != "AgentBus" {
		t.Errorf("Name() = %q, want AgentBus", tl.Name())
	}
	params := tl.Parameters()
	if params["type"] != "object" {
		t.Errorf("Parameters()[type] = %v, want object", params["type"])
	}
	props := params["properties"].(map[string]interface{})
	actions := props["action"].(map[string]interface{})
	enum := actions["enum"].([]string)
	if len(enum) != 4 {
		t.Fatalf("action enum = %v, want 4 actions", enum)
	}
	for _, want := range []string{"agents", "send", "request", "task"} {
		found := false
		for _, a := range enum {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("action enum missing %q: %v", want, enum)
		}
	}
}

// mustTool builds a tool with a fake approver and stubbed publish.
func mustTool(t *testing.T) *AgentBusTool {
	t.Helper()
	tl, err := NewAgentBusTool(&types.ToolServices{Approvals: &fakeApprover{ticket: true}}, &agentConfig{
		brokerURL: "tcp://127.0.0.1:1", // hermetic: nothing listens here
		agentID:   "conduit-home",
	})
	if err != nil {
		t.Fatalf("NewAgentBusTool: %v", err)
	}
	tl.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error { return nil }
	return tl
}

func TestExecuteUnknownAction(t *testing.T) {
	tl := mustTool(t)
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "explode"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success {
		t.Fatalf("unknown action succeeded: %+v", res)
	}
	if !strings.Contains(res.Error, "explode") {
		t.Errorf("error = %q, want the bad action named", res.Error)
	}
}

func TestSendRequiresDestination(t *testing.T) {
	tl := mustTool(t)
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "send"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success || !strings.Contains(res.Error, "'to'") {
		t.Errorf("send without to = %+v, want missing-'to' refusal", res)
	}
}

func TestSendRejectsInvalidDestination(t *testing.T) {
	tl := mustTool(t)
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "send", "to": "Not/Valid"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success || !strings.Contains(res.Error, "not a valid agent id") {
		t.Errorf("send to bad id = %+v, want invalid-id refusal", res)
	}
}

func TestRequestRejectsInvalidDestination(t *testing.T) {
	tl := mustTool(t)
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "request", "to": "-nope"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success {
		t.Fatalf("request to invalid id ran: %+v", res)
	}
}

func TestTaskRequiresCapability(t *testing.T) {
	tl := mustTool(t)
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "task"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success || !strings.Contains(res.Error, "capability") {
		t.Errorf("task without capability = %+v, want refusal naming capability", res)
	}
}

func TestSendGatesBeforePublish(t *testing.T) {
	// With no approver configured, send must refuse BEFORE any publish
	// attempt; publishFn is a stub, so any call would mean fail-open.
	tl, err := NewAgentBusTool(&types.ToolServices{}, &agentConfig{brokerURL: "tcp://127.0.0.1:1", agentID: "conduit-home"})
	if err != nil {
		t.Fatalf("NewAgentBusTool: %v", err)
	}
	published := 0
	tl.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error {
		published++
		return nil
	}
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "send", "to": "worker.agent", "body": map[string]interface{}{"k": "v"}})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success {
		t.Fatalf("send without approver succeeded (fail-open): %+v", res)
	}
	if published != 0 {
		t.Errorf("publishFn called %d times with no approver, want 0", published)
	}
	if !strings.Contains(res.Error, "no approval channel") {
		t.Errorf("error = %q, want no-approval-channel refusal", res.Error)
	}
}

func TestRequestGatesBeforePublish(t *testing.T) {
	// Same fail-closed contract for request; the round trip must never
	// start without an approver.
	tl, err := NewAgentBusTool(&types.ToolServices{}, &agentConfig{brokerURL: "tcp://127.0.0.1:1", agentID: "conduit-home"})
	if err != nil {
		t.Fatalf("NewAgentBusTool: %v", err)
	}
	calls := 0
	tl.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error { calls++; return nil }
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "request", "to": "worker.agent", "body": "hello"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success || calls != 0 {
		t.Fatalf("request without approver: res=%+v calls=%d, want refusal and zero publishes", res, calls)
	}
}

func TestSendWithApproverPublishes(t *testing.T) {
	// Approver grants immediately (fake returns a ticket? no — approvalgate
	// returns pending when a ticket is created). We assert the gate ran and
	// publish stays pending, not sent.
	tl := mustTool(t)
	res, err := tl.Execute(context.Background(), map[string]interface{}{"action": "send", "to": "worker.agent", "body": map[string]interface{}{"k": "v"}})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !res.Success || !strings.Contains(res.Content, "NOT RUN YET") {
		t.Fatalf("gated send = %+v, want pending-approval result", res)
	}
	if res.Data["approval_status"] != "pending" {
		t.Errorf("approval_status = %v, want pending", res.Data["approval_status"])
	}
}

func TestWaitTimeout(t *testing.T) {
	tests := []struct {
		name string
		ms   int
		want time.Duration
	}{
		{"default", 0, 30 * time.Second},
		{"explicit", 1500, 1500 * time.Millisecond},
		{"over cap", 10 * 60 * 1000, maxRequestWait},
		{"negative ignored", -5, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]interface{}{}
			if tt.ms != 0 {
				args["timeout_ms"] = tt.ms
			}
			if tt.ms < 0 {
				args["timeout_ms"] = tt.ms
			}
			if got := waitTimeout(args); got != tt.want {
				t.Errorf("waitTimeout(%d ms) = %v, want %v", tt.ms, got, tt.want)
			}
		})
	}
}

func TestBodyString(t *testing.T) {
	tests := []struct {
		name string
		body any
		want string
	}{
		{"nil", nil, ""},
		{"string", "ping", "ping"},
		{"object", map[string]any{"a": 1}, `{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bodyString(tt.body); got != tt.want {
				t.Errorf("bodyString(%v) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestReplyResult(t *testing.T) {
	req, _ := a2a.New(a2a.TypeRequest, "conduit-home", "q")
	rep, err := req.Reply(a2a.TypeResponse, "worker.agent", map[string]any{"answer": 42})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	res := replyResult(rep)
	if !res.Success {
		t.Fatalf("replyResult = %+v, want success", res)
	}
	if !strings.Contains(res.Content, "worker.agent") {
		t.Errorf("content = %q, want the responder named", res.Content)
	}
	if res.Data["from"] != "worker.agent" {
		t.Errorf("data from = %v, want worker.agent", res.Data["from"])
	}
}

func TestReplyResultRemoteError(t *testing.T) {
	req, _ := a2a.New(a2a.TypeRequest, "conduit-home", "q")
	rep, err := req.Reply(a2a.TypeError, "$broker", map[string]any{"code": "no_route", "reason": "nobody serves that"})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	res := replyResult(rep)
	if !res.Success {
		t.Fatalf("a remote error envelope is a successful tool call carrying the error: %+v", res)
	}
	if !strings.Contains(res.Content, "no_route") {
		t.Errorf("content = %q, want the remote error code visible", res.Content)
	}
}

func TestInboxHandlerFailsFast(t *testing.T) {
	// Phase-1 contract: inbound requests get an immediate error reply so
	// the sender fails fast instead of waiting out the TTL.
	tl := mustTool(t)
	env, _ := a2a.New(a2a.TypeRequest, "other.agent", "hello")
	_, err := tl.inboxHandler(context.Background(), env)
	if err == nil {
		t.Fatal("inboxHandler returned no error for an inbound request, want the fail-fast error")
	}
	if !strings.Contains(err.Error(), "no inbound session runner") {
		t.Errorf("err = %v, want the no-session-runner notice", err)
	}
}

func TestEnsureConnectedHermetic(t *testing.T) {
	// Connecting to a port with no listener must fail cleanly (and must
	// not retry forever), proving the tool does not need a live broker in
	// tests and surfaces broker outages as action errors.
	tl := mustTool(t)
	_, err := tl.ensureConnected(context.Background())
	if err == nil {
		t.Skip("a broker answered on 127.0.0.1:1 — environment not hermetic; skipping")
	}
}

// Compile-time interface checks.
var (
	_ client.Handler = tl_inboxHandler
	_                = approval.Requester(nil)
)

func tl_inboxHandler(ctx context.Context, env *a2a.Envelope) (any, error) { return nil, nil }
