package tui

import (
	"context"
	"log"
	"sync"
	"time"

	"conduit/internal/channels"
	"conduit/pkg/protocol"
)

// Adapter implements ChannelAdapter for TUI connections
type Adapter struct {
	id             string
	incoming       chan *protocol.IncomingMessage
	outgoing       chan *protocol.OutgoingMessage
	status         channels.ChannelStatus
	ctx            context.Context
	cancel         context.CancelFunc
	mutex          sync.RWMutex
	messageHandler func(*protocol.OutgoingMessage) error
}

// NewAdapter creates a new TUI adapter
func NewAdapter(id string, messageHandler func(*protocol.OutgoingMessage) error) *Adapter {
	return &Adapter{
		id:             id,
		incoming:       make(chan *protocol.IncomingMessage, 100),
		outgoing:       make(chan *protocol.OutgoingMessage, 100),
		messageHandler: messageHandler,
		status: channels.ChannelStatus{
			Status:    channels.StatusInitializing,
			Message:   "TUI adapter starting",
			Timestamp: time.Now(),
		},
	}
}

// ID returns the unique identifier for this adapter
func (a *Adapter) ID() string {
	return a.id
}

// Name returns the human-readable name for this adapter
func (a *Adapter) Name() string {
	return "TUI Channel"
}

// Type returns the adapter type
func (a *Adapter) Type() string {
	return "tui"
}

// Start initializes and starts the adapter
func (a *Adapter) Start(ctx context.Context) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	a.ctx, a.cancel = context.WithCancel(ctx)

	a.status = channels.ChannelStatus{
		Status:    channels.StatusOnline,
		Message:   "TUI adapter online",
		Timestamp: time.Now(),
	}

	// Start message processing
	go a.processOutgoing()

	log.Printf("[TUIAdapter] Started adapter: %s", a.id)
	return nil
}

// Stop gracefully shuts down the adapter
func (a *Adapter) Stop() error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.cancel != nil {
		a.cancel()
	}

	a.status = channels.ChannelStatus{
		Status:    channels.StatusOffline,
		Message:   "TUI adapter stopped",
		Timestamp: time.Now(),
	}

	close(a.incoming)
	close(a.outgoing)

	log.Printf("[TUIAdapter] Stopped adapter: %s", a.id)
	return nil
}

// SendMessage sends an outgoing message through this channel
func (a *Adapter) SendMessage(msg *protocol.OutgoingMessage) error {
	select {
	case a.outgoing <- msg:
		return nil
	case <-a.ctx.Done():
		return context.Canceled
	default:
		return &ChannelError{
			Code:    "QUEUE_FULL",
			Message: "TUI outgoing message queue is full",
		}
	}
}

// ReceiveMessages returns a channel for incoming messages
func (a *Adapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return a.incoming
}

// Status returns the current adapter status
func (a *Adapter) Status() channels.ChannelStatus {
	a.mutex.RLock()
	defer a.mutex.RUnlock()
	return a.status
}

// IsHealthy returns whether the adapter is functioning properly
func (a *Adapter) IsHealthy() bool {
	a.mutex.RLock()
	defer a.mutex.RUnlock()
	return a.status.Status == channels.StatusOnline
}

// SendIncomingMessage allows the TUI client to send messages into the adapter
func (a *Adapter) SendIncomingMessage(msg *protocol.IncomingMessage) error {
	select {
	case a.incoming <- msg:
		return nil
	case <-a.ctx.Done():
		return context.Canceled
	default:
		return &ChannelError{
			Code:    "QUEUE_FULL",
			Message: "TUI incoming message queue is full",
		}
	}
}

// processOutgoing handles outgoing messages
func (a *Adapter) processOutgoing() {
	for {
		select {
		case msg, ok := <-a.outgoing:
			if !ok {
				return
			}

			if a.messageHandler != nil {
				if err := a.messageHandler(msg); err != nil {
					log.Printf("[TUIAdapter] Error handling outgoing message: %v", err)
				}
			}

		case <-a.ctx.Done():
			return
		}
	}
}

// ChannelError represents TUI channel-specific errors
type ChannelError struct {
	Code    string
	Message string
}

func (e *ChannelError) Error() string {
	return e.Message
}
