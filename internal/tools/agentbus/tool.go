//go:build with_agentbus

package agentbus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"

	"github.com/jefflaplante/agentbus/a2a"
	"github.com/jefflaplante/agentbus/client"
)

// AgentBusTool exposes the a2a/v1 agent bus to the model: discover agents,
// send events, make requests and delegate tasks to capability queues.
// Outgoing request/event envelopes pass conduit's approval gate; response
// and error replies (which only answer inbound requests) are exempt.
type AgentBusTool struct {
	services *types.ToolServices

	// cfg is resolved at construction; zero cfg = not configured.
	cfg agentConfig

	// mu guards cl (lazy connect); approvedPublishes carries one credit
	// per owner-approved (or config-exempt) gated publish so BeforePublish
	// can verify the transport boundary is never crossed unapproved.
	mu                sync.Mutex
	cl                *client.Client
	approvedPublishes atomic.Int64

	// publishFn sends one envelope; production wraps client.Client.Publish
	// (whose BeforePublish hook re-checks the gate). Tests substitute it.
	publishFn func(ctx context.Context, topic string, env *a2a.Envelope) error
}

// maxRequestWait bounds how long request/task wait for a reply; the
// approval prompt covers the send, and a wait past this is a hang.
const maxRequestWait = 5 * time.Minute

// NewAgentBusTool builds the tool from services + config. It returns
// (nil, nil) when disabled (agentbus.enabled=false), matching the
// OptionalToolFactory contract.
func NewAgentBusTool(services *types.ToolServices, cfg *agentConfig) (*AgentBusTool, error) {
	if cfg == nil {
		return nil, nil
	}
	t := &AgentBusTool{services: services, cfg: *cfg}
	t.publishFn = func(ctx context.Context, topic string, env *a2a.Envelope) error {
		cl, err := t.ensureConnected(ctx)
		if err != nil {
			return err
		}
		return cl.Publish(ctx, topic, env)
	}
	return t, nil
}

// ensureConnected lazily connects to the broker on first use, so a
// compiled-in but unused tool holds no broker connection, and a broker
// that is down fails the action (or the approved run), not the gateway.
func (t *AgentBusTool) ensureConnected(ctx context.Context) (*client.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cl != nil {
		return t.cl, nil
	}
	cl, err := client.Connect(ctx, client.Options{
		BrokerURL:     t.cfg.brokerURL,
		AgentID:       t.cfg.agentID,
		Token:         t.cfg.token,
		Inbox:         t.inboxHandler, // phase 1: answer inbound requests with a clear error (no session runner yet)
		Description:   "Conduit gateway",
		BeforePublish: t.beforePublish(),
	})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", t.cfg.brokerURL, err)
	}
	t.cl = cl
	return cl, nil
}

// inboxHandler is the phase-1 inbox: conduit is on the bus but has no
// run-to-completion session runner yet, so every inbound request is
// answered with an error envelope (fail fast for the sender instead of a
// TTL timeout, per the integration plan) and direct events are dropped.
func (t *AgentBusTool) inboxHandler(ctx context.Context, env *a2a.Envelope) (any, error) {
	return nil, fmt.Errorf("conduit (%s) received your %s but has no inbound session runner yet; the request was not processed",
		t.cfg.agentID, env.Type)
}

func (t *AgentBusTool) Name() string { return "AgentBus" }

func (t *AgentBusTool) Description() string {
	return `Talk to other agents on the a2a/v1 agent bus (MQTT-based agent-to-agent messaging).

Actions:
- agents: List agents on the bus and what capabilities they serve (START HERE)
- send: One-way event to another agent's inbox (needs owner approval)
- request: Ask another agent and wait for its reply (needs owner approval)
- task: Hand work to whoever serves a capability and wait (needs owner approval)

Notes:
- Use action=agents first to see who is on the bus and what they can do.
- request/task wait for the other agent's reply; timeout_ms bounds the wait.
- Outgoing messages ask the owner for approval first (like MQTT publish); refused in non-interactive turns (cron, heartbeat, sub-agents).
- Replies to messages sent TO conduit are not gated: answering is not a new action.`
}

func (t *AgentBusTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"agents", "send", "request", "task"},
				"description": "agents (who is on the bus—START HERE), send (one-way event), request (ask and wait), task (delegate to a capability queue and wait)",
			},
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Destination agent ID for send/request (from action=agents)",
			},
			"capability": map[string]interface{}{
				"type":        "string",
				"description": "Capability name for task (from action=agents)",
			},
			"event": map[string]interface{}{
				"type":        "string",
				"description": "Event name for send (e.g. 'status-ping'); defaults to none",
			},
			"body": map[string]interface{}{
				"type":        "object",
				"description": "Message payload (arbitrary JSON object)",
			},
			"priority": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"high", "normal", "low", "info"},
				"description": "How urgently the receiver should act (default normal)",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "How long request/task waits for a reply (default 30000, max 300000)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *AgentBusTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"agents": {
			Description: "List agents on the bus with their capabilities",
			Returns:     "array of {agent, status, description, capabilities}",
		},
		"send": {
			Description:    "One-way event to an agent's inbox. Approval-gated",
			RequiredParams: []string{"to"},
			OptionalParams: []string{"body", "event", "priority"},
			Returns:        "topic, envelope_id",
		},
		"request": {
			Description:    "Ask an agent and wait for its reply. Approval-gated",
			RequiredParams: []string{"to"},
			OptionalParams: []string{"body", "priority", "timeout_ms"},
			Returns:        "from, type, body of the reply",
		},
		"task": {
			Description:    "Delegate work to a capability's work queue and wait. Approval-gated",
			RequiredParams: []string{"capability"},
			OptionalParams: []string{"body", "priority", "timeout_ms"},
			Returns:        "from, type, body of the worker's reply",
		},
	}
}

func (t *AgentBusTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := toolargs.GetString(args, "action", "")
	switch action {
	case "agents":
		return t.agents(ctx)
	case "send":
		return t.send(ctx, args)
	case "request":
		return t.request(ctx, args)
	case "task":
		return t.task(ctx, args)
	default:
		return types.NewErrorResult("invalid_action",
			fmt.Sprintf("Unknown action: %s", action)).
			WithParameter("action", action).
			WithAvailableValues([]string{"agents", "send", "request", "task"}), nil
	}
}

// agents returns the presence cards the bus tracks. The client caches
// cards retained on its presence subscription, so this is read-only: no
// publish, no approval.
func (t *AgentBusTool) agents(ctx context.Context) (*types.ToolResult, error) {
	cl, err := t.ensureConnected(ctx)
	if err != nil {
		return types.NewErrorResult("connect_failed", err.Error()), nil
	}
	cards := cl.Agents()
	if len(cards) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No agents on the bus (presence may not have arrived yet; wait a moment and retry).",
			Data:    map[string]interface{}{"agents": []interface{}{}},
		}, nil
	}
	out := make([]map[string]interface{}, 0, len(cards))
	var b strings.Builder
	b.WriteString("Agents on the bus:\n")
	for _, p := range cards {
		caps := strings.Join(p.Capabilities, ", ")
		if caps == "" {
			caps = "(none)"
		}
		b.WriteString(fmt.Sprintf("- %s (%s): %s [capabilities: %s]\n", p.Agent, p.Status, p.Description, caps))
		out = append(out, map[string]interface{}{
			"agent":        p.Agent,
			"status":       string(p.Status),
			"description":  p.Description,
			"capabilities": p.Capabilities,
		})
	}
	return &types.ToolResult{
		Success: true,
		Content: strings.TrimRight(b.String(), "\n"),
		Data:    map[string]interface{}{"agents": out},
	}, nil
}

// send publishes a one-way event envelope to an agent's inbox, gated.
func (t *AgentBusTool) send(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if refused := destAgent(args); refused != nil {
		return refused, nil
	}
	to := toolargs.GetString(args, "to", "")
	body := messageBody(args)
	env, berr := a2a.New(a2a.TypeEvent, t.cfg.agentID, body)
	if berr != nil {
		return types.NewErrorResult("envelope_error", berr.Error()), nil
	}
	env.To = to
	if ev := toolargs.GetString(args, "event", ""); ev != "" {
		env.Event = ev
	}
	if pr := toolargs.GetString(args, "priority", ""); pr != "" {
		env.Priority = a2a.Priority(pr)
	}
	topic := a2a.InboxTopic(to)
	params := map[string]string{"action": "send", "to": to}
	return t.gatePublish(ctx, topic, env.Type, body, params, func(ctx context.Context) (*types.ToolResult, error) {
		release := t.approveOnePublish()
		defer release()
		perr := t.publishFn(ctx, topic, env)
		if perr != nil {
			return types.NewErrorResult("publish_failed", perr.Error()).WithParameter("topic", topic), nil
		}
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Sent event to %s (topic %s, id %s)", to, topic, env.ID),
			Data:    map[string]interface{}{"topic": topic, "envelope_id": env.ID},
		}, nil
	}), nil
}

// request publishes a request envelope to an agent's inbox and waits for
// the correlated response. Approval gates the send; the reply wait is
// read-only and needs no gate.
func (t *AgentBusTool) request(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if refused := destAgent(args); refused != nil {
		return refused, nil
	}
	return t.gatedRoundTrip(ctx, args, func(ctx context.Context, cl *client.Client) (*a2a.Envelope, error) {
		body := messageBody(args)
		opts, cancel, e := requestOptions(args)
		if e != nil {
			return nil, e
		}
		defer cancel()
		return cl.Request(ctx, toolargs.GetString(args, "to", ""), body, opts...)
	})
}

// task publishes a request to the capability work queue and waits.
func (t *AgentBusTool) task(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	capability := toolargs.GetString(args, "capability", "")
	if capability == "" {
		return types.NewErrorResult("missing_parameter", "task requires 'capability'").WithParameter("capability", capability), nil
	}
	if !a2a.ValidName(capability) {
		return types.NewErrorResult("invalid_capability",
			fmt.Sprintf("%q is not a valid capability: 1-64 chars of a-z, 0-9, '.', '_' or '-', not starting with punctuation", capability)).
			WithParameter("capability", capability), nil
	}
	return t.gatedRoundTrip(ctx, args, func(ctx context.Context, cl *client.Client) (*a2a.Envelope, error) {
		body := messageBody(args)
		opts, cancel, e := requestOptions(args)
		if e != nil {
			return nil, e
		}
		defer cancel()
		return cl.RequestTask(ctx, capability, body, opts...)
	})
}

// gatedRoundTrip is the shared request/task spine: gate the outgoing
// request (freezing destination, body preview and timeout), then on
// approval perform the client round trip and render the reply.
func (t *AgentBusTool) gatedRoundTrip(ctx context.Context, args map[string]interface{}, call func(ctx context.Context, cl *client.Client) (*a2a.Envelope, error)) (*types.ToolResult, error) {
	dest := toolargs.GetString(args, "to", "")
	if dest == "" {
		dest = "capability:" + toolargs.GetString(args, "capability", "")
	}
	timeout := waitTimeout(args)
	topic := a2a.InboxTopic(dest)
	if cap := toolargs.GetString(args, "capability", ""); cap != "" {
		topic = a2a.TaskTopic(cap)
	}
	params := map[string]string{"action": toolargs.GetString(args, "action", ""), "to": dest, "timeout_ms": fmt.Sprintf("%d", timeout.Milliseconds())}
	return t.gatePublish(ctx, topic, a2a.TypeRequest, messageBody(args), params, func(ctx context.Context) (*types.ToolResult, error) {
		cl, err := t.ensureConnected(ctx)
		if err != nil {
			return types.NewErrorResult("connect_failed", err.Error()), nil
		}
		release := t.approveOnePublish()
		defer release()
		reply, err := call(ctx, cl)
		if err != nil {
			return types.NewErrorResult("request_failed", err.Error()).WithParameter("destination", dest), nil
		}
		return replyResult(reply), nil
	}), nil
}

// replyResult renders a response (or broker/remote error) envelope for the
// model. RemoteError replies carry the other agent's error code/reason.
func replyResult(reply *a2a.Envelope) *types.ToolResult {
	if reply == nil {
		return types.NewErrorResult("empty_reply", "the request completed without a reply envelope")
	}
	var body any
	if berr := reply.DecodeBody(&body); berr != nil {
		body = string(reply.Body)
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Reply from %s (type %s):\n%s", reply.From, reply.Type, bodyString(body)),
		Data: map[string]interface{}{
			"from":        reply.From,
			"type":        string(reply.Type),
			"body":        body,
			"reply_id":    reply.ID,
			"correlation": reply.CorrelationID,
		},
	}
}

// destAgent validates the 'to' parameter for send/request and returns the
// refusal result, or nil when to is valid.
func destAgent(args map[string]interface{}) *types.ToolResult {
	to := toolargs.GetString(args, "to", "")
	if to == "" {
		return types.NewErrorResult("missing_parameter", "requires 'to' (destination agent id; see action=agents)").WithParameter("to", to)
	}
	if !a2a.ValidName(to) {
		return types.NewErrorResult("invalid_agent_id",
			fmt.Sprintf("%q is not a valid agent id: 1-64 chars of a-z, 0-9, '.', '_' or '-', not starting with punctuation", to)).
			WithParameter("to", to)
	}
	return nil
}

// messageBody extracts the optional body (default empty object).
func messageBody(args map[string]interface{}) any {
	if b, ok := args["body"]; ok && b != nil {
		return b
	}
	return map[string]interface{}{}
}

// waitTimeout resolves the reply wait from timeout_ms (default 30s,
// capped at maxRequestWait), further bounded by ctx's deadline if any.
func waitTimeout(args map[string]interface{}) time.Duration {
	d := 30 * time.Second
	if ms := toolargs.GetInt(args, "timeout_ms", 0); ms > 0 {
		d = time.Duration(ms) * time.Millisecond
	}
	if d > maxRequestWait {
		d = maxRequestWait
	}
	return d
}

// requestOptions builds client.MsgOptions (priority, event name) for the
// client round trip. The returned cancel releases per-call state; today it
// is a no-op kept so requestOptions can grow context-scoped options.
func requestOptions(args map[string]interface{}) ([]client.MsgOption, func(), error) {
	var opts []client.MsgOption
	if pr := toolargs.GetString(args, "priority", ""); pr != "" {
		opts = append(opts, client.WithPriority(a2a.Priority(pr)))
	}
	if ev := toolargs.GetString(args, "event", ""); ev != "" {
		opts = append(opts, client.WithEvent(ev))
	}
	return opts, func() {}, nil
}
