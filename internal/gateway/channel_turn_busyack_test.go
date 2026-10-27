package gateway

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// conduit-1mnp / conduit-5uw3: the busy-ack cooldown only suppresses a
// repeat of the same kind.
func TestShouldSendBusyAck(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	recent := strconv.FormatInt(now.Add(-10*time.Second).Unix(), 10)
	old := strconv.FormatInt(now.Add(-31*time.Second).Unix(), 10)
	cases := []struct {
		name string
		ctx  map[string]string
		kind string
		want bool
	}{
		{"first ack", map[string]string{}, busyAckKindQueued, true},
		{"first steer ack", map[string]string{}, busyAckKindSteer, true},
		{"same kind within cooldown", map[string]string{"last_busy_ack": recent, "last_busy_ack_kind": busyAckKindSteer}, busyAckKindSteer, false},
		{"same kind after cooldown", map[string]string{"last_busy_ack": old, "last_busy_ack_kind": busyAckKindSteer}, busyAckKindSteer, true},
		{"steer after recent queued", map[string]string{"last_busy_ack": recent, "last_busy_ack_kind": busyAckKindQueued}, busyAckKindSteer, true},
		{"queued after recent steer", map[string]string{"last_busy_ack": recent, "last_busy_ack_kind": busyAckKindSteer}, busyAckKindQueued, true},
		{"legacy kindless ack counts as queued", map[string]string{"last_busy_ack": recent}, busyAckKindQueued, false},
		{"legacy kindless ack never blocks steer", map[string]string{"last_busy_ack": recent}, busyAckKindSteer, true},
	}
	for _, c := range cases {
		if got := shouldSendBusyAck(c.ctx, c.kind, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Queued records the kind it sent, reading the cooldown from the store
// (not the ingress snapshot), so a steering ack follows a fresh queued ack
// while a second queued ack is suppressed.
func TestChannelTurnSink_QueuedRecordsKind(t *testing.T) {
	gw, store, _ := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	snapshot := *sess // loaded before any ack, like a concurrent ingress

	kindAfter := func(ctx context.Context) (string, string) {
		newChannelTurnSink(gw, tgMsg("x"), &snapshot).Queued(ctx)
		fresh, err := store.GetSession(sess.Key)
		if err != nil {
			t.Fatal(err)
		}
		return fresh.Context["last_busy_ack_kind"], fresh.Context["last_busy_ack"]
	}

	kind, at1 := kindAfter(context.Background())
	if kind != busyAckKindQueued || at1 == "" {
		t.Fatalf("after queued ack: kind=%q at=%q", kind, at1)
	}
	if kind, _ = kindAfter(withSteerOffered(context.Background())); kind != busyAckKindSteer {
		t.Fatalf("steering ack suppressed by the recent queued ack (kind=%q)", kind)
	}
	// A repeat steer ack within the cooldown is suppressed. Pin the last
	// ack 10s back so a (wrongly) sent ack would visibly rewrite it.
	pinned := strconv.FormatInt(time.Now().Add(-10*time.Second).Unix(), 10)
	if err := store.SetSessionContext(sess.Key, "last_busy_ack", pinned); err != nil {
		t.Fatal(err)
	}
	if kind, at := kindAfter(withSteerOffered(context.Background())); kind != busyAckKindSteer || at != pinned {
		t.Fatalf("repeat steer ack not suppressed: kind=%q at=%q, want at=%s", kind, at, pinned)
	}
}
