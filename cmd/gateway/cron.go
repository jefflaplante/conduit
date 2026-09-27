package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
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
		// conduit-31jg.74: system (crontab) jobs.
		includeSystem, updateCrontab, installMissing bool
		crontabFile                                  string
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

--include-system also converts system (crontab) jobs. This host's cron
daemon (Debian vixie cron) ignores CRON_TZ, so each converted system job is
written to the crontab as one line firing at every UTC hour its wall-clock
hour can map to, guarded by a shell check of the hour in --to (see
internal/scheduler/crontab_tz.go). --from must be the cron daemon's zone.
The dry run reads the crontab with 'crontab -l' (or --crontab-file) and
prints the line-by-line change; --apply then requires --update-crontab,
backs the crontab up to <file>.crontab.bak-<stamp> and installs the new one
with 'crontab -'. Converted jobs with no managed crontab line are reported
and only added with --install-missing.

--dry-run (default) prints a before/after table; --apply writes a
timestamped backup (<file>.bak-<UTC stamp>) and replaces the file
atomically, changing only the schedule strings. Re-running is a no-op.`,
		Example: `  conduit cron migrate-tz --from UTC --to America/Los_Angeles --file workspace/cron_jobs.json --dry-run
  conduit cron migrate-tz --from UTC --to America/Los_Angeles --file workspace/cron_jobs.json --apply
  conduit cron migrate-tz --from UTC --to America/Los_Angeles --file workspace/cron_jobs.json --include-system --dry-run
  conduit cron migrate-tz --from UTC --to America/Los_Angeles --file workspace/cron_jobs.json --include-system --apply --update-crontab`,
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
			opts := scheduler.TZMigrateOptions{From: from, To: to, Now: now, IncludeSystem: includeSystem}
			out := cmd.OutOrStdout()
			if (updateCrontab || installMissing || crontabFile != "") && !includeSystem {
				return fmt.Errorf("--update-crontab, --install-missing and --crontab-file require --include-system")
			}
			if apply && crontabFile != "" {
				return fmt.Errorf("--crontab-file is for --dry-run; --apply always reads the live crontab")
			}

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
				if includeSystem {
					cur, src, err := loadCrontab(crontabFile)
					if err != nil {
						return err
					}
					_, changes, err := scheduler.PlanCrontabTZUpdate(cur, jobs, from, now, installMissing)
					if err != nil {
						return err
					}
					writeCrontabChanges(out, changes, src)
				}
				fmt.Fprintln(out, "\nDry run: nothing written. Re-run with --apply to write.")
				return nil
			}

			// conduit-31jg.74: plan the crontab before touching anything so
			// a system-job conversion never lands without its crontab lines.
			var curCrontab []string
			if includeSystem {
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				jobs, err := scheduler.PlanTZMigration(data, opts)
				if err != nil {
					return err
				}
				if n := countSystemConversions(jobs); n > 0 && !updateCrontab {
					return fmt.Errorf("%d system job(s) would be converted; pass --update-crontab so the crontab is rewritten too (the scheduler does not re-sync system jobs from cron_jobs.json)", n)
				}
				if curCrontab, _, err = loadCrontab(""); err != nil {
					return err
				}
				if _, _, err := scheduler.PlanCrontabTZUpdate(curCrontab, jobs, from, now, installMissing); err != nil {
					return err
				}
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

			if includeSystem && countSystemConversions(res.Jobs) > 0 {
				newCrontab, changes, err := scheduler.PlanCrontabTZUpdate(curCrontab, res.Jobs, from, now, installMissing)
				if err != nil {
					return fmt.Errorf("cron_jobs.json migrated but crontab not updated: %w (restore with: cp %s %s)", err, res.BackupPath, file)
				}
				writeCrontabChanges(out, changes, "crontab -l")
				bak := fmt.Sprintf("%s.crontab.bak-%s", file, now.UTC().Format("20060102T150405Z"))
				if err := os.WriteFile(bak, []byte(joinCrontab(curCrontab)), 0600); err != nil {
					return fmt.Errorf("cron_jobs.json migrated but crontab backup failed: %w (restore with: cp %s %s)", err, res.BackupPath, file)
				}
				if err := installCrontab(newCrontab); err != nil {
					return fmt.Errorf("cron_jobs.json migrated but installing the crontab failed: %w (restore with: cp %s %s; crontab backup %s)", err, res.BackupPath, file, bak)
				}
				fmt.Fprintf(out, "\nCrontab updated. Backup: %s (restore with: crontab %s)\n", bak, bak)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fromName, "from", "UTC", "zone the existing expressions are written in")
	cmd.Flags().StringVar(&toName, "to", "", "target zone for CRON_TZ (e.g. America/Los_Angeles)")
	cmd.Flags().StringVar(&file, "file", "", "path to cron_jobs.json")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan only (default)")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the migration (backup + atomic replace)")
	cmd.Flags().StringVar(&nowStr, "now", "", "reference time (RFC3339) instead of the current time")
	cmd.Flags().BoolVar(&includeSystem, "include-system", false, "also convert system (crontab) jobs to zone-guarded crontab lines")
	cmd.Flags().BoolVar(&updateCrontab, "update-crontab", false, "with --apply --include-system: rewrite the user crontab (backup first)")
	cmd.Flags().BoolVar(&installMissing, "install-missing", false, "with --include-system: add lines for converted enabled jobs missing from the crontab")
	cmd.Flags().StringVar(&crontabFile, "crontab-file", "", "with --dry-run --include-system: read the current crontab from this file instead of 'crontab -l'")
	return cmd
}

func countSystemConversions(jobs []scheduler.TZJobResult) int {
	n := 0
	for _, j := range jobs {
		if j.Type == scheduler.JobTypeSystem && j.Action == scheduler.TZConvert {
			n++
		}
	}
	return n
}

// Crontab I/O, swappable in tests so they never touch the real crontab.
var (
	readCrontabCmd = func() ([]byte, error) {
		var stderr bytes.Buffer
		c := exec.Command("crontab", "-l")
		c.Stderr = &stderr
		out, err := c.Output()
		if err != nil && strings.Contains(strings.ToLower(stderr.String()), "no crontab") {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("crontab -l: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	installCrontab = func(lines []string) error {
		c := exec.Command("crontab", "-")
		c.Stdin = strings.NewReader(joinCrontab(lines))
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("crontab -: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

// loadCrontab returns the crontab lines verbatim (blank lines and comments
// kept) from path, or from 'crontab -l' when path is empty.
func loadCrontab(path string) ([]string, string, error) {
	var data []byte
	var err error
	src := "crontab -l"
	if path != "" {
		src = path
		data, err = os.ReadFile(path)
	} else {
		data, err = readCrontabCmd()
	}
	if err != nil {
		return nil, src, err
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil, src, nil
	}
	return strings.Split(s, "\n"), src, nil
}

func joinCrontab(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// writeCrontabChanges prints the per-job crontab effect of a migration.
func writeCrontabChanges(w io.Writer, changes []scheduler.CrontabChange, src string) {
	fmt.Fprintf(w, "\nCrontab changes (current crontab from %s): %d system job(s)\n\n", src, len(changes))
	for _, c := range changes {
		fmt.Fprintf(w, "- %s %s [%s]", c.ID, c.Name, c.Action)
		if c.Note != "" {
			fmt.Fprintf(w, " (%s)", c.Note)
		}
		fmt.Fprintln(w)
		if c.Old != "" {
			fmt.Fprintf(w, "    - %s\n", c.Old)
		}
		if c.New != "" {
			fmt.Fprintf(w, "    + %s\n", c.New)
		}
	}
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
	var parts []string
	if typ == scheduler.JobTypeSystem {
		// conduit-31jg.74: simulate the rendered crontab line (daemon-zone
		// fields + zone guard) rather than trusting the CRON_TZ intent.
		fires, err := scheduler.NextCrontabFires(expr, from, after, n)
		if err != nil {
			return "unparseable"
		}
		for _, t := range fires {
			parts = append(parts, t.In(to).Format("Mon 2006-01-02 15:04 MST"))
		}
		if len(parts) == 0 {
			return "never"
		}
		return strings.Join(parts, "<br>")
	}
	s, err := scheduler.ParseJobSchedule(expr, typ)
	if err != nil {
		return "unparseable"
	}
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
	fmt.Fprintf(w, "| ID | Name | Type | Enabled | Action | Old expression | New expression | Old: next 2 (%[1]s) | New: next 2 (%[1]s) | Old: next 2 %[2]s | New: next 2 %[2]s | Notes |\n", opts.To, postLabel)
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, j := range jobs {
		newExpr := "`" + j.NewSchedule + "`"
		if j.Action != scheduler.TZConvert {
			newExpr = "(unchanged)"
		}
		notes := j.Flags
		if j.Reason != "" {
			notes = append([]string{j.Reason}, notes...)
		}
		fmt.Fprintf(w, "| %s | %s | %s | %t | %s | `%s` | %s | %s | %s | %s | %s | %s |\n",
			mdCell(j.ID), mdCell(j.Name), j.Type, j.Enabled, j.Action,
			mdCell(j.OldSchedule), mdCell(newExpr),
			fmtRuns(j.OldSchedule, j.Type, now, opts.From, opts.To, 2),
			fmtRuns(j.NewSchedule, j.Type, now, opts.From, opts.To, 2),
			fmtRuns(j.OldSchedule, j.Type, post, opts.From, opts.To, 2), // conduit-31jg.74: show the drift
			fmtRuns(j.NewSchedule, j.Type, post, opts.From, opts.To, 2),
			mdCell(strings.Join(notes, "; ")))
	}
}
