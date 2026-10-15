package core

import (
	"testing"

	toolargs "conduit/internal/tools/args"
)

// bd-109l (sd-bd-3ose fix 4): deep sub-agent chains legitimately consume 4-6
// minutes of cumulative wall time, so the 300s default left zero slack for
// provider stalls — 5 overnight chain deaths Sep 25-26 died at exactly
// 5m0.003s. The default budget is raised to 600s (matching the tool's own
// documented example), and explicit 0/negative timeoutSeconds are rejected:
// GetInt passes an explicit 0 straight through, which would feed
// WithTimeout(ctx, 0) — an instantly-dead chain, the same silent-death class
// this fix targets.
func TestSessionsSpawnTimeoutDefault(t *testing.T) {
	if DefaultSubAgentTimeoutSeconds != 600 {
		t.Fatalf("DefaultSubAgentTimeoutSeconds = %d, want 600", DefaultSubAgentTimeoutSeconds)
	}

	params := (&SessionsSpawnTool{}).Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() missing properties map")
	}
	ts, ok := props["timeoutSeconds"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() missing timeoutSeconds schema")
	}
	if ts["default"] != 600 {
		t.Errorf("schema timeoutSeconds default = %v, want 600", ts["default"])
	}

	// Missing timeoutSeconds must resolve to the 600s default via the same
	// constant the schema advertises.
	if got := toolargs.GetInt(nil, "timeoutSeconds", DefaultSubAgentTimeoutSeconds); got != 600 {
		t.Errorf("GetInt default = %d, want 600", got)
	}

	// Explicit values pass through; only unusable ones normalize.
	for _, tc := range []struct {
		in, want int
	}{
		{0, DefaultSubAgentTimeoutSeconds},
		{-5, DefaultSubAgentTimeoutSeconds},
		{1800, 1800},
		{30, 30},
	} {
		if got := normalizeSubAgentTimeoutSeconds(tc.in); got != tc.want {
			t.Errorf("normalizeSubAgentTimeoutSeconds(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
