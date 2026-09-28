package core

import (
	"strings"
	"testing"
)

// conduit-3a4v: finished sub-agents wake the parent session automatically
// (announce=false included), so SessionsSpawn must not tell the model to poll.
func TestSessionsSpawnDescription_NoPolling(t *testing.T) {
	desc := (&SessionsSpawnTool{}).Description()
	if strings.Contains(desc, "poll SessionStatus to") {
		t.Errorf("description still instructs polling: %s", desc)
	}
	for _, want := range []string{"automatically", "Do not poll"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q: %s", want, desc)
		}
	}
}
