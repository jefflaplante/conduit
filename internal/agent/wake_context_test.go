package agent

import (
	"strings"
	"testing"
)

func TestBuildWakeContextSection(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		wantEmpty  bool
		wantSubstr string
	}{
		{name: "normal turn", source: "", wantEmpty: true},
		{name: "sub-agent announced", source: "sub_agent_announced", wantSubstr: "have seen it"},
		{name: "sub-agent silent", source: "sub_agent_silent", wantSubstr: "NOT seen"},
		{name: "legacy sub-agent callback", source: "sub_agent_callback", wantSubstr: "sub-agent callback"},
		{name: "generic inter-session", source: "inter_session", wantSubstr: "inter-session callback"},
		{name: "heartbeat", source: "heartbeat", wantSubstr: "heartbeat"},
		{name: "unknown source", source: "some_future_thing", wantSubstr: "some_future_thing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildWakeContextSection(tc.source)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("expected empty section for source=%q, got %q", tc.source, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("section for source=%q missing %q: %q", tc.source, tc.wantSubstr, got)
			}
		})
	}
}
