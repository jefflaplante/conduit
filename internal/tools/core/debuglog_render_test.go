package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/tools/debuglog"
)

// conduit-3kgo: LLM entries render with their metadata.
func TestDebugLogTool_RendersLLMEntries(t *testing.T) {
	buf := debuglog.NewRingBuffer(10)
	buf.Add(debuglog.LLMRequest("m1", map[string]string{"depth": "0", "last_preview": "tool output"}))
	resp := debuglog.LLMResponse("m1", "end_turn", 2*time.Second)
	resp.Meta = map[string]string{"tool_calls": "0"}
	buf.Add(resp)
	fail := debuglog.LLMResponse("m1", "error", time.Second)
	fail.Error = "boom"
	buf.Add(fail)

	res, err := NewDebugLogTool(nil, buf).Execute(context.Background(), map[string]interface{}{"action": "dump"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`→ LLM model=m1 depth=0 last_preview="tool output"`,
		"← LLM model=m1 stop=end_turn (2s) tool_calls=0",
		"stop=error (1s) ERROR: boom",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("dump lacks %q:\n%s", want, res.Content)
		}
	}
}
