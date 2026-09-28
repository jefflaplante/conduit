package core

import (
	"strings"
	"testing"
)

func TestFormatProviderSlots(t *testing.T) {
	got := formatProviderSlots(map[string]interface{}{"provider_slots": []map[string]interface{}{
		{"provider": "z-ai", "model": "glm-5.3", "limit": 5, "in_flight": int64(5), "waiting": int64(2)},
		{"provider": "anthropic", "model": "", "limit": 0, "in_flight": int64(1), "waiting": int64(0)},
	}})
	for _, want := range []string{"z-ai/glm-5.3: 5 in flight, 2 waiting (limit 5)", "anthropic: 1 in flight, 0 waiting (limit unlimited)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if formatProviderSlots(map[string]interface{}{}) != "" {
		t.Error("no slots should render nothing")
	}
}
