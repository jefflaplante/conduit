package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"conduit/internal/approval"
)

// Approver is the slice of approval.Manager the executor needs
// (conduit-31jg.43). Satisfied by *approval.Manager.
type Approver interface {
	Request(ctx context.Context, action approval.Action, exec approval.ExecuteFunc) (*approval.Ticket, error)
}

// ApprovalKindOwnerEmailSend labels owner-account email sends.
const ApprovalKindOwnerEmailSend = "email.send_as_owner"

// bodyPreviewRunes bounds the body excerpt shown to the human in the prompt.
const bodyPreviewRunes = 3000

// SetApprover wires the human approval gate. Nil disables owner sends
// entirely (fail closed).
func (e *Executor) SetApprover(a Approver) {
	e.approverMu.Lock()
	e.approver = a
	e.approverMu.Unlock()
}

func (e *Executor) getApprover() Approver {
	e.approverMu.RLock()
	defer e.approverMu.RUnlock()
	return e.approver
}

// SetApprover wires the human approval gate into the skill executor.
func (m *Manager) SetApprover(a Approver) {
	if m == nil || m.executor == nil {
		return
	}
	m.executor.SetApprover(a)
}

// isEmailSend reports whether this call sends mail via the gog/email skill.
// Reads/searches/lists are intentionally not gated.
func isEmailSend(skill Skill, action string) bool {
	return (skill.Name == "email" || skill.Name == "gog") && normalizeAction(action) == "send"
}

// isOwnerEmailSend reports whether this call would send mail as the owner.
func (e *Executor) isOwnerEmailSend(skill Skill, action string, args map[string]interface{}) bool {
	return isEmailSend(skill, action) && e.gog.sendUsesOwner(args)
}

// auditSenderGate records one email sender-gate decision (conduit-1nfq).
// decision is "allow", "approval_required" or "deny"; identity is "agent" or
// "owner". Recipient is loggable; subject and body never are here (the
// approval audit carries the subject for owner sends).
func (e *Executor) auditSenderGate(ctx context.Context, skill Skill, decision, identity, reason string, args map[string]interface{}) {
	logger := e.gateLog
	if logger == nil {
		logger = slog.Default()
	}
	to, _ := args["to"].(string)
	o, _ := approval.OriginFrom(ctx)
	source := o.Source
	if source == "" {
		source = "unknown"
	}
	logger.Info("email sender gate",
		"component", "approval", "event", "email.sender_gate",
		"path", "skill:"+skill.Name, "decision", decision, "identity", identity,
		"reason", reason, "to", to, "origin", source,
		"session_key", o.SessionKey, "channel_id", o.ChannelID, "user_id", o.UserID)
}

// gateOwnerSend diverts owner-account email sends to human approval
// (conduit-31jg.43). gated=false means the call is not an owner send and
// should run normally. When gated, the returned result is what the model
// sees; the send itself happens only if and when the human approves.
func (e *Executor) gateOwnerSend(ctx context.Context, skill Skill, action string, args map[string]interface{}) (*ExecutionResult, bool) {
	if !isEmailSend(skill, action) {
		return nil, false
	}
	if !e.gog.sendUsesOwner(args) {
		e.auditSenderGate(ctx, skill, "allow", "agent", "agent_account", args)
		return nil, false
	}
	// Validate before prompting a human about a request that cannot run.
	for _, key := range []string{"account", "inbox"} {
		if err := e.gog.validateAccountArg(args, key); err != nil {
			e.auditSenderGate(ctx, skill, "deny", "owner", "invalid_account", args)
			return &ExecutionResult{Success: false, Error: fmt.Sprintf("invalid arguments for action %s: %v", action, err)}, true
		}
	}
	to, _ := args["to"].(string)
	if to == "" {
		e.auditSenderGate(ctx, skill, "deny", "owner", "missing_to", args)
		return &ExecutionResult{Success: false, Error: "send requires a non-empty 'to'"}, true
	}

	// Freeze the exact args: the approved send runs this copy and nothing
	// else, and the fingerprint is computed over it.
	frozen, fp, err := freezeArgs(skill.Name, action, args)
	if err != nil {
		e.auditSenderGate(ctx, skill, "deny", "owner", "unencodable_args", args)
		return &ExecutionResult{Success: false, Error: fmt.Sprintf("owner send refused: %v", err)}, true
	}

	approver := e.getApprover()
	if approver == nil {
		e.auditSenderGate(ctx, skill, "deny", "owner", "no_approver", args)
		slog.Warn("owner email send refused: no approver configured",
			"component", "approval", "event", "approval.refused_no_approver", "kind", ApprovalKindOwnerEmailSend)
		return &ExecutionResult{Success: false, Error: "NOT SENT: sending as the owner's account requires human approval, but no approval channel is configured."}, true
	}

	subject, _ := frozen["subject"].(string)
	body, _ := frozen["body"].(string)
	identity, _ := frozen["account"].(string)
	if identity == "" || e.gog.isAgentAlias(identity) {
		identity, _ = frozen["from"].(string)
	}

	act := approval.Action{
		Kind:  ApprovalKindOwnerEmailSend,
		Title: fmt.Sprintf("Send email AS THE OWNER (%s) to %s", identity, to),
		Fields: []approval.Field{
			{Name: "From (owner account)", Value: identity},
			{Name: "To", Value: to},
			{Name: "Subject", Value: subject},
			{Name: "Body", Value: preview(body, bodyPreviewRunes)},
		},
		// Recipient and subject are loggable; the body never is.
		Audit: map[string]string{
			"to":       to,
			"subject":  subject,
			"account":  identity,
			"body_len": strconv.Itoa(len(body)),
			"skill":    skill.Name,
		},
		Fingerprint: fp,
	}

	exec := func(execCtx context.Context, t approval.Ticket) (string, error) {
		// Re-verify binding: only the frozen params that were approved run.
		_, again, err := freezeArgs(skill.Name, action, frozen)
		if err != nil {
			return "", err
		}
		if again != t.Fingerprint {
			return "", errors.New("approval fingerprint mismatch; refusing to send")
		}
		run := e.execute
		if e.runApproved != nil {
			run = e.runApproved
		}
		res, err := run(execCtx, skill, action, frozen)
		if err != nil {
			return "", err
		}
		if !res.Success {
			if res.Error != "" {
				return "", errors.New(res.Error)
			}
			return "", errors.New("send failed")
		}
		return fmt.Sprintf("Email sent as %s to %s.", identity, to), nil
	}

	ticket, err := approver.Request(ctx, act, exec)
	if err != nil {
		var ni *approval.NonInteractiveError
		if errors.As(err, &ni) {
			e.auditSenderGate(ctx, skill, "deny", "owner", "non_interactive", args)
			return &ExecutionResult{
				Success: false,
				Error: fmt.Sprintf("NOT SENT: sending as the owner's account needs live human approval, but this turn is non-interactive (origin: %s). "+
					"A human must request this send from an interactive chat (Telegram/TUI/WebSocket). Do not retry from this context.", ni.Source),
				Data: map[string]interface{}{"approval_status": "refused_noninteractive", "origin": ni.Source},
			}, true
		}
		e.auditSenderGate(ctx, skill, "deny", "owner", "approval_unavailable", args)
		return &ExecutionResult{
			Success: false,
			Error:   fmt.Sprintf("NOT SENT: sending as the owner's account needs human approval, which could not be requested: %v", err),
			Data:    map[string]interface{}{"approval_status": "refused"},
		}, true
	}

	e.auditSenderGate(ctx, skill, "approval_required", "owner", "owner_account", args)
	// conduit-31jg.43: the code is deliberately withheld from the model.
	return &ExecutionResult{
		Success: true,
		Output: fmt.Sprintf("NOT SENT YET — awaiting the owner's approval. An approval prompt was sent to the owner in this chat. "+
			"The email to %s will be sent automatically only if the owner approves within %s; otherwise nothing is sent. "+
			"Do not retry or resend; tell the owner you are waiting for their approval.",
			to, time.Until(ticket.ExpiresAt).Round(time.Second)),
		Data: map[string]interface{}{
			"approval_status": "pending",
			"approval_id":     ticket.ID,
			"expires_at":      ticket.ExpiresAt.Format(time.RFC3339),
		},
	}, true
}

// freezeArgs deep-copies args via JSON and returns the copy plus the
// approval fingerprint over (skill, action, canonical args JSON).
func freezeArgs(skillName, action string, args map[string]interface{}) (map[string]interface{}, string, error) {
	raw, err := json.Marshal(args) // map keys are sorted => canonical
	if err != nil {
		return nil, "", fmt.Errorf("cannot encode args: %w", err)
	}
	var frozen map[string]interface{}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return nil, "", fmt.Errorf("cannot decode args: %w", err)
	}
	fp := approval.Fingerprint(ApprovalKindOwnerEmailSend, map[string]string{
		"skill":  skillName,
		"action": normalizeAction(action),
		"args":   string(raw),
	})
	return frozen, fp, nil
}

func preview(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("... [%d more chars]", len(r)-max)
}
