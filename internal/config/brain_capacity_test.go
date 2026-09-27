package config

import (
	"encoding/json"
	"testing"
)

// conduit-31jg.53: ltm_eviction_grace_seconds and max_wm_entries_per_user
// parse from JSON and default when omitted.
func TestBrainConfig_CapacityKnobs(t *testing.T) {
	var bc BrainConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"ltm_eviction_grace_seconds":90,"max_wm_entries_per_user":-1}`), &bc); err != nil {
		t.Fatal(err)
	}
	if err := bc.Validate(); err != nil {
		t.Fatal(err)
	}
	if bc.LTMEvictionGraceSeconds != 90 || bc.MaxWMEntriesPerUser != -1 {
		t.Fatalf("got grace=%d cap=%d", bc.LTMEvictionGraceSeconds, bc.MaxWMEntriesPerUser)
	}

	var omitted BrainConfig
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &omitted); err != nil {
		t.Fatal(err)
	}
	omitted.ApplyDefaults()
	if omitted.LTMEvictionGraceSeconds != 3600 || omitted.MaxWMEntriesPerUser != 1000 {
		t.Fatalf("defaults: grace=%d cap=%d", omitted.LTMEvictionGraceSeconds, omitted.MaxWMEntriesPerUser)
	}
}
