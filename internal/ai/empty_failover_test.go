package ai

import (
	"context"
	"strings"
	"testing"
)

// conduit-1z0g: after the same-model retry also returns raw-empty, the guard
// makes ONE cross-model attempt via the failover router (fallback_model on
// its OWN provider — bd-27ud), refusing same-provider "failover". These tests
// cover the full decision tree.
//
// NOTE: in retry tests the ORIGINAL empty response is a literal argument to
// GuardEmptyResponse — only the RETRY and FAILOVER calls hit mock providers.

// failoverRouterStub is a controllable EmptyFailoverRouter for tests.
type failoverRouterStub struct {
	model         string
	provider      Provider
	ok            bool
	askedFor      string
	calls         int
	returnSamePov bool // pathological resolver: returns the failed provider itself
}

func (s *failoverRouterStub) ResolveEmptyFailover(failedProvider string) (string, Provider, bool) {
	s.calls++
	s.askedFor = failedProvider
	if s.returnSamePov {
		return "z-ai/glm-5.3", s.provider, true
	}
	if !s.ok {
		return "", nil, false
	}
	return s.model, s.provider, true
}

// --- Router.ResolveEmptyFailover unit tests (real Router, mock providers) ---

func newFailoverTestRouter() *Router {
	r := &Router{
		providers:    map[string]Provider{},
		providerMeta: map[string]ProviderMeta{},
	}
	r.providers["z-ai"] = NewMockProvider("z-ai")
	r.providers["anthropic"] = NewMockProvider("anthropic")
	// Type fields matter: ResolveProviderForModel's Tier-1 heuristic routes
	// "claude-*" models by provider TYPE ("anthropic"), matching production.
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai"}
	r.providerMeta["anthropic"] = ProviderMeta{Name: "anthropic", Type: "anthropic"}
	return r
}

func TestResolveEmptyFailover_DifferentProvider(t *testing.T) {
	r := newFailoverTestRouter()
	// Production-critical config: z-ai's fallback_model points at an
	// anthropic model — the cross-backend hop this whole fix exists for.
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai", FallbackModel: "claude-sonnet-4-6"}

	model, p, ok := r.ResolveEmptyFailover("z-ai")
	if !ok {
		t.Fatal("expected ok=true when fallback resolves to a different provider")
	}
	if model != "claude-sonnet-4-6" {
		t.Fatalf("expected fallback model claude-sonnet-4-6, got %q", model)
	}
	if p.Name() != "anthropic" {
		t.Fatalf("expected anthropic provider, got %q", p.Name())
	}
}

func TestResolveEmptyFailover_SameProviderRefused(t *testing.T) {
	r := newFailoverTestRouter()
	// Misconfiguration: z-ai's fallback_model resolves back to z-ai itself
	// (the bd-6tb default has this shape). Must be refused — retrying the
	// backend that just returned empty twice is not failover (bd-27ud).
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai", FallbackModel: "z-ai/glm-5.3"}

	_, _, ok := r.ResolveEmptyFailover("z-ai")
	if ok {
		t.Fatal("same-provider failover must be refused")
	}
}

func TestResolveEmptyFailover_DefaultFallbackResolvesToSelf_Refused(t *testing.T) {
	r := newFailoverTestRouter()
	// No FallbackModel configured on z-ai → bd-6tb default "z-ai/glm-5.3"
	// resolves back to z-ai itself. Must be refused, not blindly retried.
	_, _, ok := r.ResolveEmptyFailover("z-ai")
	if ok {
		t.Fatal("default fallback that resolves to the failed provider must be refused")
	}
}

func TestResolveEmptyFailover_UnknownProvider(t *testing.T) {
	r := newFailoverTestRouter()

	_, _, ok := r.ResolveEmptyFailover("nonexistent")
	if ok {
		t.Fatal("unknown provider must yield ok=false")
	}
}

// --- Guard-level failover tests (stub router + mock providers) ---

func TestGuardEmptyResponse_FailoverRecovers(t *testing.T) {
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	failover := NewMockProvider("anthropic")
	failover.SetResponses([]MockResponse{
		{Content: "recovered via failover"}, // cross-model attempt succeeds
	})
	stub := &failoverRouterStub{model: "claude-sonnet-4-6", provider: failover, ok: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered via failover" {
		t.Fatalf("expected failover content, got %q", resp.Content)
	}
	if sameModel.GetCallCount() != 1 {
		t.Fatalf("expected 1 same-provider call (the retry), got %d", sameModel.GetCallCount())
	}
	if failover.GetCallCount() != 1 {
		t.Fatalf("expected exactly 1 failover call, got %d", failover.GetCallCount())
	}
	if stub.askedFor != "z-ai" {
		t.Fatalf("failover router should be asked about the FAILED provider, got %q", stub.askedFor)
	}
}

func TestGuardEmptyResponse_SameProviderFailoverRefused(t *testing.T) {
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	pathological := NewMockProvider("z-ai") // resolver returns the SAME provider
	pathological.SetResponses([]MockResponse{
		{Content: "should never be reached"},
	})
	stub := &failoverRouterStub{provider: pathological, ok: true, returnSamePov: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("guard must not error on refused failover: %v", err)
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("expected visible fallback after refusal, got %q", resp.Content)
	}
	if pathological.GetCallCount() != 0 {
		t.Fatalf("same-provider failover must never call the failed backend, got %d calls", pathological.GetCallCount())
	}
}

func TestGuardEmptyResponse_NoFailoverRoute(t *testing.T) {
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	stub := &failoverRouterStub{ok: false} // no route resolvable
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("expected visible fallback when no failover route, got %q", resp.Content)
	}
	if stub.calls != 1 {
		t.Fatalf("expected exactly 1 failover resolution attempt, got %d", stub.calls)
	}
}

func TestGuardEmptyResponse_NilRouterDegradesTo18vj(t *testing.T) {
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	SetEmptyFailoverRouter(nil) // explicit: no failover wired

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("expected conduit-18vj fallback, got %q", resp.Content)
	}
	if sameModel.GetCallCount() != 1 {
		t.Fatalf("expected only the same-model retry, got %d calls", sameModel.GetCallCount())
	}
}

func TestGuardEmptyResponse_FailoverAlsoEmptyDeliversFallback(t *testing.T) {
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	failover := NewMockProvider("anthropic")
	failover.SetResponses([]MockResponse{
		{Content: "  "}, // failover ALSO empty (provider-wide outage)
	})
	stub := &failoverRouterStub{model: "claude-sonnet-4-6", provider: failover, ok: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("guard must not error on triple-empty: %v", err)
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("expected visible fallback when failover also empty, got %q", resp.Content)
	}
	if failover.GetCallCount() != 1 {
		t.Fatalf("expected exactly 1 failover attempt, got %d", failover.GetCallCount())
	}
}

func TestGuardEmptyResponse_FailoverErrorDeliversFallback(t *testing.T) {
	sameModel := NewMockProvider("z-ai")
	sameModel.SetResponses([]MockResponse{
		{Content: "   "}, // same-model retry ALSO empty
	})
	failover := NewMockProvider("anthropic")
	failover.AddErrorResponse(context.DeadlineExceeded)
	stub := &failoverRouterStub{model: "claude-sonnet-4-6", provider: failover, ok: true}
	SetEmptyFailoverRouter(stub)
	defer SetEmptyFailoverRouter(nil)

	resp, err := GuardEmptyResponse(context.Background(), sameModel,
		&GenerateRequest{Model: "z-ai/glm-5.3"}, &GenerateResponse{Content: ""}, nil, "test")
	if err != nil {
		t.Fatalf("guard must swallow failover errors and deliver fallback: %v", err)
	}
	if !strings.Contains(resp.Content, "empty response") {
		t.Fatalf("expected visible fallback, got %q", resp.Content)
	}
}
