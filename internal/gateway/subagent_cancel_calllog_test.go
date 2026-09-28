package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/ai"
)

// TestSubAgentCancel_InFlightCallLoggedAsContextCancel covers conduit-31jg.87
// end to end: a sub-agent canceled while its provider call is in flight
// produces a call-log record with agent_kind=subagent and
// error_class=context_cancel (not "other"/"timeout").
func TestSubAgentCancel_InFlightCallLoggedAsContextCancel(t *testing.T) {
	gw, store, hp := newSubAgentCancelGateway(t)
	logPath := filepath.Join(t.TempDir(), "llm-calls.jsonl")
	gw.ai.SetCallLog(ai.NewCallLog(ai.CallLogOptions{Path: logPath}))

	parent, err := store.GetOrCreateSession("u1", "chan1")
	if err != nil {
		t.Fatal(err)
	}
	child := spawnHanging(t, gw, sessionCtx(parent.Key), "worker")
	waitSub(t, "provider call in flight", func() bool { return hp.calls.Load() > 0 })

	if _, n, err := gw.CancelSubAgent(sessionCtx(parent.Key), child, "", "stop"); err != nil || n != 1 {
		t.Fatalf("CancelSubAgent n=%d err=%v", n, err)
	}
	waitSub(t, "child ended", subAgentEnded(gw, child))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gw.ai.CloseCallLog(ctx); err != nil {
		t.Fatalf("close call log: %v", err)
	}

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open call log: %v", err)
	}
	defer f.Close()
	var recs []ai.CallRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r ai.CallRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad record %q: %v", sc.Text(), err)
		}
		if r.SessionKey == child {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		t.Fatalf("no call-log record for canceled sub-agent %s", child)
	}
	for _, r := range recs {
		if r.AgentKind != "subagent" {
			t.Errorf("agent_kind = %q, want subagent (%+v)", r.AgentKind, r)
		}
		if r.ErrorClass != ai.ErrClassContextCancel {
			t.Errorf("error_class = %q, want %q (%+v)", r.ErrorClass, ai.ErrClassContextCancel, r)
		}
	}
}
