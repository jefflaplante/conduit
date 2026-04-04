package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigLoadE2E_EnvAndTildeExpansion verifies that the full Load() pipeline
// correctly expands ${ENV_VAR} and ~/ in all config fields, matching the behavior
// of the old manual expandEnvVars() and expandTilde() functions.
func TestConfigLoadE2E_EnvAndTildeExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-ant-xxx")
	t.Setenv("TEST_OAUTH", "oauth-tok-123")
	t.Setenv("TEST_CLIENT", "client-id-456")
	t.Setenv("TEST_BRAVE_KEY", "brave-key-789")
	t.Setenv("TEST_BOT_TOKEN", "bot:telegram-tok")
	t.Setenv("TEST_MQTT_BROKER", "tcp://mqtt:1883")
	t.Setenv("TEST_MQTT_USER", "mqttuser")
	t.Setenv("TEST_MQTT_PASS", "mqttpass")
	t.Setenv("TEST_SECRET", "hex-secret-abc")
	t.Setenv("TEST_PD_TOKEN", "pd-tok-999")
	t.Setenv("TEST_DD_API", "dd-api-111")
	t.Setenv("TEST_DD_APP", "dd-app-222")

	configJSON := map[string]interface{}{
		"port":     18789,
		"data_dir": "~/conduit-data",
		"database": map[string]interface{}{"path": "~/db/gateway.db"},
		"ai": map[string]interface{}{
			"default_provider": "anthropic",
			"providers": []interface{}{
				map[string]interface{}{
					"name":    "anthropic",
					"type":    "anthropic",
					"api_key": "${TEST_API_KEY}",
					"base_url": "${TEST_API_KEY}", // reuse to test base_url expansion
					"model":   "claude-3-5-sonnet-20241022",
					"auth": map[string]interface{}{
						"type":          "oauth",
						"oauth_token":   "${TEST_OAUTH}",
						"refresh_token": "${TEST_OAUTH}",
						"client_id":     "${TEST_CLIENT}",
						"client_secret": "${TEST_CLIENT}",
					},
				},
			},
		},
		"tools": map[string]interface{}{
			"enabled_tools":  []interface{}{"read_file"},
			"max_tool_chains": 25,
			"sandbox": map[string]interface{}{
				"workspace_dir": "./workspace",
				"allowed_paths": []interface{}{"./workspace"},
			},
			"services": map[string]interface{}{
				"brave": map[string]interface{}{"api_key": "${TEST_BRAVE_KEY}"},
			},
		},
		"channels": []interface{}{
			map[string]interface{}{
				"name": "telegram", "type": "telegram", "enabled": true,
				"config": map[string]interface{}{"bot_token": "${TEST_BOT_TOKEN}"},
			},
		},
		"mqtt": map[string]interface{}{
			"enabled":    false,
			"broker_url": "${TEST_MQTT_BROKER}",
			"username":   "${TEST_MQTT_USER}",
			"password":   "${TEST_MQTT_PASS}",
		},
		"pagerduty": map[string]interface{}{
			"enabled":   false,
			"api_token": "${TEST_PD_TOKEN}",
		},
		"datadog": map[string]interface{}{
			"enabled": false,
			"api_key": "${TEST_DD_API}",
			"app_key": "${TEST_DD_APP}",
		},
		"auth": map[string]interface{}{"token_secret": "${TEST_SECRET}"},
		"ssh": map[string]interface{}{
			"host_key_path":        "~/ssh/host_key",
			"authorized_keys_path": "~/ssh/authorized_keys",
		},
		"remote_ssh": map[string]interface{}{
			"enabled": false,
			"audit":   map[string]interface{}{"log_path": "~/logs/ssh_audit.jsonl"},
			"pool":    map[string]interface{}{"known_hosts_file": "~/.ssh/known_hosts"},
			"defaults": map[string]interface{}{"identity_file": "~/.ssh/id_rsa"},
			"hosts": []interface{}{
				map[string]interface{}{
					"name": "web-1", "hostname": "192.168.1.1",
					"identity_file": "~/.ssh/web_key",
				},
			},
		},
	}

	data, err := json.Marshal(configJSON)
	require.NoError(t, err)

	tmpFile := filepath.Join(t.TempDir(), "test_config.json")
	require.NoError(t, os.WriteFile(tmpFile, data, 0644))

	cfg, err := Load(tmpFile)
	require.NoError(t, err)

	home, _ := os.UserHomeDir()

	// Env expansion checks
	assert.Equal(t, "sk-ant-xxx", cfg.AI.Providers[0].APIKey, "Provider APIKey")
	assert.Equal(t, "sk-ant-xxx", cfg.AI.Providers[0].BaseURL, "Provider BaseURL")
	assert.Equal(t, "oauth-tok-123", cfg.AI.Providers[0].Auth.OAuthToken, "Auth OAuthToken")
	assert.Equal(t, "oauth-tok-123", cfg.AI.Providers[0].Auth.RefreshToken, "Auth RefreshToken")
	assert.Equal(t, "client-id-456", cfg.AI.Providers[0].Auth.ClientID, "Auth ClientID")
	assert.Equal(t, "client-id-456", cfg.AI.Providers[0].Auth.ClientSecret, "Auth ClientSecret")
	assert.Equal(t, "tcp://mqtt:1883", cfg.MQTT.BrokerURL, "MQTT BrokerURL")
	assert.Equal(t, "mqttuser", cfg.MQTT.Username, "MQTT Username")
	assert.Equal(t, "mqttpass", cfg.MQTT.Password, "MQTT Password")
	assert.Equal(t, "pd-tok-999", cfg.PagerDuty.APIToken, "PagerDuty APIToken")
	assert.Equal(t, "dd-api-111", cfg.Datadog.APIKey, "Datadog APIKey")
	assert.Equal(t, "dd-app-222", cfg.Datadog.AppKey, "Datadog AppKey")
	assert.Equal(t, "hex-secret-abc", cfg.Auth.TokenSecret, "Auth TokenSecret")

	// Map expansion checks (manual expandEnvMaps)
	assert.Equal(t, "bot:telegram-tok", cfg.Channels[0].Config["bot_token"], "Channel bot_token")
	assert.Equal(t, "brave-key-789", cfg.Tools.Services["brave"]["api_key"], "Services brave api_key")

	// Tilde expansion checks
	assert.Equal(t, home+"/conduit-data", cfg.DataDir, "DataDir tilde")
	assert.Equal(t, home+"/db/gateway.db", cfg.Database.Path, "Database.Path tilde")
	assert.Equal(t, home+"/ssh/host_key", cfg.SSH.HostKeyPath, "SSH HostKeyPath tilde")
	assert.Equal(t, home+"/ssh/authorized_keys", cfg.SSH.AuthorizedKeysPath, "SSH AuthKeysPath tilde")
	assert.Equal(t, home+"/logs/ssh_audit.jsonl", cfg.RemoteSSH.Audit.LogPath, "RemoteSSH Audit LogPath tilde")
	assert.Equal(t, home+"/.ssh/known_hosts", cfg.RemoteSSH.Pool.KnownHostsFile, "RemoteSSH Pool KnownHostsFile tilde")
	assert.Equal(t, home+"/.ssh/id_rsa", cfg.RemoteSSH.Defaults.IdentityFile, "RemoteSSH Defaults IdentityFile tilde")
	assert.Equal(t, home+"/.ssh/web_key", cfg.RemoteSSH.Hosts[0].IdentityFile, "RemoteSSH Host IdentityFile tilde")
}
