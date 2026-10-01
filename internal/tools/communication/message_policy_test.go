package communication

import (
	"context"
	"testing"

	"conduit/internal/policy"
	"conduit/internal/tools/types"
)

func TestMessageTool_ClassifyActions(t *testing.T) {
	m := &MessageTool{}
	ctx := types.WithRequestContext(context.Background(), "telegram", "42", "s1")
	for _, tt := range []struct {
		args   map[string]interface{}
		class  string
		target string
	}{
		{map[string]interface{}{"action": "send", "target": "telegram"}, policy.MessageDM, "telegram:42"},
		{map[string]interface{}{"target": "telegram:7"}, policy.MessageDM, "telegram:7"},
		{map[string]interface{}{"action": "send", "target": "telegram:-1001234"}, policy.MessageGroup, "telegram:-1001234"},
		{map[string]interface{}{"action": "send", "target": "tui_me"}, policy.MessageSelf, "tui_me:42"},
	} {
		got := m.ClassifyActions(ctx, tt.args)
		if len(got) != 1 || got[0].Class != tt.class || got[0].Target != tt.target {
			t.Errorf("%v -> %+v, want %s %s", tt.args, got, tt.class, tt.target)
		}
	}
	b := m.ClassifyActions(ctx, map[string]interface{}{"action": "broadcast", "targets": []interface{}{"telegram:-5", "telegram:9"}})
	if len(b) != 2 || b[0].Class != policy.MessageGroup || b[1].Class != policy.MessageDM {
		t.Fatalf("broadcast = %+v", b)
	}
	for _, a := range []string{"status", "react", "edit", "delete"} {
		if got := m.ClassifyActions(ctx, map[string]interface{}{"action": a, "target": "telegram:-5"}); len(got) != 0 {
			t.Errorf("%s has no outward effect, got %+v", a, got)
		}
	}
}
