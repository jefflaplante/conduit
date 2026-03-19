package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"sync"
	"time"

	"conduit/internal/config"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// Client wraps paho.mqtt.golang with auto-reconnect and topic subscription.
type Client struct {
	cfg        config.MQTTConfig
	pahoClient pahomqtt.Client
	onMessage  func(Event)

	mu        sync.RWMutex
	connected bool
}

// NewClient creates a new MQTT client (does not connect yet).
func NewClient(cfg config.MQTTConfig, onMessage func(Event)) *Client {
	return &Client{
		cfg:       cfg,
		onMessage: onMessage,
	}
}

// Connect establishes the MQTT connection and subscribes to configured topics.
func (c *Client) Connect(ctx context.Context) error {
	opts := pahomqtt.NewClientOptions()
	opts.AddBroker(c.cfg.BrokerURL)
	opts.SetClientID(c.cfg.ClientID)

	if c.cfg.Username != "" {
		opts.SetUsername(c.cfg.Username)
	}
	if c.cfg.Password != "" {
		opts.SetPassword(c.cfg.Password)
	}

	// TLS configuration
	if c.cfg.TLS != nil {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: c.cfg.TLS.Insecure,
		}
		opts.SetTLSConfig(tlsCfg)
	}

	// Auto-reconnect with backoff
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(2 * time.Minute)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)

	// Connection handlers
	opts.SetOnConnectHandler(func(client pahomqtt.Client) {
		log.Printf("[MQTT] Connected to %s", c.cfg.BrokerURL)
		c.mu.Lock()
		c.connected = true
		c.mu.Unlock()
		c.subscribeAll(client)
	})

	opts.SetConnectionLostHandler(func(client pahomqtt.Client, err error) {
		log.Printf("[MQTT] Connection lost: %v", err)
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
	})

	opts.SetReconnectingHandler(func(client pahomqtt.Client, opts *pahomqtt.ClientOptions) {
		log.Printf("[MQTT] Reconnecting to %s...", c.cfg.BrokerURL)
	})

	// Message handler
	opts.SetDefaultPublishHandler(func(client pahomqtt.Client, msg pahomqtt.Message) {
		if c.onMessage != nil {
			// Defensive copy of payload before passing to callback
			payload := make([]byte, len(msg.Payload()))
			copy(payload, msg.Payload())

			if msg.Retained() {
				log.Printf("[MQTT] Retained message: %s (%d bytes)", msg.Topic(), len(payload))
			}

			c.onMessage(Event{
				Topic:     msg.Topic(),
				Payload:   payload,
				Timestamp: time.Now(),
				Retained:  msg.Retained(),
			})
		}
	})

	// Keep-alive
	opts.SetKeepAlive(60 * time.Second)
	opts.SetPingTimeout(10 * time.Second)

	c.pahoClient = pahomqtt.NewClient(opts)

	token := c.pahoClient.Connect()
	// Wait with context awareness
	done := make(chan struct{})
	go func() {
		token.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		if token.Error() != nil {
			return fmt.Errorf("mqtt connect: %w", token.Error())
		}
	}

	return nil
}

// subscribeAll subscribes to all configured topics.
func (c *Client) subscribeAll(client pahomqtt.Client) {
	qos := byte(c.cfg.QoS)
	for _, topic := range c.cfg.Topics {
		token := client.Subscribe(topic, qos, nil)
		token.Wait()
		if token.Error() != nil {
			log.Printf("[MQTT] Failed to subscribe to %s: %v", topic, token.Error())
		} else {
			log.Printf("[MQTT] Subscribed to %s (QoS %d)", topic, qos)
		}
	}
}

// PublishResult holds the outcome of a publish operation.
type PublishResult struct {
	Topic       string
	QoS         byte
	Retained    bool
	PayloadSize int
	BrokerAck   bool // true when broker confirmed receipt (QoS >= 1)
}

// Publish sends a message to a topic and waits for broker acknowledgement.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*PublishResult, error) {
	if c.pahoClient == nil {
		return nil, fmt.Errorf("mqtt client not initialized")
	}
	if !c.pahoClient.IsConnected() {
		return nil, fmt.Errorf("mqtt client not connected to broker")
	}

	// Force QoS 1 minimum for publish so we get broker ACK (PUBACK)
	if qos < 1 {
		qos = 1
	}

	log.Printf("[MQTT] Publishing to %s (QoS %d, retained=%v, %d bytes)", topic, qos, retained, len(payload))
	token := c.pahoClient.Publish(topic, qos, retained, payload)

	done := make(chan struct{})
	go func() {
		token.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		log.Printf("[MQTT] Publish to %s cancelled: %v", topic, ctx.Err())
		return nil, ctx.Err()
	case <-done:
		if token.Error() != nil {
			log.Printf("[MQTT] Publish to %s failed: %v", topic, token.Error())
			return nil, token.Error()
		}
		log.Printf("[MQTT] Published to %s — broker ACK received (QoS %d)", topic, qos)
		return &PublishResult{
			Topic:       topic,
			QoS:         qos,
			Retained:    retained,
			PayloadSize: len(payload),
			BrokerAck:   true, // token.Wait() + no error = PUBACK received at QoS 1
		}, nil
	}
}

// IsConnected returns the current connection status.
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Close disconnects the MQTT client.
func (c *Client) Close() {
	if c.pahoClient != nil {
		c.pahoClient.Disconnect(1000) // 1s grace
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		log.Println("[MQTT] Disconnected")
	}
}
