package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDuration_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr string
	}{
		{in: `"5m"`, want: 5 * time.Minute},
		{in: `"30s"`, want: 30 * time.Second},
		{in: `"1h30m"`, want: 90 * time.Minute},
		{in: `"0"`, want: 0},
		{in: `300000000000`, want: 5 * time.Minute}, // integer nanoseconds (backward compat)
		{in: `0`, want: 0},
		{in: `"5 minutes"`, wantErr: "invalid duration"},
		{in: `""`, wantErr: "invalid duration"},
		{in: `1.5`, wantErr: "integer nanoseconds"},
		{in: `true`, wantErr: "invalid duration"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var d Duration
			err := json.Unmarshal([]byte(tt.in), &d)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Unmarshal(%s) error = %v, want containing %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", tt.in, err)
			}
			if d.Duration() != tt.want {
				t.Errorf("Unmarshal(%s) = %v, want %v", tt.in, d.Duration(), tt.want)
			}
		})
	}
}

func TestDuration_NullKeepsValue(t *testing.T) {
	d := Duration(time.Minute)
	if err := json.Unmarshal([]byte(`null`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Duration() != time.Minute {
		t.Errorf("null changed the value to %v", d.Duration())
	}
}

func TestDuration_MarshalRoundTrip(t *testing.T) {
	type wrap struct {
		D Duration `json:"d,omitempty"`
	}
	b, err := json.Marshal(wrap{D: Duration(90 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"d":"1m30s"}` {
		t.Errorf("Marshal = %s, want string form", b)
	}
	var back wrap
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.D.Duration() != 90*time.Second {
		t.Errorf("round trip = %v", back.D.Duration())
	}
	if b, _ := json.Marshal(wrap{}); string(b) != `{}` {
		t.Errorf("zero Duration with omitempty = %s, want omitted", b)
	}
}

// conduit-2lr5: the documented string forms must load through Parse.
func TestParse_RemoteSSHDurationStrings(t *testing.T) {
	doc := `{
  "port": 18789,
  "ai": {"default_provider": "anthropic", "providers": [{"name": "anthropic", "type": "anthropic", "model": "claude-sonnet-4-6"}]},
  "tools": {"enabled_tools": ["Read"], "max_tool_chains": 25},
  "remote_ssh": {
    "enabled": true,
    "hosts": [{"name": "host-a", "hostname": "host-a.example", "connect_timeout": "10s"}],
    "security": {"default_tier": "dangerous", "approval_timeout": "5m"},
    "pool": {"idle_timeout": "2m", "connect_timeout": "30s", "health_check_interval": 60000000000},
    "sessions": {"session_idle_timeout": "15m"},
    "defaults": {"connect_timeout": "20s"}
  },
  "agent_heartbeat": {"enabled": true, "interval_minutes": 5,
    "alert_retry_policy": {"max_retries": 3, "retry_interval": "30s", "backoff_factor": 2}}
}`
	cfg, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := cfg.RemoteSSH
	checks := map[string][2]time.Duration{
		"hosts[0].connect_timeout":   {r.Hosts[0].ConnectTimeout.Duration(), 10 * time.Second},
		"approval_timeout":           {r.Security.ApprovalTimeout.Duration(), 5 * time.Minute},
		"pool.idle_timeout":          {r.Pool.IdleTimeout.Duration(), 2 * time.Minute},
		"pool.connect_timeout":       {r.Pool.ConnectTimeout.Duration(), 30 * time.Second},
		"pool.health_check_interval": {r.Pool.HealthCheckInterval.Duration(), time.Minute},
		"session_idle_timeout":       {r.Sessions.SessionIdleTimeout.Duration(), 15 * time.Minute},
		"defaults.connect_timeout":   {r.Defaults.ConnectTimeout.Duration(), 20 * time.Second},
		"retry_interval":             {cfg.AgentHeartbeat.AlertRetryPolicy.RetryInterval.Duration(), 30 * time.Second},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %v, want %v", name, c[0], c[1])
		}
	}
}
