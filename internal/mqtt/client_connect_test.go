package mqtt

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/eclipse/paho.mqtt.golang/packets"
)

// H13: with SetConnectRetry(true) paho's connect token only completes once
// a broker answers, so Connect (and gateway startup) hung while the broker
// was down. Connect must return promptly and keep retrying in the
// background; subscriptions happen in OnConnect once the broker is up.

// closedAddr returns a loopback address with nothing listening on it.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// fakeBroker is a minimal MQTT 3.1.1 broker: it accepts CONNECT and
// SUBSCRIBE and answers pings.
type fakeBroker struct {
	l          net.Listener
	connects   atomic.Int32
	mu         sync.Mutex
	subscribed []string
	conns      []net.Conn
	wg         sync.WaitGroup
}

func startFakeBroker(t *testing.T, addr string) *fakeBroker {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	b := &fakeBroker{l: l}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			b.conns = append(b.conns, conn)
			b.mu.Unlock()
			b.wg.Add(1)
			go func() {
				defer b.wg.Done()
				b.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		b.mu.Lock()
		for _, c := range b.conns {
			c.Close()
		}
		b.mu.Unlock()
		b.wg.Wait()
	})
	return b
}

func (b *fakeBroker) serve(conn net.Conn) {
	defer conn.Close()
	for {
		p, err := packets.ReadPacket(conn)
		if err != nil {
			return
		}
		var reply packets.ControlPacket
		switch p := p.(type) {
		case *packets.ConnectPacket:
			b.connects.Add(1)
			reply = packets.NewControlPacket(packets.Connack)
		case *packets.SubscribePacket:
			b.mu.Lock()
			b.subscribed = append(b.subscribed, p.Topics...)
			b.mu.Unlock()
			ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
			ack.MessageID = p.MessageID
			ack.ReturnCodes = make([]byte, len(p.Topics))
			reply = ack
		case *packets.PingreqPacket:
			reply = packets.NewControlPacket(packets.Pingresp)
		case *packets.DisconnectPacket:
			return
		}
		if reply != nil {
			if err := reply.Write(conn); err != nil {
				return
			}
		}
	}
}

func (b *fakeBroker) topics() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.subscribed...)
}

// withConnectTiming shortens the connect timings for a test.
func withConnectTiming(t *testing.T, wait, retry time.Duration) {
	t.Helper()
	oldWait, oldRetry := initialConnectWait, connectRetryInterval
	initialConnectWait, connectRetryInterval = wait, retry
	t.Cleanup(func() { initialConnectWait, connectRetryInterval = oldWait, oldRetry })
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Start must not hang when the broker refuses connections; it uses the
// production timings.
func TestService_Start_BrokerDownReturnsPromptly(t *testing.T) {
	cfg := config.MQTTConfig{BrokerURL: "tcp://" + closedAddr(t), Topics: []string{"acme/#"}}
	svc := NewService(cfg)

	errc := make(chan error, 1)
	start := time.Now()
	go func() { errc <- svc.Start(context.Background()) }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Start with broker down = %v, want nil (retry in background)", err)
		}
	case <-time.After(initialConnectWait + 3*time.Second):
		t.Fatalf("Start blocked for more than %v with the broker down", time.Since(start).Round(time.Second))
	}
	if svc.Status().Connected {
		t.Fatal("Status reports connected with no broker")
	}

	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked")
	}
}

// After Connect returns, the client keeps retrying and subscribes to the
// configured topics once the broker comes up.
func TestClient_Connect_RetriesInBackgroundThenSubscribes(t *testing.T) {
	withConnectTiming(t, 200*time.Millisecond, 50*time.Millisecond)
	addr := closedAddr(t)
	c := NewClient(config.MQTTConfig{BrokerURL: "tcp://" + addr, Topics: []string{"acme/a", "acme/b"}}, nil)
	t.Cleanup(c.Close)

	done := make(chan error, 1)
	go func() { done <- c.Connect(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Connect = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect blocked with the broker down")
	}
	if c.IsConnected() {
		t.Fatal("connected with no broker")
	}

	b := startFakeBroker(t, addr)
	waitFor(t, 5*time.Second, c.IsConnected, "background connect")
	waitFor(t, 5*time.Second, func() bool { return len(b.topics()) == 2 }, "subscriptions")
	// Wait for the OnConnect pass to finish (under c.mu) so its goroutine
	// is done before the next test touches package state.
	waitFor(t, 5*time.Second, func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.subscribeRounds == 1
	}, "OnConnect subscription pass")
	// Disconnect before the broker goes away, so paho doesn't start a
	// reconnect loop that outlives the test.
	c.Close()
}

// A cancelled context returns ctx.Err() and stops the retry loop, so no
// paho client is left connecting in the background.
func TestClient_Connect_ContextCancelStopsRetry(t *testing.T) {
	withConnectTiming(t, 10*time.Second, 50*time.Millisecond)
	addr := closedAddr(t)
	c := NewClient(config.MQTTConfig{BrokerURL: "tcp://" + addr}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := c.Connect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Connect returned after %v, want prompt return on cancel", d)
	}

	// Let any in-flight retry sleep finish, then bring the broker up: a
	// leaked retry loop would connect to it.
	time.Sleep(200 * time.Millisecond)
	b := startFakeBroker(t, addr)
	time.Sleep(500 * time.Millisecond)
	if n := b.connects.Load(); n != 0 {
		t.Fatalf("broker saw %d CONNECTs after Connect was cancelled; retry loop leaked", n)
	}
	if c.IsConnected() {
		t.Fatal("client connected after cancel")
	}
}
