package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
)

// conduit-31jg.60: the "quiet hours" hint follows the configured window and
// zone (config/quiet_hours.go) instead of a hardcoded 23:00-08:00 rule.
func TestComputeTimeContextAt_ConfiguredQuietHours(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	quiet := &config.AgentHeartbeatConfig{
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
		QuietHours:   config.QuietHoursConfig{StartTime: "20:00", EndTime: "06:00"},
	}
	disabled := *quiet
	disabled.QuietEnabled = false

	tests := []struct {
		name     string
		tz       string // prompt (user) timezone
		at       time.Time
		quiet    *config.AgentHeartbeatConfig
		want     string
		wantQuit bool
	}{
		// 21:30 PDT: quiet under 20-06 config, not under legacy 23-08.
		{"config quiet evening", "America/Los_Angeles", time.Date(2026, 9, 28, 21, 30, 0, 0, la), quiet, "Time context: Monday late night | quiet hours", true},
		{"legacy not quiet evening", "America/Los_Angeles", time.Date(2026, 9, 28, 21, 30, 0, 0, la), nil, "Time context: Monday late night", false},
		// 07:00 PDT: legacy says quiet (<08), config (end 06:00) says not.
		{"config awake 7am", "America/Los_Angeles", time.Date(2026, 9, 28, 7, 0, 0, 0, la), quiet, "Time context: Monday morning", false},
		{"legacy quiet 7am", "America/Los_Angeles", time.Date(2026, 9, 28, 7, 0, 0, 0, la), nil, "Time context: Monday morning | quiet hours", true},
		// Server/prompt zone UTC: quiet is still judged on the Pacific clock.
		// 16:00 UTC = 09:00 PDT (awake); 04:00 UTC = 21:00 PDT (quiet).
		{"utc prompt zone, config judges pacific", "UTC", time.Date(2026, 9, 29, 16, 0, 0, 0, time.UTC), quiet, "Time context: Tuesday afternoon", false},
		{"utc prompt zone, pacific night", "UTC", time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC), quiet, "Time context: Tuesday late night | quiet hours", true},
		// After DST ends (PST): 05:30 PST is quiet, 06:00 PST is not.
		{"after DST quiet edge", "America/Los_Angeles", time.Date(2026, 11, 2, 5, 30, 0, 0, la), quiet, "Time context: Monday morning | quiet hours", true},
		{"after DST end boundary", "America/Los_Angeles", time.Date(2026, 11, 2, 6, 0, 0, 0, la), quiet, "Time context: Monday morning", false},
		{"quiet disabled", "America/Los_Angeles", time.Date(2026, 9, 28, 23, 30, 0, 0, la), &disabled, "Time context: Monday late night", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeTimeContextAt(tc.tz, tc.at, tc.quiet)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "quiet hours") != tc.wantQuit {
				t.Errorf("quiet mismatch in %q", got)
			}
		})
	}
}

// The quiet config reaches the Situation Awareness (dynamic) section through
// SectionParams.
func TestBuildSituationAwareness_UsesConfiguredQuietHours(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	mock := newMockBrainLister()
	mock.addEntry("sense.tasks.active", "Some task", 0.5)
	pb := &PromptBuilder{brainService: mock}
	at := time.Date(2026, 9, 28, 21, 0, 0, 0, la)
	params := &SectionParams{
		UserTimezone: "America/Los_Angeles",
		Now:          func() time.Time { return at },
		QuietHours: &config.AgentHeartbeatConfig{
			Timezone: "America/Los_Angeles", QuietEnabled: true,
			QuietHours: config.QuietHoursConfig{StartTime: "20:00", EndTime: "06:00"},
		},
	}
	if got := pb.buildSituationAwareness(context.Background(), params); !strings.Contains(got, "| quiet hours") {
		t.Errorf("expected quiet hours hint, got:\n%s", got)
	}
}

func TestNewPromptBuilder_PropagatesQuietHours(t *testing.T) {
	q := &config.AgentHeartbeatConfig{QuietEnabled: true}
	a := NewConduitAgentWithIntegration(AgentConfig{Name: "t", QuietHours: q}, nil, nil, nil, nil, nil, nil)
	if a.promptBuilder.sectionParams.QuietHours != q {
		t.Fatal("QuietHours not propagated to section params")
	}
	a.SetTools(nil)
	if a.promptBuilder.sectionParams.QuietHours != q {
		t.Fatal("QuietHours lost on prompt builder rebuild")
	}
}
