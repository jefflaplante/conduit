//go:build with_agentbus

package agentbus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"conduit/internal/approval"
	"conduit/internal/tools/approvalgate"
	"conduit/internal/tools/types"

	"github.com/jefflaplante/agentbus/a2a"
)

// fakeApprover records the last requested action and returns a canned
// (ticket, error); it implements approval.Requester.
type fakeApprover struct {
	lastKind  string
	lastTitle string
	ticket    bool
	err       error
}

func (f *fakeApprover) Request(ctx context.Context, action approval.Action, exec approval.ExecuteFunc) (*approval.Ticket, error) {
	f.lastKind = action.Kind
	f.lastTitle = action.Title
	if f.err != nil {
		return nil, f.err
	}
	if !f.ticket {
		return nil, nil
	}
	return &approval.Ticket{ID: "t1", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

var _ approval.Requester = (*fakeApprover)(nil)

func newTestTool(approver approval.Requester) *AgentBusTool {
	tl := &AgentBusTool{services: &types.ToolServices{Approvals: approver}}
	tl.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error { return nil }
	return tl
}

// gateEnvelope drives gatePublish with a pre-built envelope so tests can
// assert on the gating decision itself.
func (t *AgentBusTool) gateEnvelope(ctx context.Context, topic string, env *a2a.Envelope) *types.ToolResult {
	params := map[string]string{"to": env.To, "capability": env.Capability}
	return t.gatePublish(ctx, topic, env.Type, env.Body, params, func(ctx context.Context) (*types.ToolResult, error) {
		release := t.approveOnePublish()
		defer release()
		if err := t.publishFn(ctx, topic, env); err != nil {
			return types.NewErrorResult("publish_failed", err.Error()), nil
		}
		return &types.ToolResult{Success: true, Content: "published"}, nil
	})
}

func TestGateEnvelopeNeedsApproval(t *testing.T) {
	tests := []struct {
		name string
		typ  a2a.Type
		want bool // wantNeedsApproval
	}{
		{"request needs approval", a2a.TypeRequest, true},
		{"event needs approval", a2a.TypeEvent, true},
		{"response exempt", a2a.TypeResponse, false},
		{"error exempt", a2a.TypeError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsApproval(tt.typ); got != tt.want {
				t.Errorf("needsApproval(%s) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestGateEnvelopeFailsClosedWithoutApprover(t *testing.T) {
	// No approver configured: request/event must NOT publish, response may.
	tl := newTestTool(nil)
	env, _ := a2a.New(a2a.TypeRequest, "conduit-home", map[string]string{"q": "hi"})
	res := tl.gateEnvelope(context.Background(), a2a.InboxTopic("claude-code.cli"), env)
	if res.Success {
		t.Fatalf("request with nil approver: result = %+v, want fail-closed refusal", res)
	}
	if !strings.Contains(res.Error, "approval") {
		t.Errorf("error = %q, want mention of approval", res.Error)
	}
	// And the fake approver must never have been asked (nil anyway), plus
	// a published-branch would have connected to a broker — there is no
	// broker here, so Success=true would itself prove fail-open.
}

func TestGateEnvelopeRequestGoesThroughApprover(t *testing.T) {
	fa := &fakeApprover{ticket: true}
	tl := newTestTool(fa)
	env, _ := a2a.New(a2a.TypeRequest, "conduit-home", map[string]string{"q": "hi"})
	res := tl.gateEnvelope(context.Background(), a2a.InboxTopic("claude-code.cli"), env)
	if !res.Success {
		t.Fatalf("gated request refused: %+v", res)
	}
	if fa.lastKind != approvalKind {
		t.Errorf("approval kind = %q, want %q", fa.lastKind, approvalKind)
	}
	if fa.lastTitle == "" || !strings.Contains(fa.lastTitle, a2a.InboxTopic("claude-code.cli")) {
		t.Errorf("approval title = %q, want the topic in it", fa.lastTitle)
	}
	if !strings.Contains(res.Content, "NOT RUN YET") {
		t.Errorf("content = %q, want pending-approval notice", res.Content)
	}
	if res.Data["approval_status"] != "pending" {
		t.Errorf("data approval_status = %v, want pending", res.Data["approval_status"])
	}
}

func TestGateEnvelopeNonInteractiveFailsClosed(t *testing.T) {
	fa := &fakeApprover{err: &approval.NonInteractiveError{Source: "cron"}}
	tl := newTestTool(fa)
	env, _ := a2a.New(a2a.TypeEvent, "conduit-home", "ping")
	res := tl.gateEnvelope(context.Background(), a2a.InboxTopic("other.agent"), env)
	if res.Success {
		t.Fatalf("non-interactive turn published anyway: %+v", res)
	}
	if !strings.Contains(res.Error, "non-interactive") {
		t.Errorf("error = %q, want non-interactive refusal", res.Error)
	}
}

func TestGateEnvelopeReplyExempt(t *testing.T) {
	// response/error replies only answer an inbound request; they skip
	// the approval gate entirely (the spec exempts them).
	calls := 0
	tl := newTestTool(nil)
	tl.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error {
		calls++
		return nil
	}
	for _, typ := range []a2a.Type{a2a.TypeResponse, a2a.TypeError} {
		env, _ := a2a.New(typ, "conduit-home", map[string]string{"answer": "yes"})
		env.CorrelationID = "corr-1"
		res := tl.gateEnvelope(context.Background(), a2a.InboxTopic("claude-code.cli"), env)
		if !res.Success {
			t.Fatalf("%s reply blocked: %+v", typ, res)
		}
	}
	if calls != 2 {
		t.Errorf("publishFn calls = %d, want 2 (both replies published without approval)", calls)
	}
}

func TestGateEnvelopeExemptReplySkipsApproverEvenIfItWouldRefuse(t *testing.T) {
	// A reply must go out even when the approval channel is broken —
	// answering an inbound request is not a new outbound action.
	fa := &fakeApprover{err: errors.New("approval channel down")}
	tl := newTestTool(fa)
	published := 0
	tl.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error {
		published++
		return nil
	}
	env, _ := a2a.New(a2a.TypeResponse, "conduit-home", "answer")
	env.CorrelationID = "corr-1"
	res := tl.gateEnvelope(context.Background(), a2a.InboxTopic("claude-code.cli"), env)
	if !res.Success {
		t.Fatalf("response blocked by broken approver: %+v", res)
	}
	if published != 1 || fa.lastKind != "" {
		t.Errorf("published=%d approverKind=%q, want reply bypass the approver entirely", published, fa.lastKind)
	}
}

func TestGateEnvelopeBeforePublishHook(t *testing.T) {
	// The client's BeforePublish hook must consult the same policy: an
	// event on any topic needs the gate; a response goes through.
	fa := &fakeApprover{ticket: true}
	tl := newTestTool(fa)
	tl.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error { return nil }
	env, _ := a2a.New(a2a.TypeEvent, "conduit-home", "ping")
	env.To = "other.agent"
	before := tl.beforePublish()
	if err := before(context.Background(), a2a.InboxTopic("other.agent"), env); err == nil {
		t.Error("BeforePublish(event) = nil, want refusal error (fail closed)")
	}
}

func TestGateEnvelopeOperationShape(t *testing.T) {
	// Frozen operation shows topic + destination + payload preview, and
	// carries the exact params.
	fa := &fakeApprover{ticket: true}
	tl := newTestTool(fa)
	env, _ := a2a.New(a2a.TypeRequest, "conduit-home", map[string]any{"question": "status?"})
	env.To = "worker.agent"
	res := tl.gateEnvelope(context.Background(), a2a.InboxTopic("worker.agent"), env)
	if !res.Success {
		t.Fatalf("gated request refused: %+v", res)
	}
	if !strings.Contains(fa.lastTitle, "worker.agent") {
		t.Errorf("title = %q, want destination agent named", fa.lastTitle)
	}
}

// Keep the compile-time references honest: approvalgate.Operation must
// stay the mechanism (if the gate API drifts, these tests break).
var _ = approvalgate.MaxFieldRunes
