package tools

import (
	"context"
	"log"

	"conduit/internal/policy"
)

// SetPolicyEngine installs the tool action policy (conduit-25lt.2). Nil
// disables it. Safe to call while tools run.
func (r *Registry) SetPolicyEngine(e *policy.Engine) {
	r.mu.Lock()
	r.policy = e
	r.mu.Unlock()
}

func (r *Registry) policyEngine() *policy.Engine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

// observePolicy classifies a tool call and records the policy's decision.
// Shadow mode (phase 1): it never blocks; a would-ask or would-deny is
// logged so the shadow week can be reviewed (`conduit policy report`).
func (r *Registry) observePolicy(ctx context.Context, name string, tool Tool, args map[string]interface{}) {
	eng := r.policyEngine()
	if eng == nil {
		return
	}
	c, ok := tool.(policy.Classifier)
	if !ok {
		return
	}
	actions := c.ClassifyActions(ctx, args)
	if len(actions) == 0 {
		return
	}
	purpose, _ := args["purpose"].(string)
	for _, res := range eng.Observe(ctx, name, purpose, actions) {
		if res.Decision != policy.Allow {
			log.Printf("[policy] shadow: %s %s would %s (%s)", name, res.Class, res.Decision, res.Rule)
		}
	}
}
