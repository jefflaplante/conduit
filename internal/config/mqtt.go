package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// MQTTConfig holds configuration for the optional MQTT event ingest service.
type MQTTConfig struct {
	Enabled         bool           `json:"enabled"`
	BrokerURL       string         `json:"broker_url" cfg:"env"`             // "tcp://192.168.1.10:1883"
	ClientID        string         `json:"client_id,omitempty"`              // default: unique "conduit-<host>-<rand>" per process
	Username        string         `json:"username,omitempty" cfg:"env"`     // ${MQTT_USERNAME}
	Password        string         `json:"password,omitempty" cfg:"env"`     // ${MQTT_PASSWORD}
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

// DefaultMQTTConfig returns sensible defaults for MQTT configuration.
func DefaultMQTTConfig() MQTTConfig {
	return MQTTConfig{
		Enabled:         false,
		BufferMaxAge:    3600,
		BufferMaxEvents: 1000,
		BufferMaxTopics: 500,
	}
}

// Validate checks the MQTT configuration for errors.
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

	// Apply defaults for zero-valued fields that weren't set
	// ClientID stays empty here: mqtt.NewClient assigns a unique per-process
	// ID (EffectiveClientID) so two instances never kick each other off the
	// broker (conduit-31jg.40). An explicit client_id is used verbatim.
	if m.BufferMaxAge <= 0 {
		m.BufferMaxAge = 3600
	}
	if m.BufferMaxEvents <= 0 {
		m.BufferMaxEvents = 1000
	}
	if m.BufferMaxTopics <= 0 {
		m.BufferMaxTopics = 500
	}

	return nil
}

// GenerateMQTTClientID returns a client ID unique to this process:
// "conduit-<host>-<8 hex>", at most 23 bytes so it fits MQTT 3.1.1's minimum
// broker limit. The old fixed default "conduit" made a second instance (or a
// restart racing the old session) disconnect the first (conduit-31jg.40).
func GenerateMQTTClientID() string {
	host, _ := os.Hostname()
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if b.Len() == 6 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("conduit-%s-%d", b.String(), os.Getpid())
	}
	if b.Len() == 0 {
		return "conduit-" + hex.EncodeToString(rnd[:])
	}
	return "conduit-" + b.String() + "-" + hex.EncodeToString(rnd[:])
}

// EffectiveClientID returns the configured client_id, or a freshly generated
// unique one when unset. Call once per connection owner and keep the result.
func (m *MQTTConfig) EffectiveClientID() string {
	if m.ClientID != "" {
		return m.ClientID
	}
	return GenerateMQTTClientID()
}
