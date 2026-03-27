package mqtt

import (
	"context"
	"log"
	"time"

	"conduit/internal/config"
)

// ServiceStatus reports the current state of the MQTT service.
type ServiceStatus struct {
	Connected       bool     `json:"connected"`
	BrokerURL       string   `json:"broker_url"`
	SubscribedTopics []string `json:"subscribed_topics"`
	ActiveTopics    int      `json:"active_topics"`
	TotalEvents     int64    `json:"total_events"`
	PublishAllowed  bool     `json:"publish_allowed"`
}

// Service owns the MQTT client and event buffer, providing the query API.
type Service struct {
	cfg            config.MQTTConfig
	client         *Client
	buffer         *EventBuffer
	retained       *RetainedStore
	deviceRegistry *DeviceRegistry
	cancel         context.CancelFunc
}

// NewService creates a new MQTT service (does not start yet).
func NewService(cfg config.MQTTConfig) *Service {
	buffer := NewEventBuffer(cfg.BufferMaxAge, cfg.BufferMaxEvents, cfg.BufferMaxTopics)
	return &Service{
		cfg:            cfg,
		buffer:         buffer,
		retained:       NewRetainedStore(),
		deviceRegistry: NewDeviceRegistry(),
	}
}

// Start connects to the broker and begins background maintenance.
func (s *Service) Start(ctx context.Context) error {
	s.client = NewClient(s.cfg, func(e Event) {
		s.buffer.Add(e)

		// Store retained messages persistently
		if e.Retained {
			s.retained.Set(e.Topic, e.Payload, e.Timestamp)
		}

		// Parse zigbee2mqtt device list
		if e.Topic == "zigbee2mqtt/bridge/devices" {
			if err := s.deviceRegistry.Update(e.Payload); err != nil {
				log.Printf("[MQTT] Failed to parse bridge/devices: %v", err)
			}
		}
	})

	if err := s.client.Connect(ctx); err != nil {
		return err
	}

	// Background prune goroutine
	pruneCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pruneCtx.Done():
				return
			case <-ticker.C:
				if n := s.buffer.Prune(); n > 0 {
					debugf("[MQTT] Pruned %d old events", n)
				}
			}
		}
	}()

	return nil
}

// Stop disconnects the client and stops background tasks.
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.client != nil {
		s.client.Close()
	}
}

// Status returns the current service status.
func (s *Service) Status() ServiceStatus {
	connected := false
	if s.client != nil {
		connected = s.client.IsConnected()
	}
	return ServiceStatus{
		Connected:        connected,
		BrokerURL:        s.cfg.BrokerURL,
		SubscribedTopics: s.cfg.Topics,
		ActiveTopics:     s.buffer.ActiveTopics(),
		TotalEvents:      s.buffer.TotalEvents(),
		PublishAllowed:   s.cfg.PublishAllowed,
	}
}

// Recent returns the most recent events across all topics.
func (s *Service) Recent(limit int) []Event {
	return s.buffer.Recent(limit)
}

// RecentForTopic returns recent events for a specific topic.
func (s *Service) RecentForTopic(topic string, limit int) []Event {
	return s.buffer.RecentForTopic(topic, limit)
}

// RecentMatching returns recent events for topics matching a glob pattern.
func (s *Service) RecentMatching(pattern string, limit int) []Event {
	return s.buffer.RecentMatching(pattern, limit)
}

// Topics returns summaries of all active topics.
func (s *Service) Topics() []TopicSummary {
	return s.buffer.Topics()
}

// Devices returns the parsed zigbee2mqtt device list.
func (s *Service) Devices() []Device {
	return s.deviceRegistry.Devices()
}

// RetainedByPrefix returns retained messages matching a topic prefix.
func (s *Service) RetainedByPrefix(prefix string) []RetainedMessage {
	return s.retained.GetByPrefix(prefix)
}

// RetainedPrefixes returns unique top-level prefixes of retained topics.
func (s *Service) RetainedPrefixes() []string {
	return s.retained.Prefixes()
}

// Publish sends a message to a topic (gated by config).
func (s *Service) Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*PublishResult, error) {
	if !s.cfg.PublishAllowed {
		log.Printf("[MQTT] Publish rejected: publish_allowed is false")
		return nil, ErrPublishNotAllowed
	}
	if s.client == nil {
		return nil, ErrNotConnected
	}
	return s.client.Publish(ctx, topic, payload, qos, retained)
}
