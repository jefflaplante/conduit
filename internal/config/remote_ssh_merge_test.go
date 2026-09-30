package config

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// conduit-6wjo: a remote_ssh block is merged field-wise onto
// DefaultRemoteSSHConfig; omitted keys keep their defaults.
func TestRemoteSSHConfig_UnmarshalMergesDefaults(t *testing.T) {
	var cfg Config
	err := json.Unmarshal([]byte(`{"remote_ssh": {
		"enabled": true,
		"hosts": [{"name": "host-a", "hostname": "host-a.example"}],
		"security": {"approval_timeout": "10m"},
		"pool": {"idle_timeout": "2m"}
	}}`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultRemoteSSHConfig()
	got := cfg.RemoteSSH

	if !got.Enabled || len(got.Hosts) != 1 || got.Hosts[0].Hostname != "host-a.example" {
		t.Fatalf("explicit keys lost: %+v", got)
	}
	if got.Security.DefaultTier != def.Security.DefaultTier {
		t.Errorf("default_tier = %q, want default %q", got.Security.DefaultTier, def.Security.DefaultTier)
	}
	if !reflect.DeepEqual(got.Security.AllowedCommands, def.Security.AllowedCommands) {
		t.Error("allowed_commands must default (read/modify lists) when omitted")
	}
	if !reflect.DeepEqual(got.Security.BlockedPatterns, def.Security.BlockedPatterns) {
		t.Error("blocked_patterns must default when omitted")
	}
	if !got.Security.AllowPipes {
		t.Error("allow_pipes must keep its default (true) when omitted")
	}
	if got.Security.ApprovalTimeout.Duration() != 10*time.Minute {
		t.Errorf("approval_timeout = %v, want 10m", got.Security.ApprovalTimeout)
	}
	if !reflect.DeepEqual(got.Audit, def.Audit) {
		t.Errorf("audit must default (enabled) when omitted: %+v", got.Audit)
	}
	if got.Pool.IdleTimeout.Duration() != 2*time.Minute {
		t.Errorf("pool.idle_timeout = %v, want 2m", got.Pool.IdleTimeout)
	}
	if got.Pool.ConnectTimeout != def.Pool.ConnectTimeout || got.Pool.StrictHostKeyChecking != "yes" {
		t.Errorf("omitted pool keys must default: %+v", got.Pool)
	}
	if !reflect.DeepEqual(got.Sessions, def.Sessions) || !reflect.DeepEqual(got.Defaults, def.Defaults) {
		t.Error("sessions/defaults must default when omitted")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("merged config must validate: %v", err)
	}
}

// Explicit false, 0 and [] from the user win over the defaults.
func TestRemoteSSHConfig_UnmarshalRespectsExplicitZeroValues(t *testing.T) {
	var got RemoteSSHConfig
	err := json.Unmarshal([]byte(`{
		"security": {"allow_pipes": false, "allowed_commands": {"read": [], "modify": ["git"]}, "require_approval": []},
		"audit": {"enabled": false, "log_output": false},
		"pool": {"max_connections_per_host": 0}
	}`), &got)
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultRemoteSSHConfig()
	if got.Security.AllowPipes {
		t.Error("explicit allow_pipes=false ignored")
	}
	if got.Security.AllowedCommands.Read == nil || len(got.Security.AllowedCommands.Read) != 0 {
		t.Errorf("explicit read=[] must stay empty, got %v", got.Security.AllowedCommands.Read)
	}
	if !reflect.DeepEqual(got.Security.AllowedCommands.Modify, []string{"git"}) {
		t.Errorf("modify list must be replaced, not appended: %v", got.Security.AllowedCommands.Modify)
	}
	if !reflect.DeepEqual(got.Security.AllowedCommands.Dangerous, def.Security.AllowedCommands.Dangerous) {
		t.Error("omitted dangerous list must default")
	}
	if got.Security.RequireApproval == nil || len(got.Security.RequireApproval) != 0 {
		t.Errorf("explicit require_approval=[] must stay empty, got %v", got.Security.RequireApproval)
	}
	if got.Audit.Enabled || got.Audit.LogOutput {
		t.Errorf("explicit audit false values ignored: %+v", got.Audit)
	}
	if !got.Audit.LogCommands {
		t.Error("omitted audit.log_commands must keep its default (true)")
	}
	if got.Pool.MaxConnectionsPerHost != 0 {
		t.Errorf("explicit max_connections_per_host=0 ignored: %d", got.Pool.MaxConnectionsPerHost)
	}
}

func TestRemoteSSHConfig_UnmarshalNull(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"remote_ssh": null}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteSSH.Enabled {
		t.Error("null remote_ssh must stay disabled")
	}
}
