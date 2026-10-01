package tools

import (
	"context"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/policy"
	"conduit/internal/tools/types"
)

type recordingPolicy struct{ recs []policy.Record }

func (r *recordingPolicy) Record(rec policy.Record) { r.recs = append(r.recs, rec) }

// outwardTool always reaches a group chat.
type outwardTool struct{ ran bool }

func (o *outwardTool) Name() string                       { return "Outward" }
func (o *outwardTool) Description() string                { return "test" }
func (o *outwardTool) Parameters() map[string]interface{} { return map[string]interface{}{} }
func (o *outwardTool) Execute(context.Context, map[string]interface{}) (*types.ToolResult, error) {
	o.ran = true
	return &types.ToolResult{Success: true}, nil
}
func (o *outwardTool) ClassifyActions(context.Context, map[string]interface{}) []policy.Action {
	return []policy.Action{{Class: policy.MessageGroup, Target: "telegram:-100"}}
}

// conduit-25lt.2 shadow mode: a would-deny is recorded and the tool still runs.
func TestExecuteTool_PolicyShadowRecordsAndRuns(t *testing.T) {
	r := NewRegistry(config.ToolsConfig{EnabledTools: []string{"Outward"}})
	tool := &outwardTool{}
	r.tools["Outward"] = tool
	r.enabledTools[normalizeToolName("Outward")] = true
	rec := &recordingPolicy{}
	r.SetPolicyEngine(policy.New(config.ToolPolicyConfig{}.Resolve(nil), rec))

	ctx := approval.WithNonInteractive(context.Background(), "cron")
	res, err := r.ExecuteTool(ctx, "Outward", map[string]interface{}{"purpose": "weekly digest"})
	if err != nil || !res.Success || !tool.ran {
		t.Fatalf("shadow mode must not block: %+v %v ran=%v", res, err, tool.ran)
	}
	if len(rec.recs) != 1 {
		t.Fatalf("records = %d", len(rec.recs))
	}
	got := rec.recs[0]
	if got.Tool != "Outward" || got.Class != policy.MessageGroup || got.Decision != policy.Deny ||
		got.Origin != "cron" || got.Purpose != "weekly digest" {
		t.Fatalf("record = %+v", got)
	}
}

func TestExecuteTool_NoPolicyEngineIsUnchanged(t *testing.T) {
	r := NewRegistry(config.ToolsConfig{EnabledTools: []string{"Outward"}})
	tool := &outwardTool{}
	r.tools["Outward"] = tool
	r.enabledTools[normalizeToolName("Outward")] = true
	if res, err := r.ExecuteTool(context.Background(), "Outward", nil); err != nil || !res.Success {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestGoogleWorkspace_ClassifyActions(t *testing.T) {
	g := &GoogleWorkspaceTool{}
	got := g.ClassifyActions(context.Background(), map[string]interface{}{
		"action": "email_send", "to": "A@Example.com, b@example.com", "cc": "c@example.com", "bcc": "",
	})
	if len(got) != 3 || got[0].Class != policy.EmailSendAgent || got[0].Target != "a@example.com" || got[2].Target != "c@example.com" {
		t.Fatalf("actions = %+v", got)
	}
	if g.ClassifyActions(context.Background(), map[string]interface{}{"action": "email_search"}) != nil {
		t.Fatal("reads have no outward actions")
	}
}
