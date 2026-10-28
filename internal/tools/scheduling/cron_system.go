package scheduling

// cron_system.go guards jobType=system (C2): the command is written into the
// user's crontab and later run by cron with no human in the loop, so it
// gets the same checks as a Bash call plus human approval.

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/approval"
	"conduit/internal/tools/approvalgate"
	"conduit/internal/tools/types"
)

// CommandPolicy vets a shell command. It returns a non-empty reason when the
// command is denied.
type CommandPolicy func(ctx context.Context, command string) string

// SetCommandPolicy wires the shell-command policy used for system jobs.
func (t *CronTool) SetCommandPolicy(p CommandPolicy) { t.commandPolicy = p }

// controlChars are never allowed in job fields that reach a line-oriented
// sink: a newline in a crontab line plants a second, unmanaged entry.
const controlChars = "\r\n\x00"

// validateJobFields rejects CR, LF and NUL in the schedule and name of every
// job and in the command of system jobs. Go-job commands are AI prompts
// stored as JSON and may span lines, but never contain NUL.
func validateJobFields(job *types.SchedulerJob) error {
	check := func(field, v, bad string) error {
		if strings.ContainsAny(v, bad) {
			return fmt.Errorf("%s must not contain control characters (CR, LF or NUL)", field)
		}
		return nil
	}
	cmdBad := "\x00"
	if job.Type == "system" {
		cmdBad = controlChars
	}
	if err := check("schedule", job.Schedule, controlChars); err != nil {
		return err
	}
	if err := check("name", job.Name, controlChars); err != nil {
		return err
	}
	return check("command", job.Command, cmdBad)
}

// scheduleSystemJob runs the command policy over a system job and hands it
// to the approval gate. The job is installed only after the bound human
// approves; non-interactive origins and a missing approver fail closed.
func (t *CronTool) scheduleSystemJob(ctx context.Context, job *types.SchedulerJob) *types.ToolResult {
	if t.commandPolicy == nil {
		return &types.ToolResult{Success: false,
			Error: "system jobs are unavailable: no shell command policy is configured"}
	}
	if reason := t.commandPolicy(ctx, job.Command); reason != "" {
		return &types.ToolResult{Success: false,
			Error: fmt.Sprintf("system job command refused by the Bash command policy: %s", reason)}
	}

	shown := approvalgate.Clip(approvalgate.RedactCommand(job.Command), approvalgate.MaxFieldRunes)
	short := approvalgate.Clip(approvalgate.RedactCommand(job.Command), 120)
	name := job.Name
	if name == "" {
		name = "(unnamed)"
	}
	op := approvalgate.Operation{
		Kind:  "cron.system_schedule",
		Title: "Install a system crontab job",
		Fields: []approval.Field{
			{Name: "Name", Value: approvalgate.Clip(name, 200)},
			{Name: "Schedule", Value: job.Schedule},
			{Name: "Command", Value: shown},
			{Name: "Runs", Value: "as the gateway's OS user, from cron, with no further checks"},
		},
		Params: map[string]string{
			"id":       job.ID,
			"name":     job.Name,
			"schedule": job.Schedule,
			"command":  job.Command,
			"oneshot":  fmt.Sprint(job.OneShot),
		},
		Audit: map[string]string{
			"job_id":   job.ID,
			"schedule": job.Schedule,
			"command":  approvalgate.Clip(shown, 200),
		},
		Summary: fmt.Sprintf("the system cron job %q (%s)", short, job.Schedule),
		Data:    map[string]interface{}{"type": "system", "requires_approval": true},
	}
	frozen := *job
	frozen.Skills = append([]string(nil), job.Skills...)
	return approvalgate.Request(ctx, t.services.Approvals, op, func(context.Context) (*types.ToolResult, error) {
		j := frozen
		if err := t.services.Gateway.ScheduleJob(&j); err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to schedule job: %v", err)}, nil
		}
		return &types.ToolResult{Success: true, Content: fmt.Sprintf("System job %s installed (%s).", j.ID, j.Schedule)}, nil
	})
}
