package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.5: literal '$' in secrets must survive env expansion; only
// ${NAME} is expanded.
func TestExpandEnvBraced(t *testing.T) {
	t.Setenv("CONDUIT_T_KEY", "sk-123")
	t.Setenv("word1", "SHOULD-NOT-APPEAR")
	cases := map[string]string{
		"p@ss$word1":              "p@ss$word1",
		"a$$b":                    "a$$b",
		"$":                       "$",
		"trailing$":               "trailing$",
		"${CONDUIT_T_KEY}":        "sk-123",
		"pre-${CONDUIT_T_KEY}-x":  "pre-sk-123-x",
		"${CONDUIT_T_UNSET_X}":    "",
		"$CONDUIT_T_KEY":          "$CONDUIT_T_KEY",   // bare form is NOT expanded
		"$${CONDUIT_T_KEY}":       "${CONDUIT_T_KEY}", // escape for a literal ${...}
		"${not valid}":            "${not valid}",
		"${}":                     "${}",
		"${CONDUIT_T_UNSET_X:-d}": "d",
		"${CONDUIT_T_KEY:-d}":     "sk-123",
		"$${CONDUIT_T_KEY:-d}":    "${CONDUIT_T_KEY:-d}",
	}
	for in, want := range cases {
		assert.Equal(t, want, expandEnvBraced(in), "input %q", in)
	}
}

func TestExpandEnvTagged_PreservesDollar(t *testing.T) {
	t.Setenv("CONDUIT_T_KEY", "sk-123")
	cfg := &Config{
		AI:   AIConfig{Providers: []ProviderConfig{{APIKey: "p@ss$word1"}}},
		Auth: AuthTokenConfig{TokenSecret: "${CONDUIT_T_KEY}"},
	}
	cfg.expandEnvTagged()
	assert.Equal(t, "p@ss$word1", cfg.AI.Providers[0].APIKey)
	assert.Equal(t, "sk-123", cfg.Auth.TokenSecret)
}

// conduit-31jg.5: expandEnvMaps recurses into nested maps and slices and
// leaves '$' intact.
func TestExpandEnvMaps_NestedAndDollar(t *testing.T) {
	t.Setenv("CONDUIT_T_KEY", "sk-123")
	cfg := &Config{
		Channels: []ChannelConfig{{Config: map[string]interface{}{
			"password": "a$$b",
			"nested": map[string]interface{}{
				"token":  "${CONDUIT_T_KEY}",
				"deeper": map[string]interface{}{"k": "${CONDUIT_T_KEY}"},
			},
			"list": []interface{}{"${CONDUIT_T_KEY}", "x$y", map[string]interface{}{"k": "${CONDUIT_T_KEY}"}},
		}}},
		Tools: ToolsConfig{Services: map[string]map[string]interface{}{
			"svc": {"auth": map[string]interface{}{"key": "${CONDUIT_T_KEY}"}, "pw": "p@ss$word1"},
		}},
	}
	cfg.expandEnvMaps()

	c := cfg.Channels[0].Config
	assert.Equal(t, "a$$b", c["password"])
	nested := c["nested"].(map[string]interface{})
	assert.Equal(t, "sk-123", nested["token"])
	assert.Equal(t, "sk-123", nested["deeper"].(map[string]interface{})["k"])
	list := c["list"].([]interface{})
	assert.Equal(t, "sk-123", list[0])
	assert.Equal(t, "x$y", list[1])
	assert.Equal(t, "sk-123", list[2].(map[string]interface{})["k"])
	svc := cfg.Tools.Services["svc"]
	assert.Equal(t, "sk-123", svc["auth"].(map[string]interface{})["key"])
	assert.Equal(t, "p@ss$word1", svc["pw"])
}

// End-to-end through Load: a secret containing '$' written literally in
// config.json survives, and ${VAR} still expands.
func TestLoad_SecretWithDollarSurvives(t *testing.T) {
	t.Setenv("CONDUIT_T_BOT", "bot-xyz")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{
  "port": 18789,
  "tools": {"max_tool_chains": 5, "enabled_tools": ["read"]},
  "auth": {"token_secret": "s3cr$et$$x"},
  "channels": [{"name": "telegram", "type": "telegram", "enabled": false,
    "config": {"bot_token": "${CONDUIT_T_BOT}", "extra": {"pw": "p@ss$word1"}}}]
}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "s3cr$et$$x", cfg.Auth.TokenSecret)
	assert.Equal(t, "bot-xyz", cfg.Channels[0].Config["bot_token"])
	assert.Equal(t, "p@ss$word1", cfg.Channels[0].Config["extra"].(map[string]interface{})["pw"])
}
