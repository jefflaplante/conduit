package config

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fillSentinels sets every string reachable from v (struct fields, pointer
// targets, one element of each slice, one entry of each string-valued map)
// to "SENTINEL<path>SENTINEL" and returns the sentinel for every path.
func fillSentinels(t *testing.T, v reflect.Value, path string, out map[string]string) {
	t.Helper()
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillSentinels(t, v.Elem(), path, out)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fillSentinels(t, v.Field(i), path+"."+name, out)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillSentinels(t, s.Index(0), path+"[]", out)
		v.Set(s)
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return
		}
		m := reflect.MakeMap(v.Type())
		elem := reflect.New(v.Type().Elem()).Elem()
		if elem.Kind() == reflect.Interface {
			return // free-form maps are covered by TestRedacted_FreeFormMaps
		}
		fillSentinels(t, elem, path+"{}", out)
		m.SetMapIndex(reflect.ValueOf("k"), elem)
		v.Set(m)
	case reflect.String:
		s := "SENTINEL" + path + "SENTINEL"
		v.SetString(s)
		out[path] = s
	}
}

func survivingSentinels(t *testing.T, v interface{}, sentinels map[string]string) []string {
	t.Helper()
	red, err := Redacted(v)
	if err != nil {
		t.Fatalf("Redacted: %v", err)
	}
	raw, err := json.Marshal(red)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var survived []string
	for path, s := range sentinels {
		if strings.Contains(string(raw), s) {
			survived = append(survived, path)
		}
	}
	sort.Strings(survived)
	return survived
}

// gatewayConfigSubtree mirrors what Gateway.GetConfiguration returns.
type gatewayConfigSubtree struct {
	AI        AIConfig        `json:"ai"`
	Workspace WorkspaceConfig `json:"workspace"`
}

// reviewedNonSecretFields lists every string field reachable from the config
// subtrees the Gateway tool returns (ai, workspace) that was reviewed and is
// NOT a secret. Adding a string field to AIConfig / WorkspaceConfig (or a
// type reachable from them) fails TestRedacted_GatewaySubtreeComplete until
// it either has a JSON name IsSecretKey redacts, or is added here after
// confirming it never holds a credential. conduit-31jg.56
var reviewedNonSecretFields = map[string]bool{
	".ai.compaction.model":                              true,
	".ai.default_provider":                              true,
	".ai.model_aliases{}":                               true,
	".ai.providers[].auth.client_id":                    true, // OAuth client IDs are public
	".ai.providers[].auth.type":                         true,
	".ai.providers[].base_url":                          true, // URL passwords still redacted (TestRedacted_URLPassword)
	".ai.providers[].claude_code.allowed_tools[]":       true,
	".ai.providers[].claude_code.claude_path":           true,
	".ai.providers[].claude_code.permission_mode":       true,
	".ai.providers[].claude_code.working_dir":           true,
	".ai.providers[].fallback_model":                    true,
	".ai.providers[].model":                             true,
	".ai.providers[].name":                              true,
	".ai.providers[].thinking.type":                     true,
	".ai.providers[].type":                              true,
	".ai.subagent_default_model":                        true,
	".workspace.context_dir":                            true,
	".workspace.files.core[]":                           true,
	".workspace.security.default_policy":                true,
	".workspace.summary.cache_dir":                      true,
	".workspace.summary.file_configs{}.preserve_keys[]": true,
	".workspace.summary.model":                          true,
}

// Every string field in the returned subtree is either redacted or reviewed.
func TestRedacted_GatewaySubtreeComplete(t *testing.T) {
	var sub gatewayConfigSubtree
	sentinels := map[string]string{}
	fillSentinels(t, reflect.ValueOf(&sub).Elem(), "", sentinels)

	for _, path := range survivingSentinels(t, sub, sentinels) {
		if !reviewedNonSecretFields[path] {
			t.Errorf("config field %s is returned to the model unredacted and is not in reviewedNonSecretFields; "+
				"if it can hold a credential give it a secret JSON name (see IsSecretKey), otherwise review and add it", path)
		}
	}
	// The known credential fields must be among the redacted ones.
	for _, path := range []string{
		".ai.providers[].api_key",
		".ai.providers[].auth.oauth_token",
		".ai.providers[].auth.refresh_token",
		".ai.providers[].auth.client_secret",
	} {
		if _, ok := sentinels[path]; !ok {
			t.Errorf("expected secret field %s to exist in the config (renamed?)", path)
		}
		if reviewedNonSecretFields[path] {
			t.Errorf("%s must not be on the non-secret list", path)
		}
	}
}

// Every credential field across the WHOLE Config is caught by the key-name
// rule, so any other surface that redacts the full config is covered too.
func TestRedacted_WholeConfigSecretsCaught(t *testing.T) {
	var cfg Config
	sentinels := map[string]string{}
	fillSentinels(t, reflect.ValueOf(&cfg).Elem(), "", sentinels)
	survived := map[string]bool{}
	for _, p := range survivingSentinels(t, cfg, sentinels) {
		survived[p] = true
	}
	secretLeaf := regexp.MustCompile(`\.(api_key|app_key|api_token|token_secret|password|client_secret|oauth_token|refresh_token|client_key)$`)
	checked := 0
	for path := range sentinels {
		if secretLeaf.MatchString(path) {
			checked++
			if survived[path] {
				t.Errorf("secret config field %s survived redaction", path)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d secret fields found in Config; the walker is not reaching them", checked)
	}
}

func TestRedacted_FreeFormMaps(t *testing.T) {
	in := map[string]interface{}{
		"channels": []interface{}{
			map[string]interface{}{"type": "telegram", "config": map[string]interface{}{
				"bot_token": "SENTINEL1", "chat_id": "123",
				"nested": map[string]interface{}{"webhook_secret": "SENTINEL2", "Authorization": "Bearer SENTINEL3"},
			}},
		},
		"services": map[string]interface{}{
			"brave":  map[string]interface{}{"api_key": "SENTINEL4"},
			"google": map[string]interface{}{"credentials": map[string]interface{}{"x": "SENTINEL5"}},
		},
		"max_tokens":     4000,
		"redact_secrets": true,
		"api_key":        "",
	}
	red, err := Redacted(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(red)
	for _, s := range []string{"SENTINEL1", "SENTINEL2", "SENTINEL3", "SENTINEL4", "SENTINEL5"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("%s leaked: %s", s, raw)
		}
	}
	m := red.(map[string]interface{})
	if m["max_tokens"] != float64(4000) || m["redact_secrets"] != true || m["api_key"] != "" {
		t.Errorf("non-secret scalars / empty values must be preserved: %s", raw)
	}
	if !strings.Contains(string(raw), `"chat_id":"123"`) {
		t.Errorf("non-secret value dropped: %s", raw)
	}
	// The input is not mutated.
	if in["services"].(map[string]interface{})["brave"].(map[string]interface{})["api_key"] != "SENTINEL4" {
		t.Error("Redacted mutated its input")
	}
}

func TestRedacted_URLPassword(t *testing.T) {
	red, err := Redacted(ProviderConfig{BaseURL: "https://bob:SENTINEL@proxy.example/v1", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(red)
	if strings.Contains(string(raw), "SENTINEL") {
		t.Errorf("URL password leaked: %s", raw)
	}
	if !strings.Contains(string(raw), "bob:xxxxx@proxy.example") {
		t.Errorf("URL otherwise preserved: %s", raw)
	}
}

func TestIsSecretKey(t *testing.T) {
	for _, k := range []string{"api_key", "apiKey", "APIKey", "oauth_token", "refresh_token", "token", "bot_token",
		"client_secret", "token_secret", "password", "app_key", "api_token", "private_key", "client_key", "credentials"} {
		if !IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = false", k)
		}
	}
	for _, k := range []string{"max_tokens", "budget_tokens", "model", "base_url", "client_id", "type",
		"preserve_keys", "strict_host_key_checking", "default_provider"} {
		if IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = true", k)
		}
	}
}
