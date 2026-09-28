package types

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrMQTTPublishNotAllowed is returned when publish is attempted but not configured.
var ErrMQTTPublishNotAllowed = errors.New("mqtt: publishing is not allowed (publish_allowed is false)")

// MQTTEvent represents a single MQTT message for the tool layer.
type MQTTEvent struct {
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
	Retained  bool            `json:"retained,omitempty"`
}

// MQTTTopicSummary provides an overview of a single MQTT topic.
type MQTTTopicSummary struct {
	Topic      string          `json:"topic"`
	EventCount int             `json:"event_count"`
	LastEvent  time.Time       `json:"last_event"`
	LastValue  json.RawMessage `json:"last_value"`
}

// MQTTServiceStatus reports the current state of the MQTT service.
type MQTTServiceStatus struct {
	Connected        bool     `json:"connected"`
	BrokerURL        string   `json:"broker_url"`
	SubscribedTopics []string `json:"subscribed_topics"`
	ActiveTopics     int      `json:"active_topics"`
	TotalEvents      int64    `json:"total_events"`
	PublishAllowed   bool     `json:"publish_allowed"`
}

// MQTTPublishResult confirms broker acknowledgement of a published message.
type MQTTPublishResult struct {
	Topic       string `json:"topic"`
	QoS         byte   `json:"qos"`
	Retained    bool   `json:"retained"`
	PayloadSize int    `json:"payload_size"`
	BrokerAck   bool   `json:"broker_ack"` // true = broker confirmed receipt (QoS >= 1)
}

// MQTTDevice represents a parsed zigbee2mqtt device for the tool layer.
type MQTTDevice struct {
	IEEEAddress  string `json:"ieee_address"`
	FriendlyName string `json:"friendly_name"`
	Type         string `json:"type"`
	ModelID      string `json:"model_id"`
	Manufacturer string `json:"manufacturer"`
	Description  string `json:"description"`
	Supported    bool   `json:"supported"`
	Disabled     bool   `json:"disabled"`
	MQTTTopic    string `json:"mqtt_topic"` // synthetic: zigbee2mqtt/<friendly_name>
}

// MQTTRetainedMessage represents a retained MQTT message for the tool layer.
type MQTTRetainedMessage struct {
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// MQTTService provides MQTT event data to tools.
type MQTTService interface {
	Status() MQTTServiceStatus
	Recent(limit int) []MQTTEvent
	RecentForTopic(topic string, limit int) []MQTTEvent
	RecentMatching(pattern string, limit int) []MQTTEvent
	Topics() []MQTTTopicSummary
	Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*MQTTPublishResult, error)
	Devices() []MQTTDevice
	RetainedByPrefix(prefix string) []MQTTRetainedMessage
	RetainedPrefixes() []string
}
