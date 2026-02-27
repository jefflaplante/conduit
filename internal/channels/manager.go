package channels

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"conduit/pkg/protocol"
)

// ReplyTagRe matches [[reply_to_current]] and [[reply_to:<id>]] with optional whitespace
var ReplyTagRe = regexp.MustCompile(`\[\[\s*reply_to(?:_current|:\s*(\d+))\s*\]\]`)

// StripReplyTags removes all [[reply_to_current]] and [[reply_to:<id>]] tags from text.
// Use this for channels (like TUI) that don't support reply threading.
func StripReplyTags(text string) string {
	return strings.TrimSpace(ReplyTagRe.ReplaceAllString(text, ""))
}

// Manager manages all channel adapters (both native Go and TypeScript processes)
type Manager struct {
	adapters     map[string]ChannelAdapter
	factories    map[string]ChannelFactory
	incoming     chan *protocol.IncomingMessage
	outgoing     chan *protocol.OutgoingMessage
	ctx          context.Context
	cancel       context.CancelFunc
	mutex        sync.RWMutex
	messageStats map[string]int64
}

// NewManager creates a new channel manager
func NewManager() *Manager {
	return &Manager{
		adapters:     make(map[string]ChannelAdapter),
		factories:    make(map[string]ChannelFactory),
		incoming:     make(chan *protocol.IncomingMessage, 1000),
		outgoing:     make(chan *protocol.OutgoingMessage, 1000),
		messageStats: make(map[string]int64),
	}
}

// RegisterFactory registers a channel adapter factory
func (m *Manager) RegisterFactory(factory ChannelFactory) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, adapterType := range factory.GetSupportedTypes() {
		m.factories[adapterType] = factory
		log.Printf("[ChannelManager] Registered factory for type: %s", adapterType)
	}
}

// Start initializes and starts the channel manager
func (m *Manager) Start(ctx context.Context, configs []ChannelConfig) error {
	m.ctx, m.cancel = context.WithCancel(ctx)

	// Create adapters from configurations
	for _, config := range configs {
		if !config.Enabled {
			log.Printf("[ChannelManager] Skipping disabled adapter: %s", config.ID)
			continue
		}

		if err := m.CreateAdapter(config); err != nil {
			log.Printf("[ChannelManager] Failed to create adapter %s: %v", config.ID, err)
			continue
		}
	}

	// Start message routing
	go m.routeMessages()

	log.Printf("[ChannelManager] Started with %d adapters", len(m.adapters))
	return nil
}

// Stop gracefully shuts down all adapters
func (m *Manager) Stop() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cancel != nil {
		m.cancel()
	}

	// Stop all adapters
	for id, adapter := range m.adapters {
		if err := adapter.Stop(); err != nil {
			log.Printf("[ChannelManager] Error stopping adapter %s: %v", id, err)
		}
	}

	// Close channels
	close(m.incoming)
	close(m.outgoing)

	log.Printf("[ChannelManager] Stopped")
	return nil
}

// CreateAdapter creates and starts a new adapter
func (m *Manager) CreateAdapter(config ChannelConfig) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	factory, exists := m.factories[config.Type]
	if !exists {
		return fmt.Errorf("no factory found for adapter type: %s", config.Type)
	}

	adapter, err := factory.CreateAdapter(config)
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// Start the adapter
	if err := adapter.Start(m.ctx); err != nil {
		return fmt.Errorf("failed to start adapter: %w", err)
	}

	m.adapters[config.ID] = adapter
	m.messageStats[config.ID] = 0

	// Start message forwarding from this adapter
	go m.forwardMessages(adapter)

	log.Printf("[ChannelManager] Created and started adapter: %s (%s)", config.ID, config.Type)
	return nil
}

// RemoveAdapter removes and stops an adapter
func (m *Manager) RemoveAdapter(id string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	adapter, exists := m.adapters[id]
	if !exists {
		return fmt.Errorf("adapter not found: %s", id)
	}

	if err := adapter.Stop(); err != nil {
		log.Printf("[ChannelManager] Error stopping adapter %s: %v", id, err)
	}

	delete(m.adapters, id)
	delete(m.messageStats, id)

	log.Printf("[ChannelManager] Removed adapter: %s", id)
	return nil
}

// SendMessage sends a message through the specified channel
func (m *Manager) SendMessage(msg *protocol.OutgoingMessage) error {
	select {
	case m.outgoing <- msg:
		return nil
	case <-m.ctx.Done():
		return fmt.Errorf("channel manager is shutting down")
	default:
		return fmt.Errorf("outgoing message queue is full")
	}
}

// ReceiveMessages returns the channel for incoming messages
func (m *Manager) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return m.incoming
}

// GetAdapter returns a specific adapter by ID
func (m *Manager) GetAdapter(id string) (ChannelAdapter, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	adapter, exists := m.adapters[id]
	return adapter, exists
}

// SendTypingIndicator sends a typing indicator if the adapter supports it
func (m *Manager) SendTypingIndicator(adapterID, chatID string) {
	m.mutex.RLock()
	adapter, exists := m.adapters[adapterID]
	m.mutex.RUnlock()

	if !exists {
		return
	}

	// Check if adapter implements TypingIndicator interface
	if ti, ok := adapter.(TypingIndicator); ok {
		if err := ti.SendTypingIndicator(chatID); err != nil {
			log.Printf("[ChannelManager] Failed to send typing indicator: %v", err)
		}
	}
}

// GetAdapters returns all active adapters
func (m *Manager) GetAdapters() map[string]ChannelAdapter {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make(map[string]ChannelAdapter)
	for id, adapter := range m.adapters {
		result[id] = adapter
	}
	return result
}

// GetStatus returns the status of all adapters
func (m *Manager) GetStatus() map[string]ChannelStatus {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make(map[string]ChannelStatus)
	for id, adapter := range m.adapters {
		status := adapter.Status()
		status.Details["message_count"] = m.messageStats[id]
		result[id] = status
	}
	return result
}

// GetStatusMap returns adapter status as a simple string map for error context
func (m *Manager) GetStatusMap() map[string]string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make(map[string]string)
	for id, adapter := range m.adapters {
		status := adapter.Status()
		result[id] = string(status.Status)
	}
	return result
}

// GetAvailableTargets returns a list of available channel targets with status
func (m *Manager) GetAvailableTargets() []string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	var targets []string
	for id, adapter := range m.adapters {
		status := adapter.Status()
		statusStr := string(status.Status)
		targets = append(targets, fmt.Sprintf("%s (%s)", id, statusStr))
	}

	if len(targets) == 0 {
		return []string{"No channels configured"}
	}

	return targets
}

// GetHealthyAdapters returns adapters that are currently healthy
func (m *Manager) GetHealthyAdapters() []ChannelAdapter {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	var healthy []ChannelAdapter
	for _, adapter := range m.adapters {
		if adapter.IsHealthy() {
			healthy = append(healthy, adapter)
		}
	}
	return healthy
}

// forwardMessages forwards incoming messages from an adapter to the main channel
func (m *Manager) forwardMessages(adapter ChannelAdapter) {
	for {
		select {
		case msg, ok := <-adapter.ReceiveMessages():
			if !ok {
				log.Printf("[ChannelManager] Adapter %s message channel closed", adapter.ID())
				return
			}

			select {
			case m.incoming <- msg:
				m.mutex.Lock()
				m.messageStats[adapter.ID()]++
				m.mutex.Unlock()
			case <-m.ctx.Done():
				return
			default:
				log.Printf("[ChannelManager] Warning: incoming message queue is full, dropping message from %s", adapter.ID())
			}

		case <-m.ctx.Done():
			return
		}
	}
}

// processReplyTags extracts [[reply_to_current]] and [[reply_to:<id>]] tags
// from outgoing message text, strips them, and sets reply_to_message_id metadata.
func processReplyTags(msg *protocol.OutgoingMessage) {
	match := ReplyTagRe.FindStringSubmatch(msg.Text)
	if match == nil {
		return
	}

	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string)
	}

	if match[1] != "" {
		// [[reply_to:<id>]] — explicit message ID
		msg.Metadata["reply_to_message_id"] = match[1]
	} else if srcID, ok := msg.Metadata["source_message_id"]; ok && srcID != "" {
		// [[reply_to_current]] — resolve from source message
		msg.Metadata["reply_to_message_id"] = srcID
	}

	// Strip all reply tags from the text
	msg.Text = strings.TrimSpace(ReplyTagRe.ReplaceAllString(msg.Text, ""))
}

// routeMessages handles outgoing message routing to appropriate adapters
func (m *Manager) routeMessages() {
	for {
		select {
		case msg, ok := <-m.outgoing:
			if !ok {
				return
			}

			// Strip reply tags from text; set metadata for adapters that support replies
			processReplyTags(msg)

			m.mutex.RLock()
			adapter, exists := m.adapters[msg.ChannelID]
			m.mutex.RUnlock()

			if !exists {
				// Check if this is a TUI channel that needs dynamic creation
				if strings.HasPrefix(msg.ChannelID, "tui_") {
					log.Printf("[ChannelManager] Creating dynamic TUI adapter for channel %s", msg.ChannelID)

					// Create dynamic TUI adapter configuration
					tuiConfig := ChannelConfig{
						ID:      msg.ChannelID,
						Type:    "tui",
						Name:    "TUI Dynamic",
						Enabled: true,
						Config:  map[string]interface{}{},
					}

					// Try to create the adapter
					if err := m.CreateAdapter(tuiConfig); err != nil {
						log.Printf("[ChannelManager] Failed to create dynamic TUI adapter for %s: %v", msg.ChannelID, err)
						continue
					}

					// Get the newly created adapter
					m.mutex.RLock()
					adapter, exists = m.adapters[msg.ChannelID]
					m.mutex.RUnlock()
				}

				if !exists {
					log.Printf("[ChannelManager] Warning: no adapter found for channel %s", msg.ChannelID)
					continue
				}
			}

			if err := adapter.SendMessage(msg); err != nil {
				log.Printf("[ChannelManager] Error sending message via %s: %v", msg.ChannelID, err)
			}

		case <-m.ctx.Done():
			return
		}
	}
}

// RestartAdapter stops and restarts a specific adapter
func (m *Manager) RestartAdapter(id string) error {
	m.mutex.RLock()
	adapter, exists := m.adapters[id]
	m.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("adapter not found: %s", id)
	}

	log.Printf("[ChannelManager] Restarting adapter: %s", id)

	// Stop the adapter
	if err := adapter.Stop(); err != nil {
		log.Printf("[ChannelManager] Error stopping adapter %s: %v", id, err)
	}

	// Wait a moment for cleanup
	time.Sleep(1 * time.Second)

	// Restart the adapter
	if err := adapter.Start(m.ctx); err != nil {
		return fmt.Errorf("failed to restart adapter %s: %w", id, err)
	}

	log.Printf("[ChannelManager] Successfully restarted adapter: %s", id)
	return nil
}

// GetMessageStats returns message statistics for all adapters
func (m *Manager) GetMessageStats() map[string]int64 {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make(map[string]int64)
	for id, count := range m.messageStats {
		result[id] = count
	}
	return result
}
