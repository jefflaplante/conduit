package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"conduit/internal/tools/types"
)

type fakeCancelGateway struct {
	types.GatewayService
	gotKey, gotLabel, gotReason string
	n                           int
	err                         error
}

func (f *fakeCancelGateway) CancelSubAgent(_ context.Context, key, label, reason string) (string, int, error) {
	f.gotKey, f.gotLabel, f.gotReason = key, label, reason
	if f.err != nil {
		return "", 0, f.err
	}
	if key == "" {
		key = "subagent_by_label"
	}
	return key, f.n, nil
}

type plainGateway struct{ types.GatewayService }

func TestSessionsCancelTool(t *testing.T) {
	ctx := context.Background()

	gw := &fakeCancelGateway{n: 3}
	tool := NewSessionsCancelTool(&types.ToolServices{Gateway: gw})
	if tool.Name() != "SessionsCancel" {
		t.Fatalf("name = %q", tool.Name())
	}
	res, _ := tool.Execute(ctx, map[string]interface{}{})
	if res.Success || !strings.Contains(res.Error, "sessionKey or label") {
		t.Errorf("missing args = %+v", res)
	}
	res, _ = tool.Execute(ctx, map[string]interface{}{"label": "worker", "reason": "done"})
	if !res.Success || gw.gotLabel != "worker" || gw.gotReason != "done" || !strings.Contains(res.Content, "with 2 sub-agent(s)") {
		t.Errorf("cancel by label = %+v (gw %+v)", res, gw)
	}
	gw.err = errors.New("permission denied: nope")
	res, _ = tool.Execute(ctx, map[string]interface{}{"sessionKey": "k"})
	if res.Success || !strings.Contains(res.Error, "permission denied") {
		t.Errorf("refused cancel = %+v", res)
	}
	if st := tool.SelfTest(ctx, nil); st.Status != types.SelfTestStatusOK {
		t.Errorf("selftest = %+v", st)
	}

	noCancel := NewSessionsCancelTool(&types.ToolServices{Gateway: plainGateway{}})
	res, _ = noCancel.Execute(ctx, map[string]interface{}{"sessionKey": "k"})
	if res.Success || !strings.Contains(res.Error, "not available") {
		t.Errorf("no canceller = %+v", res)
	}
	if st := noCancel.SelfTest(ctx, nil); st.Status != types.SelfTestStatusFailed {
		t.Errorf("selftest without canceller = %+v", st)
	}
}

func TestFormatSubAgents(t *testing.T) {
	got := formatSubAgents(map[string]interface{}{"sub_agents": []map[string]interface{}{
		{"session_key": "subagent_1", "label": "w", "status": "canceled", "canceled_by": "/stop", "task": "dig"},
		{"session_key": "subagent_2", "status": "running"},
	}})
	for _, want := range []string{`subagent_1 ("w"): canceled by /stop — dig`, "subagent_2: running"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
