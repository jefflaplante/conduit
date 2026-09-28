//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/approvalgate"
	"conduit/internal/tools/types"
)

// approvedBy is recorded in the SSH audit log for commands that ran after a
// human "YES <code>" reply (conduit-w3l7).
const approvedBy = "human-approval"

// effectiveSecurityConfig fills in RequireApproval when the config omits it:
// an absent list means the secure default (["dangerous", "blocked"]), not
// "no approvals". An explicit [] still disables gating (conduit-w3l7).
func effectiveSecurityConfig(sec config.SSHSecurityConfig) config.SSHSecurityConfig {
	if sec.RequireApproval == nil {
		sec.RequireApproval = config.DefaultRemoteSSHConfig().Security.RequireApproval
	}
	return sec
}

func (t *SSHTool) approver() approval.Requester {
	if t.services == nil {
		return nil
	}
	return t.services.Approvals
}

// hostLabel renders "name (user@hostname:port)" for the approval prompt.
func (t *SSHTool) hostLabel(name string) string {
	h := t.config.GetHostByName(name)
	if h == nil {
		return name
	}
	return fmt.Sprintf("%s (%s@%s:%d)", name, h.GetUser(t.config.Defaults), h.Hostname, h.GetPort(t.config.Defaults))
}

func riskText(c *ClassificationResult) string {
	s := fmt.Sprintf("%s tier", c.Tier)
	if c.Reason != "" {
		s += " (" + c.Reason + ")"
	}
	if len(c.Warnings) > 0 {
		s += "; " + strings.Join(c.Warnings, "; ")
	}
	return s
}

// shownCommand is the redacted, length-bounded command shown to the human.
func shownCommand(cmd string) string {
	return approvalgate.Clip(approvalgate.RedactCommand(cmd), approvalgate.MaxFieldRunes)
}

// summaryCommand is the short redacted command used in titles/model text.
func summaryCommand(cmd string) string {
	return approvalgate.Clip(approvalgate.RedactCommand(cmd), 120)
}

func classificationData(c *ClassificationResult) map[string]interface{} {
	return map[string]interface{}{
		"tier":              string(c.Tier),
		"reason":            c.Reason,
		"base_cmd":          c.BaseCommand,
		"requires_approval": true,
		"warnings":          c.Warnings,
	}
}

// gate hands a frozen SSH operation to the approval manager. run executes
// later, exactly once, only if the bound human approves; non-interactive
// turns and a missing approver fail closed (conduit-w3l7).
func (t *SSHTool) gate(ctx context.Context, op approvalgate.Operation, run approvalgate.Run) (*types.ToolResult, error) {
	return approvalgate.Request(ctx, t.approver(), op, run), nil
}

func (t *SSHTool) execOperation(host, command string, timeout int, c *ClassificationResult) approvalgate.Operation {
	shown := shownCommand(command)
	return approvalgate.Operation{
		Kind:  "ssh.exec",
		Title: fmt.Sprintf("Run SSH command on %s", t.hostLabel(host)),
		Fields: []approval.Field{
			{Name: "Host", Value: t.hostLabel(host)},
			{Name: "Command", Value: shown},
			{Name: "Timeout", Value: strconv.Itoa(timeout) + "s"},
			{Name: "Risk", Value: riskText(c)},
		},
		Params: map[string]string{
			"host":    host,
			"command": command,
			"timeout": strconv.Itoa(timeout),
		},
		Audit: map[string]string{
			"host":     host,
			"base_cmd": c.BaseCommand,
			"tier":     string(c.Tier),
			"command":  approvalgate.Clip(shown, 200),
		},
		Summary: fmt.Sprintf("the SSH command %q on %s", summaryCommand(command), host),
		Data:    classificationData(c),
	}
}

func (t *SSHTool) groupOperation(group string, hosts []string, command string, timeout, maxParallel int, c *ClassificationResult) approvalgate.Operation {
	shown := shownCommand(command)
	labels := make([]string, len(hosts))
	for i, h := range hosts {
		labels[i] = t.hostLabel(h)
	}
	data := classificationData(c)
	data["group"] = group
	return approvalgate.Operation{
		Kind:  "ssh.exec_group",
		Title: fmt.Sprintf("Run SSH command on %d host(s) in group %s", len(hosts), group),
		Fields: []approval.Field{
			{Name: "Group", Value: group},
			{Name: "Hosts", Value: approvalgate.Clip(strings.Join(labels, "\n"), approvalgate.MaxFieldRunes)},
			{Name: "Command", Value: shown},
			{Name: "Timeout", Value: strconv.Itoa(timeout) + "s"},
			{Name: "Max parallel", Value: strconv.Itoa(maxParallel)},
			{Name: "Risk", Value: riskText(c)},
		},
		Params: map[string]string{
			"group":        group,
			"hosts":        strings.Join(hosts, "\x00"),
			"command":      command,
			"timeout":      strconv.Itoa(timeout),
			"max_parallel": strconv.Itoa(maxParallel),
		},
		Audit: map[string]string{
			"group":      group,
			"host_count": strconv.Itoa(len(hosts)),
			"base_cmd":   c.BaseCommand,
			"tier":       string(c.Tier),
			"command":    approvalgate.Clip(shown, 200),
		},
		Summary: fmt.Sprintf("the SSH command %q on %d host(s) in group %s", summaryCommand(command), len(hosts), group),
		Data:    data,
	}
}

func (t *SSHTool) sessionOperation(sessionID, host, command string, timeout int, c *ClassificationResult) approvalgate.Operation {
	shown := shownCommand(command)
	data := classificationData(c)
	data["session_id"] = sessionID
	return approvalgate.Operation{
		Kind:  "ssh.session_send",
		Title: fmt.Sprintf("Run SSH command in session %s on %s", sessionID, t.hostLabel(host)),
		Fields: []approval.Field{
			{Name: "Host", Value: t.hostLabel(host)},
			{Name: "Session", Value: sessionID},
			{Name: "Command", Value: shown},
			{Name: "Timeout", Value: strconv.Itoa(timeout) + "s"},
			{Name: "Risk", Value: riskText(c)},
		},
		Params: map[string]string{
			"session_id": sessionID,
			"host":       host,
			"command":    command,
			"timeout":    strconv.Itoa(timeout),
		},
		Audit: map[string]string{
			"host":       host,
			"session_id": sessionID,
			"base_cmd":   c.BaseCommand,
			"tier":       string(c.Tier),
			"command":    approvalgate.Clip(shown, 200),
		},
		Summary: fmt.Sprintf("the SSH command %q in session %s on %s", summaryCommand(command), sessionID, host),
		Data:    data,
	}
}

func (t *SSHTool) uploadOperation(host, localPath, resolvedLocal, remotePath string, size int64, c *ClassificationResult) approvalgate.Operation {
	return approvalgate.Operation{
		Kind:  "ssh.scp_upload",
		Title: fmt.Sprintf("Upload a local file to %s:%s via SCP", host, remotePath),
		Fields: []approval.Field{
			{Name: "Host", Value: t.hostLabel(host)},
			{Name: "Local file", Value: fmt.Sprintf("%s (%d bytes)", resolvedLocal, size)},
			{Name: "Remote path", Value: remotePath},
			{Name: "Risk", Value: riskText(c)},
		},
		Params: map[string]string{
			"host":        host,
			"local_path":  resolvedLocal,
			"remote_path": remotePath,
		},
		Audit: map[string]string{
			"host":        host,
			"local_path":  localPath,
			"remote_path": remotePath,
			"size":        strconv.FormatInt(size, 10),
			"tier":        string(c.Tier),
		},
		Summary: fmt.Sprintf("the SCP upload of %s to %s:%s", localPath, host, remotePath),
		Data: map[string]interface{}{
			"tier":              string(c.Tier),
			"requires_approval": true,
		},
	}
}
