package channels

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"conduit/internal/protocol"
)

// ReplyTagRe matches [[reply_to_current]] and [[reply_to:<id>]] with optional whitespace
var ReplyTagRe = regexp.MustCompile(`\[\[\s*reply_to(?:_current|:\s*(\d+))\s*\]\]`)

// StripReplyTags removes all [[reply_to_current]] and [[reply_to:<id>]] tags from text.
// Use this for channels (like TUI) that don't support reply threading.
func StripReplyTags(text string) string {
	return strings.TrimSpace(ReplyTagRe.ReplaceAllString(text, ""))
}

// Manager manages all channel adapters (native Go)
type Manager struct {
	adapters     map[string]ChannelAdapter
	factories    map[string]ChannelFactory
	incoming     chan *protocol.IncomingMessage
	outgoing     chan *protocol.OutgoingMessage
	ctx          context.Context
	cancel       context.CancelFunc
	mutex        sync.RWMutex
	messageStats map[string]int64

	// conduit-31jg.26: incoming/outgoing are NEVER closed — they have many
	// senders (adapters' forwarders, every SendMessage caller). Shutdown is
	// signalled by cancelling ctx; wg tracks routeMessages + forwarders so
	// Stop can wait for them; forwarders holds per-adapter cancel funcs so a
	// removed adapter's forwarder exits too.
	wg         sync.WaitGroup
	forwarders map[string]context.CancelFunc
	stopped    bool
}

// NewManager creates a new channel manager
func NewManager() *Manager {
	return &Manager{
		adapters:     make(map[string]ChannelAdapter),
		factories:    make(map[string]ChannelFactory),
		incoming:     make(chan *protocol.IncomingMessage, 1000),
		outgoing:     make(chan *protocol.OutgoingMessage, 1000),
		messageStats: make(map[string]int64),
		forwarders:   make(map[string]context.CancelFunc),
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
	m.mutex.Lock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.stopped = false
	m.wg.Add(1)
	m.mutex.Unlock()

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
	go func() {
		defer m.wg.Done()
		m.routeMessages()
	}()

	log.Printf("[ChannelManager] Started with %d adapters", len(m.adapters))
	return nil
}

// Stop gracefully shuts down all adapters.
//
// conduit-31jg.26: Stop used to close(m.incoming) / close(m.outgoing) while
// forwarders and SendMessage callers could still send (a send on a closed
// channel is "ready" in select and panics even alongside ctx.Done), and the
// gateway's processMessages would read nil messages. Now: cancel ctx, stop
// adapters, wait for our goroutines, leave the channels open. Idempotent.
func (m *Manager) Stop() error {
	m.mutex.Lock()
	if m.stopped {
		m.mutex.Unlock()
		return nil
	}
	m.stopped = true
	if m.cancel != nil {
		m.cancel()
	}
	adapters := make(map[string]ChannelAdapter, len(m.adapters))
	for id, adapter := range m.adapters {
		adapters[id] = adapter
	}
	m.mutex.Unlock()

	// Stop adapters outside the lock: adapter Stop may block briefly
	// (waiting on its poller), and routeMessages takes m.mutex.
	for id, adapter := range adapters {
		if err := adapter.Stop(); err != nil {
			log.Printf("[ChannelManager] Error stopping adapter %s: %v", id, err)
		}
	}

	m.wg.Wait()

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

	if m.stopped || m.ctx == nil {
		return fmt.Errorf("channel manager is not running")
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
	m.startForwarderLocked(config.ID, adapter)

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

	if cancelFwd, ok := m.forwarders[id]; ok {
		cancelFwd()
		delete(m.forwarders, id)
	}
	delete(m.adapters, id)
	delete(m.messageStats, id)

	log.Printf("[ChannelManager] Removed adapter: %s", id)
	return nil
}

// SendMessage sends a message through the specified channel
func (m *Manager) SendMessage(msg *protocol.OutgoingMessage) error {
	m.mutex.RLock()
	ctx := m.ctx
	m.mutex.RUnlock()
	if ctx == nil {
		return fmt.Errorf("channel manager is not running")
	}
	// Check shutdown first: select picks randomly among ready cases, so a
	// message could otherwise be queued after Stop with nobody routing it.
	if ctx.Err() != nil {
		return fmt.Errorf("channel manager is shutting down")
	}
	select {
	case m.outgoing <- msg:
		return nil
	case <-ctx.Done():
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

// startForwarderLocked spawns the forwarder for adapter under its own
// cancelable context (child of the manager ctx). Caller holds m.mutex.
func (m *Manager) startForwarderLocked(id string, adapter ChannelAdapter) {
	if prev, ok := m.forwarders[id]; ok {
		prev()
	}
	fctx, fcancel := context.WithCancel(m.ctx)
	m.forwarders[id] = fcancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.forwardMessages(fctx, adapter)
	}()
}

// forwardMessages forwards incoming messages from an adapter to the main
// channel until ctx (the per-adapter forwarder context) is cancelled.
func (m *Manager) forwardMessages(ctx context.Context, adapter ChannelAdapter) {
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
				if _, ok := m.messageStats[adapter.ID()]; ok {
					m.messageStats[adapter.ID()]++
				}
				m.mutex.Unlock()
			default:
				log.Printf("[ChannelManager] Warning: incoming message queue is full, dropping message from %s", adapter.ID())
			}

		case <-ctx.Done():
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
	m.mutex.RLock()
	ctx := m.ctx
	m.mutex.RUnlock()
	for {
		select {
		case msg, ok := <-m.outgoing:
			if !ok {
				return
			}

			// Strip reply tags from text; set metadata for adapters that support replies
			processReplyTags(msg)

			// Sanitize remaining internal markers (MEDIA: lines, excessive newlines)
			msg.Text = SanitizeOutgoingText(msg.Text)

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

		case <-ctx.Done():
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

	// Restart the adapter. conduit-31jg.26: adapters no longer close their
	// ReceiveMessages channel on Stop, so the existing forwarder keeps
	// working across the restart; re-arm it anyway in case an adapter
	// implementation still closes (the forwarder would have exited).
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.stopped {
		return fmt.Errorf("channel manager is not running")
	}
	if err := adapter.Start(m.ctx); err != nil {
		return fmt.Errorf("failed to restart adapter %s: %w", id, err)
	}
	m.startForwarderLocked(id, adapter)

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
