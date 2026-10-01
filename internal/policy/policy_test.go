package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/approval"
)

func testConfig() Config {
	return Config{
		Mode:    ModeShadow,
		Default: Allow,
		Classes: map[string]Decision{
			MessageSelf:    Allow,
			"message":      Ask,
			"email.send":   Ask,
			EmailSendOwner: Ask,
			"web.fetch":    Allow,
		},
		Recipients:   map[string][]string{EmailSendAgent: {"Owner@Example.com"}},
		OwnerTargets: []string{"telegram:42"},
	}
}

func interactive() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), approval.Origin{
		Source: "telegram", ChannelID: "telegram", UserID: "42", SessionKey: "s1",
	})
}

func TestEvaluate_TableOwnerAndRecipients(t *testing.T) {
	e := New(testConfig(), nil)
	for _, tt := range []struct {
		a        Action
		class    string
		decision Decision
		rule     string
	}{
		{Action{MessageDM, "telegram:42"}, MessageSelf, Allow, "class:message.self"},
		{Action{MessageDM, "Telegram:42 "}, MessageSelf, Allow, "class:message.self"},
		{Action{MessageDM, "telegram:7"}, MessageDM, Ask, "class:message"},
		{Action{MessageGroup, "telegram:-1001"}, MessageGroup, Ask, "class:message"},
		{Action{EmailSendAgent, "owner@example.com"}, EmailSendAgent, Allow, "recipient_listed"},
		{Action{EmailSendAgent, "stranger@example.org"}, EmailSendAgent, Ask, "class:email.send"},
		{Action{EmailSendOwner, "owner@example.com"}, EmailSendOwner, Ask, "class:email.send.owner"},
		{Action{"bash.exec", ""}, "bash.exec", Allow, "default"},
	} {
		r := e.Evaluate(interactive(), []Action{tt.a})[0]
		if r.Class != tt.class || r.Decision != tt.decision || r.Rule != tt.rule {
			t.Errorf("%v -> %s/%s/%s, want %s/%s/%s", tt.a, r.Class, r.Decision, r.Rule, tt.class, tt.decision, tt.rule)
		}
	}
}

// A recipient list never applies to the owner-account class unless listed
// for it: listing under email.send.agent must not leak to email.send.owner.
func TestEvaluate_RecipientsScopedToClass(t *testing.T) {
	e := New(testConfig(), nil)
	r := e.Evaluate(interactive(), []Action{{EmailSendOwner, "owner@example.com"}})[0]
	if r.Decision != Ask {
		t.Fatalf("owner-account send to a listed agent recipient = %s, want ask", r.Decision)
	}
}

// Nobody can answer a non-interactive turn, so ask becomes deny; allow stays.
func TestEvaluate_NonInteractiveAskBecomesDeny(t *testing.T) {
	e := New(testConfig(), nil)
	for _, ctx := range []context.Context{
		context.Background(),
		approval.WithNonInteractive(context.Background(), "cron"),
	} {
		rs := e.Evaluate(ctx, []Action{{MessageGroup, "telegram:-1"}, {MessageDM, "telegram:42"}})
		if rs[0].Policy != Ask || rs[0].Decision != Deny || rs[0].Rule != "class:message+non_interactive" {
			t.Errorf("group from non-interactive = %+v", rs[0])
		}
		if rs[1].Decision != Allow {
			t.Errorf("message.self from non-interactive = %+v", rs[1])
		}
	}
}

type memRecorder struct{ recs []Record }

func (m *memRecorder) Record(r Record) { m.recs = append(m.recs, r) }

func TestObserve_RecordsAndOffIsSilent(t *testing.T) {
	rec := &memRecorder{}
	e := New(testConfig(), rec)
	e.Observe(interactive(), "Message", "tell the group the build is green", []Action{{MessageGroup, "telegram:-5"}})
	if len(rec.recs) != 1 {
		t.Fatalf("records = %d", len(rec.recs))
	}
	r := rec.recs[0]
	if r.Tool != "Message" || r.Class != MessageGroup || r.Decision != Ask || r.Origin != "telegram" ||
		!r.Interactive || r.SessionKey != "s1" || r.Purpose == "" || r.Mode != ModeShadow {
		t.Fatalf("record = %+v", r)
	}

	off := testConfig()
	off.Mode = ModeOff
	rec2 := &memRecorder{}
	if res := New(off, rec2).Observe(interactive(), "Message", "", []Action{{MessageGroup, "x"}}); res != nil || len(rec2.recs) != 0 {
		t.Fatal("mode off must not evaluate or record")
	}
	var nilEngine *Engine
	if nilEngine.Observe(interactive(), "Message", "", []Action{{MessageGroup, "x"}}) != nil {
		t.Fatal("nil engine must be a no-op")
	}
}

func TestFileRecorder_RotateReadSummarize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy", "decisions.jsonl")
	r, err := NewFileRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	r.maxBytes = 400 // force a rotation after a couple of records
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, d := range []Decision{Allow, Ask, Deny, Allow, Ask} {
		r.Record(Record{Time: base.Add(time.Duration(i) * time.Hour), Class: MessageGroup, Decision: d, Tool: "Message"})
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotated generation: %v", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("decision log mode = %v, want 0600", st.Mode().Perm())
	}

	all, err := ReadRecords(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	recent, _ := ReadRecords(path, base.Add(2*time.Hour))
	if len(recent) != 3 || !recent[0].Time.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("since filter: %d records", len(recent))
	}
	s := Summarize(all)
	if s.Total != len(all) || len(s.Flagged) == 0 {
		t.Fatalf("summary = %+v", s)
	}
	for _, f := range s.Flagged {
		if f.Decision == Allow {
			t.Fatal("allow decisions must not be flagged")
		}
	}

	if recs, err := ReadRecords(filepath.Join(t.TempDir(), "missing.jsonl"), time.Time{}); err != nil || len(recs) != 0 {
		t.Fatalf("missing log: %v %v", recs, err)
	}
}

func TestParseDecision(t *testing.T) {
	if d, err := ParseDecision(" ASK "); err != nil || d != Ask {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := ParseDecision("maybe"); err == nil {
		t.Fatal("invalid decision accepted")
	}
}
