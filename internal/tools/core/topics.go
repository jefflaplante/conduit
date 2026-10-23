package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

// TopicManager is the gateway capability behind the topic_* tools
// (conduit-43vk). Like SubAgentCanceller it is asserted on
// ToolServices.Gateway rather than added to types.GatewayService, so tool
// fakes need not implement it. Each method is the same internal function the
// /topic command uses; chat-scoped operations (switch, list's ★, the pointer
// moves of park/close) act on the chat in the request context. Every result
// ends with the chat's current topic state.
type TopicManager interface {
	TopicList(ctx context.Context) (string, error)
	TopicCreate(ctx context.Context, slug, name, persona string) (string, error)
	TopicSwitch(ctx context.Context, slug string) (string, error)
	TopicRename(ctx context.Context, slug, newName, newSlug string) (string, error)
	TopicPark(ctx context.Context, slug string) (string, error)
	TopicClose(ctx context.Context, slug string) (string, error)
	TopicStitch(ctx context.Context, from, to string) (string, error)
	TopicPersona(ctx context.Context, slug, persona string) (string, error)
	TopicProfile(ctx context.Context, slug, profile string) (string, error)
}

// topicParam describes one string parameter of a topic tool.
type topicParam struct {
	name, desc string
	required   bool
	examples   []interface{}
}

// TopicTool is one topic_* tool: a thin wrapper that validates arguments and
// calls the matching TopicManager method.
type TopicTool struct {
	services *types.ToolServices
	name     string
	desc     string
	params   []topicParam
	call     func(ctx context.Context, m TopicManager, args map[string]interface{}) (string, error)
}

var (
	slugParam = topicParam{name: "slug", required: true,
		desc:     "Topic slug (lowercase-hyphen, max 32 chars; other text is normalized, e.g. \"Solar Panels\" → solar-panels)",
		examples: []interface{}{"conduit-feature", "solar-panels"}}
	optSlug = func(p topicParam, desc string) topicParam { p.desc = desc; return p }
)

func str(args map[string]interface{}, key string) string { return toolargs.GetString(args, key, "") }

// profileArg returns the profile argument as JSON text. Models often send
// it as an object rather than a string, so non-strings are re-encoded; the
// gateway validates the JSON either way (conduit-331g).
func profileArg(args map[string]interface{}) string {
	switch v := args["profile"].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

// NewTopicTools returns the nine topic_* tools.
func NewTopicTools(services *types.ToolServices) []types.Tool {
	mk := func(name, desc string, params []topicParam, call func(context.Context, TopicManager, map[string]interface{}) (string, error)) types.Tool {
		return &TopicTool{services: services, name: name, desc: desc, params: params, call: call}
	}
	return []types.Tool{
		mk("topic_list",
			"List this chat's topics (slug, name, status, last activity, ~tokens; ★ = current topic). Topics give each project its own session — its own transcript, compaction and token budget — while home stays the hub.",
			nil,
			func(ctx context.Context, m TopicManager, _ map[string]interface{}) (string, error) {
				return m.TopicList(ctx)
			}),
		mk("topic_create",
			"Create a topic without switching to it. Fails if the slug is taken. Use topic_switch to create-and-switch in one step.",
			[]topicParam{slugParam,
				{name: "name", desc: "Display name (defaults to the slug)", examples: []interface{}{"Conduit feature work"}},
				{name: "persona", desc: "Optional prompt fragment injected into this topic's turns"}},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicCreate(ctx, str(a, "slug"), str(a, "name"), str(a, "persona"))
			}),
		mk("topic_switch",
			"Point this chat at a topic (created if unknown; a parked topic is unparked). The user's NEXT message goes to that topic's session; your current reply still lands in this session. Use \"home\" to return to the main session. Proactively switch when the user starts a distinct new project.",
			[]topicParam{optSlug(slugParam, "Topic slug to switch to, or \"home\"")},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicSwitch(ctx, str(a, "slug"))
			}),
		mk("topic_rename",
			"Rename a topic's display name and/or slug. A new slug moves every chat pointer with it; the old slug stops resolving. Without new_slug, the slug is derived from new_name.",
			[]topicParam{slugParam,
				{name: "new_name", desc: "New display name"},
				{name: "new_slug", desc: "New slug (optional)"}},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicRename(ctx, str(a, "slug"), str(a, "new_name"), str(a, "new_slug"))
			}),
		mk("topic_park",
			"Park a topic: store a 2-3 sentence summary and mark it parked. Chats pointing at it go home. Switching to it later unparks it.",
			[]topicParam{slugParam},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicPark(ctx, str(a, "slug"))
			}),
		mk("topic_close",
			"Close a finished topic: store a final summary in the topic and its session context, and archive it. Chats pointing at it go home.",
			[]topicParam{slugParam},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicClose(ctx, str(a, "slug"))
			}),
		mk("topic_stitch",
			"Hand context across topics: prepend topic `from`'s summary to topic `to`'s prompt prefix (kept until cleared; `from` is unchanged).",
			[]topicParam{
				{name: "from", required: true, desc: "Source topic slug"},
				{name: "to", required: true, desc: "Destination topic slug"}},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicStitch(ctx, str(a, "from"), str(a, "to"))
			}),
		mk("topic_persona",
			"Set a topic's persona — a free-text prompt fragment injected into that topic's turns after the topic line. Empty persona clears it.",
			[]topicParam{slugParam,
				{name: "persona", desc: "Persona text; empty clears it", examples: []interface{}{"You are a terse release engineer."}}},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicPersona(ctx, str(a, "slug"), str(a, "persona"))
			}),
		mk("topic_profile",
			"Set a topic's prompt profile (lean mode): a JSON object {\"skills\": [...], \"skip_context\": [...]}. skills is an allowlist of skill names (empty = all skills); skip_context lists workspace context files to leave out (USER.md, AGENTS.md, TOOLS.md, IDENTITY.md, BOOTSTRAP.md, HEARTBEAT.md, MEMORY.md; SOUL.md is always loaded). Applies from the topic's next turn. Empty profile clears it (full prompt).",
			[]topicParam{slugParam,
				{name: "profile", desc: "Profile JSON; empty clears it",
					examples: []interface{}{`{"skills": ["email", "briefing"], "skip_context": ["AGENTS.md", "TOOLS.md"]}`}}},
			func(ctx context.Context, m TopicManager, a map[string]interface{}) (string, error) {
				return m.TopicProfile(ctx, str(a, "slug"), profileArg(a))
			}),
	}
}

func (t *TopicTool) Name() string        { return t.name }
func (t *TopicTool) Description() string { return t.desc }

func (t *TopicTool) Parameters() map[string]interface{} {
	props := map[string]interface{}{}
	required := []string{}
	for _, p := range t.params {
		props[p.name] = map[string]interface{}{"type": "string", "description": p.desc}
		if p.required {
			required = append(required, p.name)
		}
	}
	params := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		params["required"] = required
	}
	return params
}

// GetSchemaHints implements types.EnhancedSchemaProvider.
func (t *TopicTool) GetSchemaHints() map[string]schema.SchemaHints {
	hints := map[string]schema.SchemaHints{}
	for _, p := range t.params {
		if len(p.examples) > 0 {
			hints[p.name] = schema.SchemaHints{Examples: p.examples}
		}
	}
	return hints
}

func (t *TopicTool) manager() TopicManager {
	if t.services == nil || t.services.Gateway == nil {
		return nil
	}
	m, _ := t.services.Gateway.(TopicManager)
	return m
}

func (t *TopicTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	for _, p := range t.params {
		if p.required && str(args, p.name) == "" {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("%s is required", p.name)}, nil
		}
	}
	m := t.manager()
	if m == nil {
		return &types.ToolResult{Success: false, Error: "topics are not available (gateway service missing)"}, nil
	}
	out, err := t.call(ctx, m, args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("%s failed: %v", t.name, err)}, nil
	}
	return &types.ToolResult{Success: true, Content: out}, nil
}

// SelfTest implements types.SelfTester.
func (t *TopicTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()
	result := &types.SelfTestResult{Status: types.SelfTestStatusOK, TestedAt: start}
	dep := types.DependencyStatus{Name: "GatewayService", Required: true}
	if t.manager() == nil {
		dep.Status = "not_configured"
		dep.Message = "Gateway service (with topics) not available"
		result.Status = types.SelfTestStatusFailed
		result.Message = t.name + " tool is not functional: gateway service unavailable"
		result.Suggestions = []string{"Ensure gateway is properly initialized"}
	} else {
		dep.Available = true
		dep.Status = "connected"
		result.Capabilities = []string{t.name}
		result.Message = t.name + " tool is fully functional"
	}
	result.Dependencies = []types.DependencyStatus{dep}
	result.TestDuration = time.Since(start)
	return result
}
