package mqtt

import (
	"testing"

	"conduit/internal/config"
)

// The service reports mqtt.require_approval / approval_exempt_topics to the
// tool layer; unset require_approval must reach it as "approval on".
func TestServiceStatus_ApprovalSettings(t *testing.T) {
	cfg := config.DefaultMQTTConfig()
	if st := NewServiceAdapter(NewService(cfg)).Status(); st.ApprovalDisabled || len(st.ApprovalExemptTopics) != 0 {
		t.Errorf("default status = %+v, want approval on with no exemptions", st)
	}

	off := false
	cfg.RequireApproval = &off
	cfg.ApprovalExemptTopics = []string{"zigbee2mqtt/lights/#"}
	st := NewServiceAdapter(NewService(cfg)).Status()
	if !st.ApprovalDisabled {
		t.Error("require_approval=false not reported")
	}
	if len(st.ApprovalExemptTopics) != 1 || st.ApprovalExemptTopics[0] != "zigbee2mqtt/lights/#" {
		t.Errorf("exempt topics = %v", st.ApprovalExemptTopics)
	}
}
