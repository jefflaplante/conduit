package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/protocol"
	"conduit/internal/tui"
)

// conduit-5uw3: WS and TUI steering. A steered request is closed with a
// "steered" notice tied to its request ID, never a StreamStart/StreamEnd.

// wsRecorder drains a test WS client's Send channel.
type wsRecorder struct {
	mu   sync.Mutex
	msgs []map[string]interface{}
}

func recordWS(t *testing.T, c *Client) *wsRecorder {
	t.Helper()
	r := &wsRecorder{}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case raw := <-c.Send:
				var m map[string]interface{}
				if err := json.Unmarshal(raw, &m); err == nil {
					r.mu.Lock()
					r.msgs = append(r.msgs, m)
					r.mu.Unlock()
				}
			case <-stop:
				return
			}
		}
	}()
	return r
}

// find returns the messages matching typ (and command, when not "").
func (r *wsRecorder) find(typ, command string) []map[string]interface{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]interface{}
	for _, m := range r.msgs {
		if m["type"] == typ && (command == "" || m["command"] == command) {
			out = append(out, m)
		}
	}
	return out
}

func TestSteer_WebSocket(t *testing.T) {
	gw, store, p, reg := newSteerTestGateway(t)
	sess, _ := store.GetOrCreateSession("u1", "tui_u1")
	c := newTestWSClient("c1")
	c.SetSessionKey(sess.Key)
	rec := recordWS(t, c)

	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{SessionKey: sess.Key, UserID: "u1", RequestID: "r1", Text: "deploy"})
	}()
	waitEntered(t, p, 1)
	reg.waitEntered(t)
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{SessionKey: sess.Key, UserID: "u1", RequestID: "r2", Text: "use prod-2"})
	}()
	waitForCond(t, "queued notice", func() bool { return len(rec.find("command_response", tui.QueuedNoticeCommand)) == 1 })
	if q := rec.find("command_response", tui.QueuedNoticeCommand)[0]; q["response"] != steerAckText || q["request_id"] != "r2" {
		t.Fatalf("queued notice = %v, want the steering ack for r2", q)
	}

	close(reg.gate)
	<-doneA
	<-doneB
	waitForCond(t, "steered notice", func() bool { return len(rec.find("command_response", tui.SteeredNoticeCommand)) == 1 })
	if s := rec.find("command_response", tui.SteeredNoticeCommand)[0]; s["request_id"] != "r2" || s["session_key"] != sess.Key {
		t.Fatalf("steered notice = %v", s)
	}
	if n := p.callCount(); n != 2 {
		t.Fatalf("provider calls = %d, want 2", n)
	}
	waitForCond(t, "r1 stream_end", func() bool {
		for _, m := range rec.find("stream_end", "") {
			if m["request_id"] == "r1" {
				return true
			}
		}
		return false
	})
	for _, typ := range []string{"stream_start", "stream_end"} {
		for _, m := range rec.find(typ, "") {
			if m["request_id"] == "r2" {
				t.Fatalf("steered request r2 got a %s: %v", typ, m)
			}
		}
	}
}

func TestSteer_TUI(t *testing.T) {
	gw, store, p, reg := newSteerTestGateway(t)
	c := NewDirectClient(DirectClientConfig{
		ParentCtx: context.Background(), UserID: "owner", Sessions: store, AI: gw.ai, Turns: gw.turns(),
	})
	defer c.Close()
	sess, _ := store.GetOrCreateSession("owner", "tui_owner")

	if err := c.SendChatWithID(sess.Key, "deploy", "r1"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)
	reg.waitEntered(t)
	if err := c.SendChatWithID(sess.Key, "use prod-2", "r2"); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, "B offered", func() bool {
		gw.turns().mu.Lock()
		defer gw.turns().mu.Unlock()
		box := gw.turns().steer[sess.Key]
		return box != nil && len(box.pending) == 1
	})
	close(reg.gate)
	waitForCond(t, "turns done", func() bool { return !gw.turns().Busy(sess.Key) })

	var queued, steered *tui.CommandResponseMsg
	deadline := time.After(10 * time.Second)
	for steered == nil {
		select {
		case m := <-c.inbox:
			switch v := m.(type) {
			case tui.CommandResponseMsg:
				if v.Command == tui.QueuedNoticeCommand {
					queued = &v
				}
				if v.Command == tui.SteeredNoticeCommand {
					steered = &v
				}
			case tui.StreamStartMsg:
				if v.RequestID == "r2" {
					t.Fatal("steered request r2 got a StreamStart")
				}
			case tui.StreamEndMsg:
				if v.RequestID == "r2" {
					t.Fatal("steered request r2 got a StreamEnd")
				}
			}
		case <-deadline:
			t.Fatal("no steered notice delivered to the TUI")
		}
	}
	if queued == nil || queued.Response != steerAckText {
		t.Fatalf("queued notice = %+v, want the steering ack", queued)
	}
	if steered.RequestID != "r2" || steered.SessionKey != sess.Key {
		t.Fatalf("steered notice = %+v", steered)
	}
	if n := p.callCount(); n != 2 {
		t.Fatalf("provider calls = %d, want 2", n)
	}
}

// Two TUI clients on one session: the second client's message must not be
// folded into the first client's turn, whose reply it would never see.
func TestSteer_TUIOtherClientNotFolded(t *testing.T) {
	gw, store, p, reg := newSteerTestGateway(t)
	newClient := func() *DirectClient {
		c := NewDirectClient(DirectClientConfig{
			ParentCtx: context.Background(), UserID: "owner", Sessions: store, AI: gw.ai, Turns: gw.turns(),
		})
		t.Cleanup(c.Close)
		return c
	}
	c1, c2 := newClient(), newClient()
	sess, _ := store.GetOrCreateSession("owner", "tui_owner")

	if err := c1.SendChatWithID(sess.Key, "deploy", "r1"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)
	reg.waitEntered(t)
	if err := c2.SendChatWithID(sess.Key, "from the other client", "r2"); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, "c2 queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })
	close(reg.gate)
	waitEntered(t, p, 2)
	waitEntered(t, p, 3)
	waitForCond(t, "turns done", func() bool { return !gw.turns().Busy(sess.Key) })
	for _, m := range p.request(2) {
		if strings.Contains(m.Content, "from the other client") {
			t.Fatal("another client's message was folded into c1's turn")
		}
	}
}
