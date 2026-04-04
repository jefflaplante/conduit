package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"conduit/internal/config"
)

// MQTTPublisher is the interface for publishing messages to MQTT topics.
// This decouples the deliverer from the concrete mqtt.Service implementation.
type MQTTPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (any, error)
}

// MQTTDeliverer delivers alerts via MQTT publish.
type MQTTDeliverer struct {
	publisher MQTTPublisher
}

// NewMQTTDeliverer creates a new MQTT deliverer.
func NewMQTTDeliverer(publisher MQTTPublisher) *MQTTDeliverer {
	return &MQTTDeliverer{publisher: publisher}
}

// Type returns the delivery type identifier.
func (d *MQTTDeliverer) Type() string {
	return "mqtt"
}

// Deliver publishes an alert to the configured MQTT topic.
// Required config: "topic"
// Optional config: "qos" (default "1"), "retain" (default "false")
func (d *MQTTDeliverer) Deliver(ctx context.Context, alert Alert, target config.AlertTarget) error {
	topic, ok := target.Config["topic"]
	if !ok || topic == "" {
		return fmt.Errorf("mqtt delivery requires 'topic' in target config")
	}

	// Parse QoS (default 1)
	qos := byte(1)
	if qosStr, ok := target.Config["qos"]; ok {
		if q, err := strconv.ParseUint(qosStr, 10, 8); err == nil && q <= 2 {
			qos = byte(q)
		}
	}

	// Parse retain flag (default false)
	retain := false
	if retainStr, ok := target.Config["retain"]; ok {
		retain = retainStr == "true"
	}

	// Serialize alert to JSON
	payload, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("failed to serialize alert: %w", err)
	}

	// Publish to MQTT
	_, err = d.publisher.Publish(ctx, topic, payload, qos, retain)
	if err != nil {
		return fmt.Errorf("mqtt publish failed: %w", err)
	}

	return nil
}
