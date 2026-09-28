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

// marshalToolDefs marshals the model-facing tool definitions verbatim — no
// normalization — since convertToolsToAIFormat must itself be deterministic
// for the prompt-cached tools prefix to stay stable (conduit-oc3u).
func marshalToolDefs(t *testing.T, defs []ai.Tool) string {
	t.Helper()
	b, err := json.Marshal(defs)
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

	if a, b := marshalToolDefs(t, withDiscovery), marshalToolDefs(t, withoutDiscovery); a != b {
		t.Fatalf("model-facing tool definitions depend on the discovery SchemaBuilder:\nwith:    %s\nwithout: %s", a, b)
	}
}

// TestConvertToolsToAIFormat_Deterministic covers conduit-oc3u: repeated
// conversions must yield byte-identical tool definitions, including the
// "Action details" lines built from GetActionDocs (a map, whose iteration
// order Go randomizes per range — the Gateway tool has 10+ actions, so an
// unsorted build reorders them across 20 conversions with near certainty).
func TestConvertToolsToAIFormat_Deterministic(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.search = &SearchService{}
	ws := t.TempDir()
	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{ContextDir: ws},
		Tools: config.ToolsConfig{
			EnabledTools: []string{"Gateway", "Cron", "Message", "ReadFile"},
			Sandbox:      config.SandboxConfig{WorkspaceDir: ws, AllowedPaths: []string{ws}},
		},
	}
	reg := tools.NewRegistry(cfg.Tools)
	reg.SetServices(gw.buildToolServices(cfg, store, nil, nil, nil))

	defs := convertToolsToAIFormat(reg)
	first := marshalToolDefs(t, defs)

	var gwDesc string
	for _, d := range defs {
		if d.Name == "Gateway" {
			gwDesc = d.Description
		}
	}
	const marker = "\n\nAction details:\n"
	k := strings.Index(gwDesc, marker)
	if k < 0 {
		t.Fatalf("Gateway tool description has no action details: %q", gwDesc)
	}
	lines := strings.Split(gwDesc[k+len(marker):], "\n")
	if len(lines) < 8 {
		t.Fatalf("expected many Gateway action lines, got %d", len(lines))
	}
	if !sort.StringsAreSorted(lines) {
		t.Fatalf("Gateway action details not sorted:\n%s", strings.Join(lines, "\n"))
	}

	for n := 0; n < 20; n++ {
		if got := marshalToolDefs(t, convertToolsToAIFormat(reg)); got != first {
			t.Fatalf("conversion %d differs from the first:\nfirst: %s\ngot:   %s", n+1, first, got)
		}
	}
}
