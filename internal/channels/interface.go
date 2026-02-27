package channels

import (
	"context"
	"time"

	"conduit/pkg/protocol"
)

// ChannelAdapter defines the interface for all channel implementations
type ChannelAdapter interface {
	// ID returns the unique identifier for this adapter
	ID() string

	// Name returns the human-readable name for this adapter
	Name() string

	// Type returns the adapter type (e.g., "telegram", "whatsapp", "discord")
	Type() string

	// Start initializes and starts the adapter
	Start(ctx context.Context) error

	// Stop gracefully shuts down the adapter
	Stop() error

	// SendMessage sends an outgoing message through this channel
	SendMessage(msg *protocol.OutgoingMessage) error

	// ReceiveMessages returns a channel for incoming messages
	ReceiveMessages() <-chan *protocol.IncomingMessage

	// Status returns the current adapter status
	Status() ChannelStatus

	// IsHealthy returns whether the adapter is functioning properly
	IsHealthy() bool
}

// ChannelStatus represents the current status of a channel adapter
type ChannelStatus struct {
	Status    StatusCode             `json:"status"`
	Message   string                 `json:"message"`
	Details   map[string]interface{} `json:"details,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// StatusCode represents the various states an adapter can be in
type StatusCode string

const (
	StatusInitializing StatusCode = "initializing"
	StatusOnline       StatusCode = "online"
	StatusOffline      StatusCode = "offline"
	StatusError        StatusCode = "error"
	StatusReconnecting StatusCode = "reconnecting"
)

// ChannelConfig contains configuration for channel adapters
type ChannelConfig struct {
	ID      string                 `json:"id"`
	Type    string                 `json:"type"`
	Name    string                 `json:"name"`
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config"`
}

// TypingIndicator is an optional interface for adapters that support typing indicators
type TypingIndicator interface {
	SendTypingIndicator(chatID string) error
}

// StreamingAdapter is an optional interface for adapters that support message streaming
type StreamingAdapter interface {
	// SendMessageWithID sends a message and returns its ID for later editing
	SendMessageWithID(chatID int64, text string) (int, error)
	// EditMessageText edits an existing message
	EditMessageText(chatID int64, messageID int, text string) error
	// DeleteMessage deletes a message (used for silent response cleanup)
	DeleteMessage(chatID int64, messageID int) error
}

// ChannelFactory creates new channel adapters
type ChannelFactory interface {
	// SupportsType returns whether this factory can create adapters of the given type
	SupportsType(adapterType string) bool

	// CreateAdapter creates a new adapter instance
	CreateAdapter(config ChannelConfig) (ChannelAdapter, error)

	// GetSupportedTypes returns a list of adapter types this factory supports
	GetSupportedTypes() []string
}
