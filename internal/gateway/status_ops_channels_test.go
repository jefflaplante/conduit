package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"

	"conduit/internal/channels"
	"conduit/internal/protocol"
)

// switchFactory fails CreateAdapter while fail is set.
type switchFactory struct {
	mu   sync.Mutex
	fail bool
}

func (f *switchFactory) SupportsType(t string) bool  { return t == "fake" }
func (f *switchFactory) GetSupportedTypes() []string { return []string{"fake"} }
func (f *switchFactory) CreateAdapter(cfg channels.ChannelConfig) (channels.ChannelAdapter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("transient network error")
	}
	return &stubChannelAdapter{id: cfg.ID, in: make(chan *protocol.IncomingMessage)}, nil
}

func (f *switchFactory) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

type stubChannelAdapter struct {
	id string
	in chan *protocol.IncomingMessage
}

func (a *stubChannelAdapter) ID() string                                        { return a.id }
func (a *stubChannelAdapter) Name() string                                      { return a.id }
func (a *stubChannelAdapter) Type() string                                      { return "fake" }
func (a *stubChannelAdapter) Start(context.Context) error                       { return nil }
func (a *stubChannelAdapter) Stop() error                                       { return nil }
func (a *stubChannelAdapter) SendMessage(*protocol.OutgoingMessage) error       { return nil }
func (a *stubChannelAdapter) ReceiveMessages() <-chan *protocol.IncomingMessage { return a.in }
func (a *stubChannelAdapter) IsHealthy() bool                                   { return true }
func (a *stubChannelAdapter) Status() channels.ChannelStatus {
	return channels.ChannelStatus{Status: channels.StatusOnline, Details: map[string]interface{}{}}
}

// bd-twb0: enable_channel/disable_channel delegate to the channel manager,
// and a channel that failed to start shows up in GetChannelStatus.
func TestGateway_EnableDisableChannel(t *testing.T) {
	f := &switchFactory{fail: true}
	m := channels.NewManager()
	m.RegisterFactory(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, []channels.ChannelConfig{{ID: "c1", Type: "fake", Name: "c1", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop() }()
	g := &Gateway{channelManager: m}

	status, err := g.GetChannelStatus()
	if err != nil {
		t.Fatal(err)
	}
	st, ok := status["c1"].(channels.ChannelStatus)
	if !ok || st.Status != channels.StatusError || st.Details["retry_pending"] != true {
		t.Fatalf("GetChannelStatus()[c1] = %#v, want a retry-pending error status", status["c1"])
	}

	f.setFail(false)
	if err := g.EnableChannel(context.Background(), "c1"); err != nil {
		t.Fatalf("EnableChannel: %v", err)
	}
	if _, ok := m.GetAdapter("c1"); !ok {
		t.Fatal("EnableChannel did not create the adapter")
	}

	if err := g.DisableChannel(context.Background(), "c1"); err != nil {
		t.Fatalf("DisableChannel: %v", err)
	}
	if _, ok := m.GetAdapter("c1"); ok {
		t.Fatal("DisableChannel left the adapter registered")
	}
	if err := g.EnableChannel(context.Background(), "unknown"); err == nil {
		t.Fatal("EnableChannel(unknown) = nil, want error")
	}
}

func TestGateway_EnableChannel_NoManager(t *testing.T) {
	g := &Gateway{}
	if err := g.EnableChannel(context.Background(), "c1"); err == nil {
		t.Fatal("want error without a channel manager")
	}
	if err := g.DisableChannel(context.Background(), "c1"); err == nil {
		t.Fatal("want error without a channel manager")
	}
}
