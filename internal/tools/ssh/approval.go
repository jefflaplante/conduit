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
//
// The same absent-means-default rule applies to the hard blocks, so a nil
// blocked_patterns or allowed_commands.blocked never silently drops every
// block (mkfs, dd, /etc/shadow, curl|sh). Explicit [] still means none
// (conduit-enf0). A loaded remote_ssh block is already merged onto
// DefaultRemoteSSHConfig (conduit-6wjo); this still covers an explicit
// null and configs built in code.
func effectiveSecurityConfig(sec config.SSHSecurityConfig) config.SSHSecurityConfig {
	def := config.DefaultRemoteSSHConfig().Security
	if sec.RequireApproval == nil {
		sec.RequireApproval = def.RequireApproval
	}
	if sec.BlockedPatterns == nil {
		sec.BlockedPatterns = def.BlockedPatterns
	}
	if sec.AllowedCommands.Blocked == nil {
		sec.AllowedCommands.Blocked = def.AllowedCommands.Blocked
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
// turns and a missing approver fail closed (conduit-w3l7). The approval
// stays valid for security.approval_timeout (0 = the gateway default, capped
// at approval.MaxTTL; conduit-enf0).
func (t *SSHTool) gate(ctx context.Context, op approvalgate.Operation, run approvalgate.Run) (*types.ToolResult, error) {
	op.TTL = t.config.Security.ApprovalTimeout.Duration()
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
		Data:    classificationData(c),
	}
}

func (t *SSHTool) downloadOperation(host, remotePath, localPath, resolvedLocal string, c *ClassificationResult) approvalgate.Operation {
	return approvalgate.Operation{
		Kind:  "ssh.scp_download",
		Title: fmt.Sprintf("Download %s:%s via SCP", host, remotePath),
		Fields: []approval.Field{
			{Name: "Host", Value: t.hostLabel(host)},
			{Name: "Remote path", Value: remotePath},
			{Name: "Local file", Value: resolvedLocal},
			{Name: "Risk", Value: riskText(c)},
		},
		Params: map[string]string{
			"host":        host,
			"remote_path": remotePath,
			"local_path":  resolvedLocal,
		},
		Audit: map[string]string{
			"host":        host,
			"remote_path": remotePath,
			"local_path":  localPath,
			"tier":        string(c.Tier),
		},
		Summary: fmt.Sprintf("the SCP download of %s:%s to %s", host, remotePath, localPath),
		Data:    classificationData(c),
	}
}

func (t *SSHTool) tunnelOperation(host string, localPort int, remoteHost string, remotePort int, c *ClassificationResult) approvalgate.Operation {
	local := "auto-assigned"
	if localPort != 0 {
		local = strconv.Itoa(localPort)
	}
	target := fmt.Sprintf("%s:%d", remoteHost, remotePort)
	return approvalgate.Operation{
		Kind:  "ssh.tunnel_create",
		Title: fmt.Sprintf("Open an SSH tunnel to %s via %s", target, t.hostLabel(host)),
		Fields: []approval.Field{
			{Name: "Via host", Value: t.hostLabel(host)},
			{Name: "Forward to", Value: target},
			{Name: "Local port", Value: "127.0.0.1:" + local},
			{Name: "Risk", Value: riskText(c)},
		},
		Params: map[string]string{
			"host":        host,
			"local_port":  strconv.Itoa(localPort),
			"remote_host": remoteHost,
			"remote_port": strconv.Itoa(remotePort),
		},
		Audit: map[string]string{
			"host":   host,
			"target": target,
			"tier":   string(c.Tier),
		},
		Summary: fmt.Sprintf("the SSH tunnel to %s via %s", target, host),
		Data:    classificationData(c),
	}
}
