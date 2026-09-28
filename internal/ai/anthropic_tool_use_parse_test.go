package ai

import (
	"encoding/json"
	"testing"

	"conduit/internal/models"
)

// TestParseAnthropicToolCall_RejectsEmptyIDOrName covers conduit-31jg.87: the
// non-streaming parser must reject a tool_use block whose id or name is an
// empty string, exactly like the streaming parser does.
func TestParseAnthropicToolCall_RejectsEmptyIDOrName(t *testing.T) {
	a := &AnthropicProvider{}
	cases := map[string]struct {
		raw  string
		want bool
	}{
		"valid":      {`{"type":"tool_use","id":"t1","name":"Read","input":{}}`, true},
		"empty id":   {`{"type":"tool_use","id":"","name":"Read","input":{}}`, false},
		"empty name": {`{"type":"tool_use","id":"t1","name":"","input":{}}`, false},
		"both empty": {`{"type":"tool_use","id":"","name":"","input":{}}`, false},
		"missing id": {`{"type":"tool_use","name":"Read","input":{}}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var block models.ResponseBlock
			if err := json.Unmarshal([]byte(tc.raw), &block); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := a.parseAnthropicToolCall(block) != nil
			if got != tc.want {
				t.Fatalf("accepted=%v, want %v", got, tc.want)
			}
		})
	}
}
