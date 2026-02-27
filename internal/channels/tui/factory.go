package tui

import (
	"fmt"

	"conduit/internal/channels"
	"conduit/pkg/protocol"
)

// Factory creates TUI channel adapters
type Factory struct {
	messageHandler func(*protocol.OutgoingMessage) error
}

// NewFactory creates a new TUI adapter factory
func NewFactory(messageHandler func(*protocol.OutgoingMessage) error) *Factory {
	return &Factory{
		messageHandler: messageHandler,
	}
}

// SupportsType returns whether this factory can create adapters of the given type
func (f *Factory) SupportsType(adapterType string) bool {
	return adapterType == "tui"
}

// CreateAdapter creates a new TUI adapter instance
func (f *Factory) CreateAdapter(config channels.ChannelConfig) (channels.ChannelAdapter, error) {
	if config.Type != "tui" {
		return nil, fmt.Errorf("unsupported adapter type: %s", config.Type)
	}

	adapter := NewAdapter(config.ID, f.messageHandler)
	return adapter, nil
}

// GetSupportedTypes returns a list of adapter types this factory supports
func (f *Factory) GetSupportedTypes() []string {
	return []string{"tui"}
}
