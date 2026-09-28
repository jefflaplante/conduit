package scheduler

import (
	"fmt"
	"os/exec"
	"strings"
)

// addSystemCrontab adds a job to the system crontab
func (s *Scheduler) addSystemCrontab(job *Job) error {
	// Get current crontab
	entries, err := s.readSystemCrontab()
	if err != nil {
		return err
	}

	// Remove any existing entry for this job
	entries = s.filterCrontabEntries(entries, job.ID)

	// Add new entry (conduit-31jg.74: CRON_TZ schedules get a zone guard)
	entry, err := s.crontabLine(job)
	if err != nil {
		return err
	}
	entries = append(entries, entry)

	// Write back
	return s.writeSystemCrontab(entries)
}

// removeSystemCrontab removes a job from the system crontab
func (s *Scheduler) removeSystemCrontab(job *Job) error {
	entries, err := s.readSystemCrontab()
	if err != nil {
		return err
	}

	entries = s.filterCrontabEntries(entries, job.ID)
	return s.writeSystemCrontab(entries)
}

// readSystemCrontab reads the current user's crontab
func (s *Scheduler) readSystemCrontab() ([]string, error) {
	cmd := exec.Command("crontab", "-l")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// No crontab for user is okay — the message may appear in stderr
		// (captured via CombinedOutput) or in the error string itself.
		combined := string(output) + " " + err.Error()
		if strings.Contains(strings.ToLower(combined), "no crontab") {
			return []string{}, nil
		}
		// Also treat a generic exit status 1 with no stdout as "no crontab"
		// since some crontab implementations don't emit a descriptive message.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && len(strings.TrimSpace(string(output))) == 0 {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read crontab: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	lines := strings.Split(string(output), "\n")
	var entries []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip empty lines and stderr noise from CombinedOutput
		if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "crontab:") {
			continue
		}
		entries = append(entries, line)
	}
	return entries, nil
}

// writeSystemCrontab writes entries to the user's crontab
func (s *Scheduler) writeSystemCrontab(entries []string) error {
	content := strings.Join(entries, "\n")
	if len(entries) > 0 {
		content += "\n"
	}

	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

// filterCrontabEntries removes entries for a specific job ID
func (s *Scheduler) filterCrontabEntries(entries []string, jobID string) []string {
	marker := fmt.Sprintf(CrontabJobIDFormat, jobID)
	var filtered []string
	for _, entry := range entries {
		if !strings.Contains(entry, marker) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
