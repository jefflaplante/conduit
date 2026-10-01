package skills

import (
	"context"
	"strings"

	"conduit/internal/policy"
)

// classifySkillActions maps a skill call to policy actions (conduit-25lt.2).
// Only gog/email sends have outward effects; the account decision is
// sendUsesOwner, the same one the owner-send approval gate uses.
func (e *Executor) classifySkillActions(skill Skill, action string, args map[string]interface{}) []policy.Action {
	if !isEmailSend(skill, action) {
		return nil
	}
	class := policy.EmailSendAgent
	if e.gog.sendUsesOwner(args) {
		class = policy.EmailSendOwner
	}
	to, _ := args["to"].(string)
	var out []policy.Action
	for _, addr := range strings.Split(to, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			out = append(out, policy.Action{Class: class, Target: strings.ToLower(addr)})
		}
	}
	if len(out) == 0 {
		out = append(out, policy.Action{Class: class})
	}
	return out
}

// ClassifyActions implements policy.Classifier for a skill tool.
func (st *SkillTool) ClassifyActions(_ context.Context, args map[string]interface{}) []policy.Action {
	action, _ := args["action"].(string)
	skillArgs, _ := args["args"].(map[string]interface{})
	return st.executor.classifySkillActions(st.skill, action, skillArgs)
}

// ClassifyActions implements policy.Classifier for a per-action skill tool;
// arguments merge exactly as Execute merges them.
func (sat *SkillActionTool) ClassifyActions(_ context.Context, args map[string]interface{}) []policy.Action {
	merged := map[string]interface{}{}
	if inner, ok := args["args"].(map[string]interface{}); ok {
		for k, v := range inner {
			merged[k] = v
		}
	}
	for k, v := range args {
		if k != "args" {
			merged[k] = v
		}
	}
	return sat.executor.classifySkillActions(sat.skill, sat.action, merged)
}

// ClassifyActions passes classification through the adapter when the
// wrapped skill tool supports it.
func (a *SkillToolAdapter) ClassifyActions(ctx context.Context, args map[string]interface{}) []policy.Action {
	if c, ok := a.skillTool.(policy.Classifier); ok {
		return c.ClassifyActions(ctx, args)
	}
	return nil
}
