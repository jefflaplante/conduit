package agent

import (
	"strings"
	"testing"
)

func TestBuildRuntimeSection_UserTimezone(t *testing.T) {
	tests := []struct {
		name         string
		timezone     string
		wantContains string
	}{
		{
			name:         "Pacific timezone shows PST or PDT",
			timezone:     "America/Los_Angeles",
			wantContains: "P", // PST or PDT depending on time of year
		},
		{
			name:         "UTC timezone shows UTC",
			timezone:     "UTC",
			wantContains: "UTC",
		},
		{
			name:         "empty timezone defaults to server time",
			timezone:     "",
			wantContains: "Current time:",
		},
		{
			name:         "invalid timezone falls back gracefully",
			timezone:     "Invalid/Timezone",
			wantContains: "Current time:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &SectionParams{
				UserTimezone: tt.timezone,
			}
			result := buildRuntimeSection(params, map[string]string{
				"agent": "conduit",
			})

			if !strings.Contains(result, tt.wantContains) {
				t.Errorf("buildRuntimeSection() = %q, want it to contain %q", result, tt.wantContains)
			}
		})
	}
}

func TestBuildRuntimeSection_PacificTimezone(t *testing.T) {
	params := &SectionParams{
		UserTimezone: "America/Los_Angeles",
	}
	result := buildRuntimeSection(params, map[string]string{})

	// Should contain either PST or PDT
	if !strings.Contains(result, "PST") && !strings.Contains(result, "PDT") {
		t.Errorf("buildRuntimeSection with Pacific timezone should contain PST or PDT, got: %q", result)
	}
}

func TestBuildRuntimeSection_InvalidTimezoneNoPanic(t *testing.T) {
	params := &SectionParams{
		UserTimezone: "Not/A/Real/Zone",
	}
	// Should not panic
	result := buildRuntimeSection(params, map[string]string{"agent": "test"})
	if result == "" {
		t.Error("buildRuntimeSection should return non-empty even with invalid timezone")
	}
}
