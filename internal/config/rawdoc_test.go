package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rawDocSample = `{
    "port": 18789,
    "future_key": {"keep": [1, 2, 3], "nested": {"x": "<y>"}},
    "ai": {
        "default_provider": "z-ai",
        "providers": [
            {
                "name": "anthropic",
                "type": "anthropic",
                "api_key": "${ANTHROPIC_API_KEY}",
                "model": "claude-sonnet-4-6"
            },
            {
                "name": "z-ai",
                "type": "openai",
                "api_key": "${ZAI_API_KEY}",
                "model": "glm-5.3",
                "timeout_seconds": 300
            }
        ],
        "smart_routing": {"enabled": false}
    }
}
`

func TestDocument_RoundTripIsByteIdentical(t *testing.T) {
	doc, err := ParseDocument([]byte(rawDocSample))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Bytes()); got != rawDocSample {
		t.Fatalf("round trip changed the document:\n%s", got)
	}
}

func TestDocument_SetKeepsEverythingElse(t *testing.T) {
	doc, err := ParseDocument([]byte(rawDocSample))
	if err != nil {
		t.Fatal(err)
	}
	canonical, changed, err := doc.Set([]string{"ai", "providers", "z-ai", "timeout_seconds"}, 600.0, false)
	if err != nil || !changed {
		t.Fatalf("Set: changed=%v err=%v", changed, err)
	}
	if JoinPath(canonical) != "ai.providers.z-ai.timeout_seconds" {
		t.Errorf("canonical = %v", canonical)
	}
	out := string(doc.Bytes())

	// Only the edited line differs.
	want := strings.Replace(rawDocSample, `"timeout_seconds": 300`, `"timeout_seconds": 600`, 1)
	if out != want {
		t.Fatalf("unexpected output:\n%s", out)
	}
	// Placeholders, unknown keys and deprecated keys survive verbatim.
	for _, s := range []string{`"${ZAI_API_KEY}"`, `"${ANTHROPIC_API_KEY}"`, `"future_key": {"keep": [1, 2, 3], "nested": {"x": "<y>"}}`, `"smart_routing": {"enabled": false}`} {
		if !strings.Contains(out, s) {
			t.Errorf("output lost %s", s)
		}
	}
}

func TestDocument_SetByIndexCreateDeleteNoop(t *testing.T) {
	doc, err := ParseDocument([]byte(rawDocSample))
	if err != nil {
		t.Fatal(err)
	}
	// Index addressing resolves to the element's name.
	canonical, changed, err := doc.Set([]string{"ai", "providers", "0", "model"}, "claude-opus-4-6", false)
	if err != nil || !changed || JoinPath(canonical) != "ai.providers.anthropic.model" {
		t.Fatalf("index set: %v %v %v", canonical, changed, err)
	}
	// Same value again: no change.
	if _, changed, err := doc.Set([]string{"ai", "providers", "anthropic", "model"}, "claude-opus-4-6", false); err != nil || changed {
		t.Fatalf("no-op set reported changed=%v err=%v", changed, err)
	}
	// Missing intermediate objects are created.
	if _, changed, err := doc.Set([]string{"ai", "pricing_overrides", "glm-5.3", "input_per_m_token"}, 1.5, false); err != nil || !changed {
		t.Fatalf("create: %v %v", changed, err)
	}
	// Delete.
	if _, changed, err := doc.Set([]string{"ai", "smart_routing"}, nil, true); err != nil || !changed {
		t.Fatalf("delete: %v %v", changed, err)
	}
	// Deleting a missing key is a no-op.
	if _, changed, err := doc.Set([]string{"ai", "nope"}, nil, true); err != nil || changed {
		t.Fatalf("delete missing: %v %v", changed, err)
	}
	// Unknown array element is an error.
	if _, _, err := doc.Set([]string{"ai", "providers", "ghost", "model"}, "x", false); err == nil {
		t.Fatal("expected error for unknown provider")
	}
	// Descending into a scalar is an error.
	if _, _, err := doc.Set([]string{"port", "x"}, 1.0, false); err == nil {
		t.Fatal("expected error descending into a scalar")
	}

	var v map[string]interface{}
	if err := json.Unmarshal(doc.Bytes(), &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, doc.Bytes())
	}
	ai := v["ai"].(map[string]interface{})
	if _, ok := ai["smart_routing"]; ok {
		t.Error("smart_routing not deleted")
	}
	po := ai["pricing_overrides"].(map[string]interface{})["glm-5.3"].(map[string]interface{})
	if po["input_per_m_token"] != 1.5 {
		t.Errorf("pricing override = %v", po)
	}
	if !strings.Contains(string(doc.Bytes()), "\n        \"pricing_overrides\": {\n            \"glm-5.3\": {") {
		t.Errorf("new object not rendered with the file's indent:\n%s", doc.Bytes())
	}
}

func TestDocument_RejectsBadInput(t *testing.T) {
	for _, in := range []string{``, `[]`, `{"a":}`, `{"a":1,}`, `{"a":1} x`, `{"a":"x`, `{"a":tru}`} {
		if _, err := ParseDocument([]byte(in)); err == nil {
			t.Errorf("ParseDocument(%q) = nil error", in)
		}
	}
}

func TestFlattenPatch(t *testing.T) {
	ops, err := FlattenPatch(map[string]interface{}{
		"ai.providers.z-ai.timeout_seconds": 600.0,
		"ai": map[string]interface{}{
			"pricing_overrides": map[string]interface{}{"glm-5.3": map[string]interface{}{"input_per_m_token": 1.0}},
			"call_log":          nil,
		},
		"tools.sandbox.allowed_paths": []interface{}{"/tmp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range ops {
		s := JoinPath(op.Path)
		if op.Delete {
			s += "=null"
		}
		got = append(got, s)
	}
	want := "ai.call_log=null|ai.pricing_overrides.glm-5.3.input_per_m_token|ai.providers.z-ai.timeout_seconds|tools.sandbox.allowed_paths"
	if strings.Join(got, "|") != want {
		t.Fatalf("ops = %v", got)
	}

	if _, err := FlattenPatch(map[string]interface{}{"ai.call_log": 1.0, "ai": map[string]interface{}{"call_log": map[string]interface{}{"enabled": true}}}); err == nil {
		t.Error("expected conflict error for a key and a key inside it")
	}
	if _, err := FlattenPatch(map[string]interface{}{"ai..x": 1.0}); err == nil {
		t.Error("expected error for an empty segment")
	}
	if _, err := FlattenPatch(map[string]interface{}{}); err == nil {
		t.Error("expected error for an empty patch")
	}
}

func TestCheckPatchSecrets(t *testing.T) {
	ok := []PatchOp{
		{Path: []string{"ai", "providers", "z-ai", "api_key"}, Value: "${ZAI_API_KEY}"},
		{Path: []string{"ai", "providers", "z-ai", "base_url"}, Value: "https://api.z.ai/v1"},
		{Path: []string{"ai", "providers", "z-ai", "base_url"}, Value: "https://u:${PW}@host/v1"},
		{Path: []string{"ai", "providers", "z-ai", "max_concurrent"}, Value: 3.0},
		{Path: []string{"ai", "providers", "z-ai", "api_key"}, Delete: true},
		{Path: []string{"channels", "telegram", "config"}, Value: map[string]interface{}{"bot_token": "${TELEGRAM_BOT_TOKEN}"}},
	}
	for _, op := range ok {
		if err := CheckPatchSecrets(op); err != nil {
			t.Errorf("%v: unexpected error %v", op.Path, err)
		}
	}
	bad := []PatchOp{
		{Path: []string{"ai", "providers", "z-ai", "api_key"}, Value: "sk-live-123"},
		{Path: []string{"ai", "providers", "z-ai", "api_key"}, Value: RedactedValue},
		{Path: []string{"ai", "providers", "z-ai", "model"}, Value: RedactedValue},
		{Path: []string{"ai", "providers", "z-ai", "base_url"}, Value: "https://user:hunter2@host/v1"},
		{Path: []string{"channels", "telegram", "config"}, Value: map[string]interface{}{"bot_token": "123:abc"}},
		{Path: []string{"tools", "services"}, Value: []interface{}{map[string]interface{}{"password": "p"}}},
	}
	for _, op := range bad {
		err := CheckPatchSecrets(op)
		if err == nil {
			t.Errorf("%v=%v: expected rejection", op.Path, op.Value)
			continue
		}
		for _, secret := range []string{"sk-live-123", "hunter2", "123:abc"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error message leaks the secret: %v", err)
			}
		}
	}
}

func TestReplaceFile_AtomicBackupModeAndGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink to the config: the target is replaced, the link kept.
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}

	backup, err := ReplaceFile(link, []byte("new"), []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if backup != path+BackupSuffix {
		t.Errorf("backup = %q", backup)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Errorf("config = %q", b)
	}
	if b, _ := os.ReadFile(backup); string(b) != "old" {
		t.Errorf("backup = %q", b)
	}
	for _, p := range []string{path, backup} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, st.Mode().Perm())
		}
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink was replaced: %v %v", st, err)
	}

	// Stale expectation: refused, file untouched.
	if _, err := ReplaceFile(path, []byte("newer"), []byte("old")); err != ErrConfigChangedOnDisk {
		t.Fatalf("err = %v, want ErrConfigChangedOnDisk", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Errorf("config changed despite the guard: %q", b)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReplaceFile_KeepsExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceFile(path, []byte("b"), nil); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", st.Mode().Perm())
	}
}
