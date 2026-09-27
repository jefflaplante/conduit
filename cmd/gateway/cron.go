package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"conduit/internal/scheduler"

	"github.com/spf13/cobra"
)

// CronRootCmd returns the "cron" command group (scheduler job maintenance).
func CronRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Scheduler job maintenance (cron_jobs.json)",
	}
	cmd.AddCommand(cronMigrateTZCmd())
	return cmd
}

// cronMigrateTZCmd rewrites Go cron jobs written in one zone to carry a
// CRON_TZ prefix for another, keeping today's wall-clock times.
// conduit-31jg.60
func cronMigrateTZCmd() *cobra.Command {
	var (
		fromName, toName, file, nowStr string
		dryRun, apply                  bool
	)
	cmd := &cobra.Command{
		Use:   "migrate-tz",
		Short: "Rewrite Go cron jobs to CRON_TZ=<zone> wall-clock expressions",
		Long: `Rewrite each Go job in cron_jobs.json whose expression is written in --from
(the server zone, usually UTC) to the same wall-clock time in --to, prefixed
with "CRON_TZ=<to> ", so it no longer drifts when DST changes.

The UTC offset in effect now defines "today's wall-clock time" (date-pinned
jobs use the offset at their next fire). Jobs already carrying CRON_TZ/TZ,
interval jobs (@every, hour '*') and system crontab jobs are left alone.
Jobs that cannot be converted exactly are refused and left unchanged.

--dry-run (default) prints a before/after table; --apply writes a
timestamped backup (<file>.bak-<UTC stamp>) and replaces the file
atomically, changing only the schedule strings. Re-running is a no-op.`,
		Example: `  conduit cron migrate-tz --from UTC --to America/Los_Angeles --file workspace/cron_jobs.json --dry-run
  conduit cron migrate-tz --from UTC --to America/Los_Angeles --file workspace/cron_jobs.json --apply`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun && apply {
				return fmt.Errorf("--dry-run and --apply are mutually exclusive")
			}
			if file == "" || toName == "" {
				return fmt.Errorf("--file and --to are required")
			}
			from, err := time.LoadLocation(fromName)
			if err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			to, err := time.LoadLocation(toName)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}
			now := time.Now()
			if nowStr != "" {
				if now, err = time.Parse(time.RFC3339, nowStr); err != nil {
					return fmt.Errorf("--now: %w", err)
				}
			}
			opts := scheduler.TZMigrateOptions{From: from, To: to, Now: now}
			out := cmd.OutOrStdout()

			if !apply {
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				jobs, err := scheduler.PlanTZMigration(data, opts)
				if err != nil {
					return err
				}
				writeTZTable(out, jobs, opts)
				fmt.Fprintln(out, "\nDry run: nothing written. Re-run with --apply to write.")
				return nil
			}
			res, err := scheduler.ApplyTZMigration(file, opts)
			if err != nil {
				return err
			}
			writeTZTable(out, res.Jobs, opts)
			if res.Changed == 0 {
				fmt.Fprintln(out, "\nNothing to migrate; file unchanged.")
				return nil
			}
			fmt.Fprintf(out, "\nMigrated %d job(s). Backup: %s\n", res.Changed, res.BackupPath)
			fmt.Fprintln(out, "A running gateway picks this up on its next cron_jobs.json poll (<=30s).")
			return nil
		},
	}
	cmd.Flags().StringVar(&fromName, "from", "UTC", "zone the existing expressions are written in")
	cmd.Flags().StringVar(&toName, "to", "", "target zone for CRON_TZ (e.g. America/Los_Angeles)")
	cmd.Flags().StringVar(&file, "file", "", "path to cron_jobs.json")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan only (default)")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the migration (backup + atomic replace)")
	cmd.Flags().StringVar(&nowStr, "now", "", "reference time (RFC3339) instead of the current time")
	return cmd
}

// nextZoneTransition returns the first instant after t (within ~400 days)
// at which loc's UTC offset changes, or the zero time.
func nextZoneTransition(t time.Time, loc *time.Location) time.Time {
	_, off := t.In(loc).Zone()
	for h := 1; h <= 400*24; h++ {
		c := t.Add(time.Duration(h) * time.Hour)
		if _, o := c.In(loc).Zone(); o != off {
			return c
		}
	}
	return time.Time{}
}

func fmtRuns(expr string, typ scheduler.JobType, after time.Time, from, to *time.Location, n int) string {
	s, err := scheduler.ParseJobSchedule(expr, typ)
	if err != nil {
		return "unparseable"
	}
	var parts []string
	t := after.In(from)
	for i := 0; i < n; i++ {
		t = s.Next(t)
		if t.IsZero() {
			break
		}
		parts = append(parts, t.In(to).Format("Mon 2006-01-02 15:04 MST"))
	}
	if len(parts) == 0 {
		return "never"
	}
	return strings.Join(parts, "<br>")
}

func mdCell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}

// writeTZTable prints the per-job plan as a Markdown table.
func writeTZTable(w io.Writer, jobs []scheduler.TZJobResult, opts scheduler.TZMigrateOptions) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	post := nextZoneTransition(now, opts.To)
	postLabel := "after next DST change"
	if !post.IsZero() {
		y, m, d := post.In(opts.To).Date()
		post = time.Date(y, m, d+1, 0, 0, 0, 0, opts.To)
		postLabel = "from " + post.Format("2006-01-02")
	} else {
		post = now.AddDate(0, 6, 0)
	}
	var conv, skip, refuse int
	for _, j := range jobs {
		switch j.Action {
		case scheduler.TZConvert:
			conv++
		case scheduler.TZSkip:
			skip++
		case scheduler.TZRefuse:
			refuse++
		}
	}
	fmt.Fprintf(w, "Reference time: %s (%s). From %s to %s. %d convert, %d skip, %d refuse.\n\n",
		now.In(opts.To).Format("2006-01-02 15:04 MST"), now.UTC().Format(time.RFC3339), opts.From, opts.To, conv, skip, refuse)
	fmt.Fprintf(w, "| ID | Name | Type | Enabled | Action | Old expression | New expression | Old: next 2 (%[1]s) | New: next 2 (%[1]s) | New: next 2 %[2]s | Notes |\n", opts.To, postLabel)
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|")
	for _, j := range jobs {
		newExpr := "`" + j.NewSchedule + "`"
		if j.Action != scheduler.TZConvert {
			newExpr = "(unchanged)"
		}
		notes := j.Flags
		if j.Reason != "" {
			notes = append([]string{j.Reason}, notes...)
		}
		fmt.Fprintf(w, "| %s | %s | %s | %t | %s | `%s` | %s | %s | %s | %s | %s |\n",
			mdCell(j.ID), mdCell(j.Name), j.Type, j.Enabled, j.Action,
			mdCell(j.OldSchedule), mdCell(newExpr),
			fmtRuns(j.OldSchedule, j.Type, now, opts.From, opts.To, 2),
			fmtRuns(j.NewSchedule, j.Type, now, opts.From, opts.To, 2),
			fmtRuns(j.NewSchedule, j.Type, post, opts.From, opts.To, 2),
			mdCell(strings.Join(notes, "; ")))
	}
}
