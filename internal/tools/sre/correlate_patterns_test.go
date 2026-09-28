//go:build with_sre

package sre

import (
	"context"
	"reflect"
	"testing"

	"conduit/internal/tools/types"
)

// TestGetLogPatterns_TopFiveByFrequency covers conduit-oc3u: the error-count
// map used to be ranged directly and truncated to 5, so the "top 5" were a
// random subset in random order. They must be the most frequent, descending,
// ties broken by text.
func TestGetLogPatterns_TopFiveByFrequency(t *testing.T) {
	tool, exec := setupTestTool(t)

	counts := map[string]int{
		"err a": 2, "err b": 9, "err c": 4, "err d": 7,
		"err e": 4, "err f": 3, "err g": 8, "err once": 1,
	}
	var logs []interface{}
	for msg, n := range counts {
		for i := 0; i < n; i++ {
			logs = append(logs, map[string]interface{}{"message": msg})
		}
	}
	exec.setResult("Datadog", &types.ToolResult{Success: true, Data: map[string]interface{}{"logs": logs}})

	want := []string{"err b (x9)", "err g (x8)", "err d (x7)", "err c (x4)", "err e (x4)"}
	for i := 0; i < 10; i++ {
		got := tool.getLogPatterns(context.Background(), "svc", "1h")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: got %v, want %v", i, got, want)
		}
	}
}
