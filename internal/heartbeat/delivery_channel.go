package heartbeat

import (
	"context"
	"fmt"
	"log"
	"strings"

	"conduit/internal/channels"
	"conduit/internal/config"
)

// conduit-31jg.59: heartbeat messages are delivered through the gateway's
// ChannelSender (so channel adapters, pairing and SanitizeOutgoingText all
// apply), but routed via DeliveryRegistry so every attempt gets the circuit
// breaker and an alert_history audit row.

// ChannelDelivererType is the DeliveryRegistry type for ChannelSenderDeliverer.
// It is a synthetic type: heartbeat targets ("telegram:<chat>") are resolved
// by the gateway's channel manager, not by per-type raw HTTP deliverers.
const ChannelDelivererType = "channel"

// Target config keys understood by ChannelSenderDeliverer.
const (
	targetConfigChannelID = "channel_id"
	targetConfigUserID    = "user_id"
)

// ChannelSenderDeliverer implements Deliverer on top of the gateway's
// ChannelSender. A nil sender logs and reports success, matching the
// pre-registry behaviour of GatewayIntegration without a channel sender.
type ChannelSenderDeliverer struct {
	sender ChannelSender
}

// NewChannelSenderDeliverer wraps sender as a Deliverer.
func NewChannelSenderDeliverer(sender ChannelSender) *ChannelSenderDeliverer {
	return &ChannelSenderDeliverer{sender: sender}
}

// Type returns ChannelDelivererType.
func (d *ChannelSenderDeliverer) Type() string { return ChannelDelivererType }

// Deliver sanitizes alert.Message and sends it to the channel/user in
// target.Config. Sanitizing here (not only in the caller) guarantees that
// nothing routed through the registry bypasses SanitizeOutgoingText.
func (d *ChannelSenderDeliverer) Deliver(ctx context.Context, alert Alert, target config.AlertTarget) error {
	channelID := target.Config[targetConfigChannelID]
	userID := target.Config[targetConfigUserID]
	if channelID == "" || userID == "" {
		return fmt.Errorf("channel target %q missing %s/%s", target.Name, targetConfigChannelID, targetConfigUserID)
	}
	message := channels.SanitizeOutgoingText(alert.Message)
	if d.sender == nil {
		log.Printf("[HeartbeatIntegration] No channel sender configured, would send: %s", message)
		return nil
	}
	return d.sender.SendMessage(ctx, channelID, userID, message, nil)
}

// channelTarget converts a heartbeat target string ("telegram:chatid" or a
// bare chat id, which defaults to telegram) into a registry AlertTarget. The
// full "<channel>:<user>" string is the target name, so the circuit breaker
// and audit trail are keyed per destination.
func channelTarget(target string) config.AlertTarget {
	channelID, userID := "telegram", target
	if parts := strings.SplitN(target, ":", 2); len(parts) == 2 {
		channelID, userID = parts[0], parts[1]
	}
	return config.AlertTarget{
		Name: channelID + ":" + userID,
		Type: ChannelDelivererType,
		Config: map[string]string{
			targetConfigChannelID: channelID,
			targetConfigUserID:    userID,
		},
	}
}
