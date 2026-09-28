// Package approvalgate routes risky tool operations (Kubernetes
// require_approval tiers, SSH dangerous-tier commands) through the human
// approval primitive in internal/approval (conduit-c8ct, conduit-w3l7).
//
// A gated operation never runs inside the tool call. The tool freezes the
// exact operation, registers it with approval.Requester and returns a
// "NOT RUN YET" pending result to the model. The operation runs later, once,
// only if the bound human replies "YES <code>" on the originating channel.
// Non-interactive or unknown origins (cron, heartbeat, sub-agents, MCP) and
// a missing approval channel fail closed.
package approvalgate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/redact"
	"conduit/internal/tools/types"
)

// Operation is the frozen description of one gated tool operation.
type Operation struct {
	// Kind is a stable machine label, e.g. "k8s.delete" or "ssh.exec".
	Kind string
	// Title is the one-line summary shown to the human (already redacted).
	Title string
	// Fields are shown to the human in the prompt (already redacted).
	Fields []approval.Field
	// Params are the exact, unredacted parameters the approval is bound to.
	// They are fingerprinted, never shown or logged.
	Params map[string]string
	// Audit is logged on every approval event (must be redacted).
	Audit map[string]string
	// Summary names the operation for the model, e.g.
	// "Kubernetes delete of deployments/api". It is redacted text.
	Summary string
	// Data is merged into the pending/refused result's Data for the model
	// (e.g. tier, requires_approval). Must be redacted.
	Data map[string]interface{}
	// TTL optionally overrides how long the approval stays valid (the
	// tool's configured approval timeout). Zero uses the manager default;
	// the manager caps it at approval.MaxTTL (conduit-enf0).
	TTL time.Duration
}

// Run performs the approved operation with the values frozen at request
// time. ctx is a fresh context owned by the approval manager.
type Run func(ctx context.Context) (*types.ToolResult, error)

// Request registers op for human approval and returns the tool result the
// model sees. run is invoked at most once, only after approval.
func Request(ctx context.Context, r approval.Requester, op Operation, run Run) *types.ToolResult {
	if op.Kind == "" || run == nil {
		return op.refused("internal error: gated operation is missing a kind or runner", "refused", "")
	}
	if r == nil {
		slog.Warn("gated tool operation refused: no approver configured",
			"component", "approval", "event", "approval.refused_no_approver", "kind", op.Kind)
		return op.refused(fmt.Sprintf("NOT RUN: %s requires human approval, but no approval channel is configured.", op.Summary),
			"refused_no_approver", "")
	}

	// Freeze a private copy of the parameters: the fingerprint and the
	// re-check at execution time both use this copy.
	frozen := make(map[string]string, len(op.Params))
	for k, v := range op.Params {
		frozen[k] = v
	}
	fp := approval.Fingerprint(op.Kind, frozen)

	act := approval.Action{
		Kind:        op.Kind,
		Title:       op.Title,
		Fields:      op.Fields,
		Audit:       op.Audit,
		Fingerprint: fp,
		TTL:         op.TTL,
	}

	exec := func(execCtx context.Context, t approval.Ticket) (string, error) {
		if again := approval.Fingerprint(op.Kind, frozen); again != t.Fingerprint {
			return "", errors.New("approval fingerprint mismatch; refusing to run")
		}
		res, err := run(execCtx)
		if err != nil {
			return "", err
		}
		if res == nil {
			return "", errors.New("operation returned no result")
		}
		if !res.Success {
			switch {
			case res.Error != "":
				return "", errors.New(res.Error)
			case res.Content != "":
				return "", errors.New(res.Content)
			}
			return "", errors.New("operation failed")
		}
		return res.Content, nil
	}

	ticket, err := r.Request(ctx, act, exec)
	if err != nil {
		var ni *approval.NonInteractiveError
		if errors.As(err, &ni) {
			return op.refused(fmt.Sprintf("NOT RUN: %s needs live human approval, but this turn is non-interactive (origin: %s). "+
				"A human must request it from an interactive chat (Telegram/TUI/WebSocket). Do not retry from this context.",
				op.Summary, ni.Source), "refused_noninteractive", ni.Source)
		}
		return op.refused(fmt.Sprintf("NOT RUN: %s needs human approval, which could not be requested: %v", op.Summary, err),
			"refused", "")
	}

	// The approval code is deliberately withheld from the model.
	data := op.data()
	data["approval_status"] = "pending"
	data["approval_id"] = ticket.ID
	data["expires_at"] = ticket.ExpiresAt.Format(time.RFC3339)
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("NOT RUN YET, awaiting the owner's approval. An approval prompt for %s was sent to the owner in this chat. "+
			"It will run automatically only if the owner approves within %s; otherwise nothing runs. "+
			"Do not retry or re-issue it; tell the owner you are waiting for their approval.",
			op.Summary, time.Until(ticket.ExpiresAt).Round(time.Second)),
		Data: data,
	}
}

func (op Operation) data() map[string]interface{} {
	data := make(map[string]interface{}, len(op.Data)+3)
	for k, v := range op.Data {
		data[k] = v
	}
	return data
}

func (op Operation) refused(msg, status, origin string) *types.ToolResult {
	data := op.data()
	data["approval_status"] = status
	if origin != "" {
		data["origin"] = origin
	}
	return &types.ToolResult{Success: false, Error: msg, Data: data}
}

// MaxFieldRunes bounds any single value shown in an approval prompt.
const MaxFieldRunes = 1500

// Clip shortens s for display in a prompt.
func Clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("... [%d more chars]", len(r)-max)
}

var (
	// KEY=value / --key=value (env assignments, long flags).
	assignRe = regexp.MustCompile(`(^|[\s;&|(])(-{0,2}[A-Za-z_][A-Za-z0-9_.-]*)=("[^"]*"|'[^']*'|[^\s;&|)]+)`)
	// --key value / -key value.
	flagRe = regexp.MustCompile(`(^|\s)(--?[A-Za-z][A-Za-z0-9_-]*)(\s+)("[^"]*"|'[^']*'|[^\s-][^\s;&|]*)`)
	// scheme://user:pass@host
	userinfoRe = regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`)
	// Authorization: Bearer xyz (curl -H, wget --header).
	authHeaderRe = regexp.MustCompile(`(?i)(authorization:\s*(?:bearer\s+|basic\s+|token\s+)?)[^\s"']+`)
)

func secretFlag(key string) bool {
	k := strings.TrimLeft(key, "-")
	k = strings.ReplaceAll(k, "-", "_")
	k = strings.ReplaceAll(k, ".", "_")
	return k != "" && config.IsSecretKey(k)
}

// RedactCommand masks credentials in a shell command or argument string for
// display and audit logs: values of secret-named env assignments and flags
// (PASSWORD=..., --api-key ..., --token=...), URL userinfo passwords,
// Authorization headers, and registered secrets (internal/redact). The
// command that actually runs is never modified.
func RedactCommand(s string) string {
	const mask = config.RedactedValue
	s = assignRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := assignRe.FindStringSubmatch(m)
		if !secretFlag(sub[2]) {
			return m
		}
		return sub[1] + sub[2] + "=" + mask
	})
	s = flagRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := flagRe.FindStringSubmatch(m)
		if !secretFlag(sub[2]) {
			return m
		}
		return sub[1] + sub[2] + sub[3] + mask
	})
	s = userinfoRe.ReplaceAllString(s, "${1}"+mask+"@")
	s = authHeaderRe.ReplaceAllString(s, "${1}"+mask)
	return redact.String(s)
}
