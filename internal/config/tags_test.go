package config

import (
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWalkStringFields_FlatStruct(t *testing.T) {
	type flat struct {
		A string `cfg:"env"`
		B string `cfg:"path"`
		C string
		D int
	}
	s := flat{A: "hello", B: "world", C: "skip", D: 42}
	var visited []string
	walkStringFields(reflect.ValueOf(&s).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		visited = append(visited, field.String())
	})
	assert.Equal(t, []string{"hello", "world", "skip"}, visited)
}

func TestWalkStringFields_NestedStruct(t *testing.T) {
	type inner struct {
		X string `cfg:"env"`
	}
	type outer struct {
		Name  string `cfg:"env"`
		Inner inner
	}
	s := outer{Name: "a", Inner: inner{X: "b"}}
	var visited []string
	walkStringFields(reflect.ValueOf(&s).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		if hasCfgFlag(tags, "env") {
			visited = append(visited, field.String())
		}
	})
	assert.Equal(t, []string{"a", "b"}, visited)
}

func TestWalkStringFields_PointerToStruct(t *testing.T) {
	type inner struct {
		Key string `cfg:"env"`
	}
	type outer struct {
		Inner *inner
	}

	// Non-nil pointer
	s := outer{Inner: &inner{Key: "val"}}
	var visited []string
	walkStringFields(reflect.ValueOf(&s).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		if hasCfgFlag(tags, "env") {
			visited = append(visited, field.String())
		}
	})
	assert.Equal(t, []string{"val"}, visited)

	// Nil pointer — should not panic
	s2 := outer{Inner: nil}
	visited = nil
	walkStringFields(reflect.ValueOf(&s2).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		visited = append(visited, field.String())
	})
	assert.Empty(t, visited)
}

func TestWalkStringFields_SliceOfStructs(t *testing.T) {
	type item struct {
		Name string `cfg:"env"`
	}
	type container struct {
		Items []item
	}
	s := container{Items: []item{{Name: "a"}, {Name: "b"}}}
	var visited []string
	walkStringFields(reflect.ValueOf(&s).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		if hasCfgFlag(tags, "env") {
			visited = append(visited, field.String())
		}
	})
	assert.Equal(t, []string{"a", "b"}, visited)
}

func TestWalkStringFields_EmptySlice(t *testing.T) {
	type item struct {
		Name string `cfg:"env"`
	}
	type container struct {
		Items []item
	}
	s := container{Items: []item{}}
	var visited []string
	walkStringFields(reflect.ValueOf(&s).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		visited = append(visited, field.String())
	})
	assert.Empty(t, visited)
}

func TestHasCfgFlag(t *testing.T) {
	tests := []struct {
		tag  string
		flag string
		want bool
	}{
		{`cfg:"env"`, "env", true},
		{`cfg:"path"`, "path", true},
		{`cfg:"env,path"`, "env", true},
		{`cfg:"env,path"`, "path", true},
		{`cfg:"env"`, "path", false},
		{`cfg:""`, "env", false},
		{`json:"foo"`, "env", false},
		{``, "env", false},
	}
	for _, tt := range tests {
		tags := reflect.StructTag(tt.tag)
		got := hasCfgFlag(tags, tt.flag)
		assert.Equal(t, tt.want, got, "tag=%q flag=%q", tt.tag, tt.flag)
	}
}

func TestExpandTildeTagged(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cfg := &Config{
		DataDir:     "~/data",
		SecretsFile: "~/secrets.env",
		Database:    DatabaseConfig{Path: "~/db/gateway.db"},
		SSH: SSHServerConfig{
			HostKeyPath:        "~/ssh/host_key",
			AuthorizedKeysPath: "~/ssh/authorized_keys",
		},
	}
	cfg.expandTildeTagged()

	assert.Equal(t, home+"/data", cfg.DataDir)
	assert.Equal(t, home+"/secrets.env", cfg.SecretsFile)
	assert.Equal(t, home+"/db/gateway.db", cfg.Database.Path)
	assert.Equal(t, home+"/ssh/host_key", cfg.SSH.HostKeyPath)
	assert.Equal(t, home+"/ssh/authorized_keys", cfg.SSH.AuthorizedKeysPath)
}

func TestExpandTildeTagged_NoTilde(t *testing.T) {
	cfg := &Config{
		DataDir:  "/absolute/path",
		Database: DatabaseConfig{Path: "relative.db"},
	}
	cfg.expandTildeTagged()
	assert.Equal(t, "/absolute/path", cfg.DataDir)
	assert.Equal(t, "relative.db", cfg.Database.Path)
}

func TestExpandEnvTagged(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-secret123")
	t.Setenv("TEST_BROKER", "tcp://localhost:1883")

	cfg := &Config{
		AI: AIConfig{
			Providers: []ProviderConfig{
				{APIKey: "${TEST_API_KEY}"},
			},
		},
		MQTT: MQTTConfig{
			BrokerURL: "${TEST_BROKER}",
		},
		Auth: AuthTokenConfig{
			TokenSecret: "${TEST_API_KEY}",
		},
	}
	cfg.expandEnvTagged()

	assert.Equal(t, "sk-secret123", cfg.AI.Providers[0].APIKey)
	assert.Equal(t, "tcp://localhost:1883", cfg.MQTT.BrokerURL)
	assert.Equal(t, "sk-secret123", cfg.Auth.TokenSecret)
}

func TestExpandEnvTagged_NilPointer(t *testing.T) {
	// Auth is nil — should not panic
	cfg := &Config{
		AI: AIConfig{
			Providers: []ProviderConfig{
				{Auth: nil},
			},
		},
	}
	cfg.expandEnvTagged() // should not panic
}

func TestExpandEnvMaps(t *testing.T) {
	t.Setenv("TEST_TOKEN", "bot-token-123")

	cfg := &Config{
		Channels: []ChannelConfig{
			{Config: map[string]interface{}{
				"bot_token": "${TEST_TOKEN}",
				"number":    42, // non-string values are left alone
			}},
		},
		Tools: ToolsConfig{
			Services: map[string]map[string]interface{}{
				"tts": {"api_key": "${TEST_TOKEN}", "rate": 100},
			},
		},
	}
	cfg.expandEnvMaps()

	assert.Equal(t, "bot-token-123", cfg.Channels[0].Config["bot_token"])
	assert.Equal(t, 42, cfg.Channels[0].Config["number"])
	assert.Equal(t, "bot-token-123", cfg.Tools.Services["tts"]["api_key"])
	assert.Equal(t, 100, cfg.Tools.Services["tts"]["rate"])
}

func TestValidateEnumTags_Valid(t *testing.T) {
	type s struct {
		Tier string `validate:"enum=read|modify|dangerous"`
	}
	assert.NoError(t, validateEnumTags(&s{Tier: "read"}))
	assert.NoError(t, validateEnumTags(&s{Tier: "modify"}))
	assert.NoError(t, validateEnumTags(&s{Tier: "dangerous"}))
}

func TestValidateEnumTags_Invalid(t *testing.T) {
	type s struct {
		Tier string `validate:"enum=read|modify|dangerous"`
	}
	err := validateEnumTags(&s{Tier: "invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid value "invalid"`)
	assert.Contains(t, err.Error(), "Tier")
}

func TestValidateEnumTags_EmptyIsOK(t *testing.T) {
	type s struct {
		Tier string `validate:"enum=read|modify|dangerous"`
	}
	assert.NoError(t, validateEnumTags(&s{Tier: ""}))
}

func TestValidateEnumTags_NestedSlice(t *testing.T) {
	type item struct {
		Level string `validate:"enum=low|high"`
	}
	type container struct {
		Items []item
	}
	assert.NoError(t, validateEnumTags(&container{
		Items: []item{{Level: "low"}, {Level: "high"}},
	}))
	err := validateEnumTags(&container{
		Items: []item{{Level: "low"}, {Level: "bad"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Items[1].Level")
}

func TestValidateEnumTags_NilPointer(t *testing.T) {
	type inner struct {
		Mode string `validate:"enum=a|b"`
	}
	type outer struct {
		Inner *inner
	}
	// Nil pointer should not error
	assert.NoError(t, validateEnumTags(&outer{Inner: nil}))
	// Non-nil with valid value
	assert.NoError(t, validateEnumTags(&outer{Inner: &inner{Mode: "a"}}))
	// Non-nil with invalid value
	err := validateEnumTags(&outer{Inner: &inner{Mode: "c"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"c"`)
}
