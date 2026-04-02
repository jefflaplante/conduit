package config

import "fmt"

// MQTTConfig holds configuration for the optional MQTT event ingest service.
type MQTTConfig struct {
	Enabled         bool           `json:"enabled"`
	BrokerURL       string         `json:"broker_url"`                       // "tcp://192.168.1.10:1883"
	ClientID        string         `json:"client_id,omitempty"`              // default: "conduit"
	Username        string         `json:"username,omitempty"`               // ${MQTT_USERNAME}
	Password        string         `json:"password,omitempty"`               // ${MQTT_PASSWORD}
	Topics          []string       `json:"topics"`                           // ["zigbee2mqtt/#"]
	QoS             int            `json:"qos,omitempty"`                    // 0-2, default 0
	BufferMaxAge    int            `json:"buffer_max_age_seconds,omitempty"` // default 3600
	BufferMaxEvents int            `json:"buffer_max_events,omitempty"`      // per topic, default 1000
	BufferMaxTopics int            `json:"buffer_max_topics,omitempty"`      // default 500
	PublishAllowed  bool           `json:"publish_allowed,omitempty"`        // default false (safety)
	TLS             *MQTTTLSConfig `json:"tls,omitempty"`
}

// MQTTTLSConfig holds optional TLS settings for MQTT connections.
type MQTTTLSConfig struct {
	CACert     string `json:"ca_cert,omitempty"`
	ClientCert string `json:"client_cert,omitempty"`
	ClientKey  string `json:"client_key,omitempty"`
	Insecure   bool   `json:"insecure,omitempty"`
}

// Validate checks the MQTT configuration for errors and applies defaults.
func (m *MQTTConfig) Validate() error {
	if !m.Enabled {
		return nil
	}

	if m.BrokerURL == "" {
		return fmt.Errorf("mqtt: broker_url is required when enabled")
	}

	if len(m.Topics) == 0 {
		return fmt.Errorf("mqtt: at least one topic subscription is required")
	}

	if m.QoS < 0 || m.QoS > 2 {
		return fmt.Errorf("mqtt: qos must be 0, 1, or 2 (got %d)", m.QoS)
	}

	// Apply defaults
	if m.ClientID == "" {
		m.ClientID = "conduit"
	}
	if m.BufferMaxAge <= 0 {
		m.BufferMaxAge = 3600
	}
	if m.BufferMaxEvents <= 0 {
		m.BufferMaxEvents = 1000
	}
	if m.BufferMaxTopics <= 0 {
		m.BufferMaxTopics = 1000
	}

	return nil
}
