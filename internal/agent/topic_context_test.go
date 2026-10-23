package agent

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/tools/types"
)

// conduit-43vk: the topic prefix/persona rides the turn ctx into a dynamic
// prompt section, so it never perturbs the cached static block.

func TestBuildTopicSection(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "home turn", prompt: "", want: ""},
		{name: "blank", prompt: "  \n ", want: ""},
		{name: "topic", prompt: "Topic: alpha — new.", want: "## Topic\nTopic: alpha — new."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildTopicSection(tt.prompt); got != tt.want {
				t.Fatalf("buildTopicSection(%q) = %q, want %q", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestTopicSection_DynamicOnWarmCache(t *testing.T) {
	a, _ := newClockedAgent(t)
	s := sessionWithModel("claude-sonnet-4-6")
	ctx := context.Background()

	home := mustBuild(t, a, ctx, s)
	if strings.Contains(home[1].Text, "## Topic") {
		t.Fatalf("home turn rendered a topic section: %q", home[1].Text)
	}

	topicCtx := types.WithTopicPrompt(ctx, "Topic: alpha — Shipping v2. Last activity: 3h ago.\nBe terse.")
	tp := mustBuild(t, a, topicCtx, s)
	if !strings.Contains(tp[1].Text, "## Topic\nTopic: alpha — Shipping v2.") || !strings.Contains(tp[1].Text, "Be terse.") {
		t.Fatalf("topic section missing from dynamic block: %q", tp[1].Text)
	}
	if tp[0].Text != home[0].Text {
		t.Fatalf("topic prompt leaked into the static (cached) block")
	}
}
