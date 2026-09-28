package gateway

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools"
)

// normalizedToolDefs marshals the model-facing tool definitions with each
// tool's "Action details" lines sorted, so two conversions can be compared
// byte-for-byte (GetActionDocs is a map, so those lines come out in random
// order per conversion).
func normalizedToolDefs(t *testing.T, defs []ai.Tool) string {
	t.Helper()
	const marker = "\n\nAction details:\n"
	out := make([]ai.Tool, len(defs))
	copy(out, defs)
	for i := range out {
		d := out[i].Description
		if k := strings.Index(d, marker); k >= 0 {
			lines := strings.Split(d[k+len(marker):], "\n")
			sort.Strings(lines)
			out[i].Description = d[:k+len(marker)] + strings.Join(lines, "\n")
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal tool defs: %v", err)
	}
	return string(b)
}

// TestToolSchemaBuilder_SingleConstructionPath covers conduit-5y17: the
// gateway no longer builds a SchemaBuilder of its own; Registry.SetServices
// builds the only one (with channel + workspace discovery).
func TestToolSchemaBuilder_SingleConstructionPath(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.search = &SearchService{}
	ws := t.TempDir()
	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{ContextDir: ws},
		Tools: config.ToolsConfig{
			EnabledTools: []string{"Gateway", "Message", "ReadFile", "WriteFile", "ListFiles", "Cron"},
			Sandbox:      config.SandboxConfig{WorkspaceDir: ws, AllowedPaths: []string{ws}},
		},
	}

	services := gw.buildToolServices(cfg, store, nil, nil, nil)
	if services.SchemaBuilder != nil {
		t.Fatal("buildToolServices must leave SchemaBuilder nil; Registry.SetServices owns its construction")
	}

	reg := tools.NewRegistry(cfg.Tools)
	reg.SetServices(services)
	if reg.GetServices().SchemaBuilder == nil {
		t.Fatal("Registry.SetServices should build the discovery-backed SchemaBuilder")
	}
}

// TestConvertToolsToAIFormat_IndependentOfSchemaBuilder pins the model path
// to static SchemaHints: the tool definitions sent to the provider must be
// identical whether or not a discovery-backed SchemaBuilder is present, so
// runtime discovery data (channel status, workspace paths) can never leak
// into — and churn — the prompt-cached tools block.
func TestConvertToolsToAIFormat_IndependentOfSchemaBuilder(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.search = &SearchService{}
	ws := t.TempDir()
	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{ContextDir: ws},
		Tools: config.ToolsConfig{
			EnabledTools: []string{
				"ReadFile", "WriteFile", "ListFiles", "Edit", "Exec", "Message",
				"Gateway", "Cron", "SessionsSpawn", "Image", "WebFetch",
			},
			Sandbox: config.SandboxConfig{WorkspaceDir: ws, AllowedPaths: []string{ws}},
		},
	}
	reg := tools.NewRegistry(cfg.Tools)
	reg.SetServices(gw.buildToolServices(cfg, store, nil, nil, nil))

	withDiscovery := convertToolsToAIFormat(reg)
	if len(withDiscovery) < 5 {
		t.Fatalf("expected several enabled tools, got %d", len(withDiscovery))
	}

	reg.GetServices().SchemaBuilder = nil
	withoutDiscovery := convertToolsToAIFormat(reg)

	if a, b := normalizedToolDefs(t, withDiscovery), normalizedToolDefs(t, withoutDiscovery); a != b {
		t.Fatalf("model-facing tool definitions depend on the discovery SchemaBuilder:\nwith:    %s\nwithout: %s", a, b)
	}
}
