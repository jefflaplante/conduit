package mqtt

import (
	"context"

	"conduit/internal/tools/types"
)

// ServiceAdapter adapts *Service to the types.MQTTService interface,
// converting internal mqtt types to tool-layer types.
type ServiceAdapter struct {
	svc *Service
}

// NewServiceAdapter wraps a Service for tool-layer consumption.
func NewServiceAdapter(svc *Service) *ServiceAdapter {
	return &ServiceAdapter{svc: svc}
}

func (a *ServiceAdapter) Status() types.MQTTServiceStatus {
	s := a.svc.Status()
	return types.MQTTServiceStatus{
		Connected:        s.Connected,
		BrokerURL:        s.BrokerURL,
		SubscribedTopics: s.SubscribedTopics,
		ActiveTopics:     s.ActiveTopics,
		TotalEvents:      s.TotalEvents,
		PublishAllowed:   s.PublishAllowed,
	}
}

func (a *ServiceAdapter) Recent(limit int) []types.MQTTEvent {
	return convertEvents(a.svc.Recent(limit))
}

func (a *ServiceAdapter) RecentForTopic(topic string, limit int) []types.MQTTEvent {
	return convertEvents(a.svc.RecentForTopic(topic, limit))
}

func (a *ServiceAdapter) RecentMatching(pattern string, limit int) []types.MQTTEvent {
	return convertEvents(a.svc.RecentMatching(pattern, limit))
}

func (a *ServiceAdapter) Topics() []types.MQTTTopicSummary {
	summaries := a.svc.Topics()
	out := make([]types.MQTTTopicSummary, len(summaries))
	for i, s := range summaries {
		out[i] = types.MQTTTopicSummary{
			Topic:      s.Topic,
			EventCount: s.EventCount,
			LastEvent:  s.LastEvent,
			LastValue:  s.LastValue,
		}
	}
	return out
}

func (a *ServiceAdapter) Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*types.MQTTPublishResult, error) {
	r, err := a.svc.Publish(ctx, topic, payload, qos, retained)
	if err != nil {
		return nil, err
	}
	return &types.MQTTPublishResult{
		Topic:       r.Topic,
		QoS:         r.QoS,
		Retained:    r.Retained,
		PayloadSize: r.PayloadSize,
		BrokerAck:   r.BrokerAck,
	}, nil
}

func (a *ServiceAdapter) Devices() []types.MQTTDevice {
	devices := a.svc.Devices()
	out := make([]types.MQTTDevice, len(devices))
	for i, d := range devices {
		out[i] = types.MQTTDevice{
			IEEEAddress:  d.IEEEAddress,
			FriendlyName: d.FriendlyName,
			Type:         d.Type,
			ModelID:      d.ModelID,
			Manufacturer: d.Manufacturer,
			Description:  d.Description,
			Supported:    d.Supported,
			Disabled:     d.Disabled,
			MQTTTopic:    "zigbee2mqtt/" + d.FriendlyName,
		}
	}
	return out
}

func (a *ServiceAdapter) RetainedByPrefix(prefix string) []types.MQTTRetainedMessage {
	msgs := a.svc.RetainedByPrefix(prefix)
	out := make([]types.MQTTRetainedMessage, len(msgs))
	for i, m := range msgs {
		out[i] = types.MQTTRetainedMessage{
			Topic:     m.Topic,
			Payload:   m.Payload,
			Timestamp: m.Timestamp,
		}
	}
	return out
}

func (a *ServiceAdapter) RetainedPrefixes() []string {
	return a.svc.RetainedPrefixes()
}

func convertEvents(events []Event) []types.MQTTEvent {
	out := make([]types.MQTTEvent, len(events))
	for i, e := range events {
		out[i] = types.MQTTEvent{
			Topic:     e.Topic,
			Payload:   e.Payload,
			Timestamp: e.Timestamp,
			Retained:  e.Retained,
		}
	}
	return out
}
