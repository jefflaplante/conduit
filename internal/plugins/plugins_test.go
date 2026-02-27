package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"conduit/internal/tools/types"
)

// --- Mock Plugin for testing ---

type mockTool struct {
	name string
}

func (t *mockTool) Name() string                       { return t.name }
func (t *mockTool) Description() string                { return "mock tool: " + t.name }
func (t *mockTool) Parameters() map[string]interface{} { return map[string]interface{}{} }
func (t *mockTool) Execute(_ context.Context, _ map[string]interface{}) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Content: "executed"}, nil
}

type mockPlugin struct {
	meta        PluginMetadata
	tools       []types.Tool
	initErr     error
	shutdownErr error
	initialized bool
	shutDown    bool
}

func (p *mockPlugin) Metadata() PluginMetadata { return p.meta }
func (p *mockPlugin) Initialize(_ PluginContext) error {
	if p.initErr != nil {
		return p.initErr
	}
	p.initialized = true
	return nil
}
func (p *mockPlugin) Tools() []types.Tool { return p.tools }
func (p *mockPlugin) Shutdown() error {
	if p.shutdownErr != nil {
		return p.shutdownErr
	}
	p.shutDown = true
	return nil
}

func newMockPlugin(name, version string) *mockPlugin {
	return &mockPlugin{
		meta: PluginMetadata{
			Name:         name,
			Version:      version,
			Author:       "test",
			Description:  "A test plugin: " + name,
			Tags:         []string{"test"},
			Capabilities: []string{"testing"},
		},
		tools: []types.Tool{&mockTool{name: name + "-tool"}},
	}
}

func testContext(t *testing.T) PluginContext {
	t.Helper()
	return PluginContext{
		Config:  map[string]interface{}{"key": "value"},
		Logger:  log.New(os.Stderr, "[test] ", log.LstdFlags),
		DataDir: t.TempDir(),
	}
}

// ==================== Plugin Metadata Tests ====================

func TestPluginMetadata_Validate(t *testing.T) {
	tests := []struct {
		name     string
		meta     PluginMetadata
		wantErrs int
	}{
		{
			name: "valid metadata",
			meta: PluginMetadata{
				Name:        "test-plugin",
				Version:     "1.0.0",
				Author:      "author",
				Description: "desc",
			},
			wantErrs: 0,
		},
		{
			name: "valid with all fields",
			meta: PluginMetadata{
				Name:              "test-plugin",
				Version:           "2.1.3-beta",
				Author:            "author",
				Description:       "desc",
				Tags:              []string{"tag1"},
				Capabilities:      []string{"cap1"},
				Dependencies:      []string{"dep1"},
				MinGatewayVersion: "1.0.0",
			},
			wantErrs: 0,
		},
		{
			name:     "empty metadata",
			meta:     PluginMetadata{},
			wantErrs: 4, // name, version, author, description
		},
		{
			name: "invalid version",
			meta: PluginMetadata{
				Name:        "test",
				Version:     "not-semver",
				Author:      "author",
				Description: "desc",
			},
			wantErrs: 1,
		},
		{
			name: "invalid min gateway version",
			meta: PluginMetadata{
				Name:              "test",
				Version:           "1.0.0",
				Author:            "author",
				Description:       "desc",
				MinGatewayVersion: "bad",
			},
			wantErrs: 1,
		},
		{
			name: "whitespace-only name",
			meta: PluginMetadata{
				Name:        "   ",
				Version:     "1.0.0",
				Author:      "author",
				Description: "desc",
			},
			wantErrs: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.meta.Validate()
			if len(errs) != tc.wantErrs {
				t.Errorf("Validate() returned %d errors, want %d: %v", len(errs), tc.wantErrs, errs)
			}
		})
	}
}

// ==================== Plugin Registry Tests ====================

func TestPluginRegistry_Register(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("alpha", "1.0.0")

	if err := reg.Register(p); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	if !p.initialized {
		t.Error("plugin should be initialized after registration")
	}

	if reg.Count() != 1 {
		t.Errorf("Count() = %d, want 1", reg.Count())
	}
}

func TestPluginRegistry_Register_NilPlugin(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	if err := reg.Register(nil); err == nil {
		t.Error("Register(nil) should return error")
	}
}

func TestPluginRegistry_Register_Duplicate(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("alpha", "1.0.0")

	if err := reg.Register(p); err != nil {
		t.Fatalf("first Register() unexpected error: %v", err)
	}

	p2 := newMockPlugin("alpha", "2.0.0")
	if err := reg.Register(p2); err == nil {
		t.Error("Register() should fail for duplicate name")
	}
}

func TestPluginRegistry_Register_InvalidMetadata(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := &mockPlugin{
		meta: PluginMetadata{Name: "", Version: "bad", Author: "", Description: ""},
	}

	if err := reg.Register(p); err == nil {
		t.Error("Register() should fail for invalid metadata")
	}
}

func TestPluginRegistry_Register_InitError(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("fail-init", "1.0.0")
	p.initErr = fmt.Errorf("init failed")

	if err := reg.Register(p); err == nil {
		t.Error("Register() should fail when Initialize returns error")
	}

	if reg.Count() != 0 {
		t.Error("plugin should not be registered after init failure")
	}
}

func TestPluginRegistry_Register_MissingDependency(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("dependent", "1.0.0")
	p.meta.Dependencies = []string{"nonexistent"}

	if err := reg.Register(p); err == nil {
		t.Error("Register() should fail for missing dependency")
	}
}

func TestPluginRegistry_Register_WithDependency(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	dep := newMockPlugin("dependency", "1.0.0")
	if err := reg.Register(dep); err != nil {
		t.Fatalf("Register dependency: %v", err)
	}

	p := newMockPlugin("dependent", "1.0.0")
	p.meta.Dependencies = []string{"dependency"}
	if err := reg.Register(p); err != nil {
		t.Fatalf("Register dependent: %v", err)
	}
}

func TestPluginRegistry_Unregister(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("alpha", "1.0.0")
	reg.Register(p)

	if err := reg.Unregister("alpha"); err != nil {
		t.Fatalf("Unregister() unexpected error: %v", err)
	}

	if !p.shutDown {
		t.Error("plugin should be shut down after unregistration")
	}

	if reg.Count() != 0 {
		t.Errorf("Count() = %d, want 0", reg.Count())
	}
}

func TestPluginRegistry_Unregister_NotFound(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	if err := reg.Unregister("nonexistent"); err == nil {
		t.Error("Unregister() should fail for nonexistent plugin")
	}
}

func TestPluginRegistry_Unregister_HasDependents(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	dep := newMockPlugin("base", "1.0.0")
	reg.Register(dep)

	child := newMockPlugin("child", "1.0.0")
	child.meta.Dependencies = []string{"base"}
	reg.Register(child)

	if err := reg.Unregister("base"); err == nil {
		t.Error("Unregister() should fail when other plugins depend on it")
	}
}

func TestPluginRegistry_Unregister_ShutdownError(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("fail-shutdown", "1.0.0")
	p.shutdownErr = fmt.Errorf("shutdown broke")
	reg.Register(p)

	if err := reg.Unregister("fail-shutdown"); err == nil {
		t.Error("Unregister() should return error when Shutdown fails")
	}
}

func TestPluginRegistry_Get(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("alpha", "1.0.0")
	reg.Register(p)

	got, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("Get() should find registered plugin")
	}
	if got.Metadata().Name != "alpha" {
		t.Errorf("Get() returned plugin with name %q, want %q", got.Metadata().Name, "alpha")
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("Get() should return false for nonexistent plugin")
	}
}

func TestPluginRegistry_ListAll(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	reg.Register(newMockPlugin("alpha", "1.0.0"))
	reg.Register(newMockPlugin("beta", "2.0.0"))

	all := reg.ListAll()
	if len(all) != 2 {
		t.Errorf("ListAll() returned %d entries, want 2", len(all))
	}
}

func TestPluginRegistry_ListByCapability(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	p1 := newMockPlugin("alpha", "1.0.0")
	p1.meta.Capabilities = []string{"search", "analysis"}
	reg.Register(p1)

	p2 := newMockPlugin("beta", "1.0.0")
	p2.meta.Capabilities = []string{"monitoring"}
	reg.Register(p2)

	search := reg.ListByCapability("search")
	if len(search) != 1 || search[0].Name != "alpha" {
		t.Errorf("ListByCapability(search) = %v, want [alpha]", search)
	}

	none := reg.ListByCapability("nonexistent")
	if len(none) != 0 {
		t.Errorf("ListByCapability(nonexistent) = %v, want empty", none)
	}
}

func TestPluginRegistry_EnableDisable(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("alpha", "1.0.0")
	reg.Register(p)

	if !reg.IsEnabled("alpha") {
		t.Error("plugin should be enabled after registration")
	}

	if err := reg.Disable("alpha"); err != nil {
		t.Fatalf("Disable() error: %v", err)
	}
	if reg.IsEnabled("alpha") {
		t.Error("plugin should be disabled after Disable()")
	}

	if err := reg.Enable("alpha"); err != nil {
		t.Fatalf("Enable() error: %v", err)
	}
	if !reg.IsEnabled("alpha") {
		t.Error("plugin should be enabled after Enable()")
	}
}

func TestPluginRegistry_EnableDisable_NotFound(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	if err := reg.Enable("ghost"); err == nil {
		t.Error("Enable() should fail for unregistered plugin")
	}
	if err := reg.Disable("ghost"); err == nil {
		t.Error("Disable() should fail for unregistered plugin")
	}
}

func TestPluginRegistry_IsEnabled_NotRegistered(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))
	if reg.IsEnabled("nonexistent") {
		t.Error("IsEnabled() should return false for unregistered plugin")
	}
}

func TestPluginRegistry_GetAllTools(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	p1 := newMockPlugin("alpha", "1.0.0")
	p1.tools = []types.Tool{&mockTool{name: "tool-a"}, &mockTool{name: "tool-b"}}
	reg.Register(p1)

	p2 := newMockPlugin("beta", "1.0.0")
	p2.tools = []types.Tool{&mockTool{name: "tool-c"}}
	reg.Register(p2)

	tools := reg.GetAllTools()
	if len(tools) != 3 {
		t.Errorf("GetAllTools() returned %d tools, want 3", len(tools))
	}
}

func TestPluginRegistry_GetAllTools_DisabledExcluded(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	p1 := newMockPlugin("alpha", "1.0.0")
	p1.tools = []types.Tool{&mockTool{name: "tool-a"}}
	reg.Register(p1)

	p2 := newMockPlugin("beta", "1.0.0")
	p2.tools = []types.Tool{&mockTool{name: "tool-b"}}
	reg.Register(p2)

	reg.Disable("beta")

	tools := reg.GetAllTools()
	if len(tools) != 1 {
		t.Errorf("GetAllTools() returned %d tools, want 1 (disabled excluded)", len(tools))
	}
	if tools[0].Name() != "tool-a" {
		t.Errorf("GetAllTools() returned %q, want tool-a", tools[0].Name())
	}
}

func TestPluginRegistry_ShutdownAll(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	p1 := newMockPlugin("alpha", "1.0.0")
	p2 := newMockPlugin("beta", "1.0.0")
	reg.Register(p1)
	reg.Register(p2)

	if err := reg.ShutdownAll(); err != nil {
		t.Fatalf("ShutdownAll() error: %v", err)
	}

	if !p1.shutDown || !p2.shutDown {
		t.Error("all plugins should be shut down")
	}

	if reg.Count() != 0 {
		t.Errorf("Count() = %d after ShutdownAll, want 0", reg.Count())
	}
}

func TestPluginRegistry_ShutdownAll_WithErrors(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	p := newMockPlugin("fail", "1.0.0")
	p.shutdownErr = fmt.Errorf("boom")
	reg.Register(p)

	if err := reg.ShutdownAll(); err == nil {
		t.Error("ShutdownAll() should return error when a plugin shutdown fails")
	}
}

func TestPluginRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewPluginRegistry(testContext(t))

	// Pre-register some plugins
	for i := 0; i < 5; i++ {
		reg.Register(newMockPlugin(fmt.Sprintf("pre-%d", i), "1.0.0"))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)

	// Concurrent readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.ListAll()
			_ = reg.GetAllTools()
			_ = reg.ListByCapability("testing")
			reg.Get("pre-0")
			_ = reg.IsEnabled("pre-0")
			_ = reg.Count()
		}()
	}

	// Concurrent writers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent-%d", idx)
			if err := reg.Register(newMockPlugin(name, "1.0.0")); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent operation error: %v", err)
	}
}

// ==================== Manifest Tests ====================

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, ManifestFileName)

	manifest := PluginManifest{
		Name:         "test-plugin",
		Version:      "1.2.3",
		Description:  "A test plugin",
		Author:       "tester",
		Entrypoint:   "./plugin.so",
		Tags:         []string{"test", "sample"},
		Capabilities: []string{"analysis"},
		ConfigSchema: map[string]ConfigParam{
			"timeout": {Type: "int", Description: "timeout in seconds", Default: 30},
		},
	}

	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(manifestPath, data, 0644)

	loaded, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error: %v", err)
	}

	if loaded.Name != "test-plugin" {
		t.Errorf("Name = %q, want %q", loaded.Name, "test-plugin")
	}
	if loaded.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", loaded.Version, "1.2.3")
	}
	if loaded.Dir != dir {
		t.Errorf("Dir = %q, want %q", loaded.Dir, dir)
	}
	if len(loaded.ConfigSchema) != 1 {
		t.Errorf("ConfigSchema has %d entries, want 1", len(loaded.ConfigSchema))
	}
}

func TestLoadManifest_NotFound(t *testing.T) {
	_, err := LoadManifest("/nonexistent/plugin.json")
	if err == nil {
		t.Error("LoadManifest() should fail for missing file")
	}
}

func TestLoadManifest_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFileName)
	os.WriteFile(path, []byte("{invalid json"), 0644)

	_, err := LoadManifest(path)
	if err == nil {
		t.Error("LoadManifest() should fail for invalid JSON")
	}
}

func TestValidateManifest(t *testing.T) {
	tests := []struct {
		name     string
		manifest PluginManifest
		wantErrs int
	}{
		{
			name: "valid",
			manifest: PluginManifest{
				Name:        "test",
				Version:     "1.0.0",
				Description: "desc",
				Author:      "author",
				Entrypoint:  "./main.go",
			},
			wantErrs: 0,
		},
		{
			name:     "empty",
			manifest: PluginManifest{},
			wantErrs: 5, // name, version, description, author, entrypoint
		},
		{
			name: "invalid version",
			manifest: PluginManifest{
				Name:        "test",
				Version:     "abc",
				Description: "desc",
				Author:      "author",
				Entrypoint:  "./main.go",
			},
			wantErrs: 1,
		},
		{
			name: "invalid config type",
			manifest: PluginManifest{
				Name:        "test",
				Version:     "1.0.0",
				Description: "desc",
				Author:      "author",
				Entrypoint:  "./main.go",
				ConfigSchema: map[string]ConfigParam{
					"bad": {Type: "invalid_type"},
				},
			},
			wantErrs: 1,
		},
		{
			name: "invalid min gateway version",
			manifest: PluginManifest{
				Name:              "test",
				Version:           "1.0.0",
				Description:       "desc",
				Author:            "author",
				Entrypoint:        "./main.go",
				MinGatewayVersion: "nope",
			},
			wantErrs: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateManifest(&tc.manifest)
			if len(errs) != tc.wantErrs {
				t.Errorf("ValidateManifest() returned %d errors, want %d: %v", len(errs), tc.wantErrs, errs)
			}
		})
	}
}

func TestPluginManifest_ToMetadata(t *testing.T) {
	m := PluginManifest{
		Name:              "test",
		Version:           "1.0.0",
		Author:            "author",
		Description:       "desc",
		Tags:              []string{"tag1"},
		Capabilities:      []string{"cap1"},
		Dependencies:      []string{"dep1"},
		MinGatewayVersion: "0.5.0",
	}

	meta := m.ToMetadata()
	if meta.Name != "test" || meta.Version != "1.0.0" || meta.Author != "author" {
		t.Error("ToMetadata() did not copy fields correctly")
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "tag1" {
		t.Error("ToMetadata() did not copy tags")
	}
	if meta.MinGatewayVersion != "0.5.0" {
		t.Error("ToMetadata() did not copy MinGatewayVersion")
	}
}

func TestDiscoverPlugins(t *testing.T) {
	baseDir := t.TempDir()

	// Create valid plugin directory
	pluginDir := filepath.Join(baseDir, "good-plugin")
	os.MkdirAll(pluginDir, 0755)
	manifest := PluginManifest{
		Name:        "good-plugin",
		Version:     "1.0.0",
		Description: "A good plugin",
		Author:      "tester",
		Entrypoint:  "./main.go",
	}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(pluginDir, ManifestFileName), data, 0644)

	// Create plugin with invalid manifest
	badDir := filepath.Join(baseDir, "bad-plugin")
	os.MkdirAll(badDir, 0755)
	os.WriteFile(filepath.Join(badDir, ManifestFileName), []byte("{}"), 0644)

	// Create directory without manifest
	noManifest := filepath.Join(baseDir, "no-manifest")
	os.MkdirAll(noManifest, 0755)

	results := DiscoverPlugins([]string{baseDir})

	if len(results) != 1 {
		t.Fatalf("DiscoverPlugins() found %d plugins, want 1", len(results))
	}

	if results[0].Name != "good-plugin" {
		t.Errorf("DiscoverPlugins() found %q, want %q", results[0].Name, "good-plugin")
	}

	if results[0].Dir != pluginDir {
		t.Errorf("Dir = %q, want %q", results[0].Dir, pluginDir)
	}
}

func TestDiscoverPlugins_MultipleDirectories(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	for i, dir := range []string{dir1, dir2} {
		pluginDir := filepath.Join(dir, fmt.Sprintf("plugin-%d", i))
		os.MkdirAll(pluginDir, 0755)
		m := PluginManifest{
			Name:        fmt.Sprintf("plugin-%d", i),
			Version:     "1.0.0",
			Description: "test",
			Author:      "tester",
			Entrypoint:  "./main.go",
		}
		data, _ := json.Marshal(m)
		os.WriteFile(filepath.Join(pluginDir, ManifestFileName), data, 0644)
	}

	results := DiscoverPlugins([]string{dir1, dir2})
	if len(results) != 2 {
		t.Errorf("DiscoverPlugins() found %d plugins, want 2", len(results))
	}
}

func TestDiscoverPlugins_DeduplicatesByName(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// Same plugin name in both directories
	for _, dir := range []string{dir1, dir2} {
		pluginDir := filepath.Join(dir, "shared")
		os.MkdirAll(pluginDir, 0755)
		m := PluginManifest{
			Name:        "shared-plugin",
			Version:     "1.0.0",
			Description: "test",
			Author:      "tester",
			Entrypoint:  "./main.go",
		}
		data, _ := json.Marshal(m)
		os.WriteFile(filepath.Join(pluginDir, ManifestFileName), data, 0644)
	}

	results := DiscoverPlugins([]string{dir1, dir2})
	if len(results) != 1 {
		t.Errorf("DiscoverPlugins() found %d plugins, want 1 (dedup)", len(results))
	}
}

func TestDiscoverPlugins_NonexistentDirs(t *testing.T) {
	results := DiscoverPlugins([]string{"/nonexistent/path"})
	if len(results) != 0 {
		t.Errorf("DiscoverPlugins() found %d plugins for nonexistent dir, want 0", len(results))
	}
}

func TestDiscoverPlugins_EmptyDirs(t *testing.T) {
	results := DiscoverPlugins([]string{})
	if len(results) != 0 {
		t.Errorf("DiscoverPlugins() found %d plugins for empty dirs, want 0", len(results))
	}
}

// ==================== Marketplace Catalog Tests ====================

func newTestEntry(name string, tags []string) PluginEntry {
	return PluginEntry{
		Metadata: PluginMetadata{
			Name:         name,
			Version:      "1.0.0",
			Author:       "tester",
			Description:  "Description for " + name,
			Tags:         tags,
			Capabilities: []string{"cap-" + name},
		},
		Status: StatusAvailable,
	}
}

func TestCatalog_AddAndGet(t *testing.T) {
	cat := NewCatalog()
	entry := newTestEntry("alpha", []string{"test"})
	cat.AddEntry(entry)

	got := cat.GetEntry("alpha")
	if got == nil {
		t.Fatal("GetEntry() returned nil for existing entry")
	}
	if got.Metadata.Name != "alpha" {
		t.Errorf("Name = %q, want %q", got.Metadata.Name, "alpha")
	}
}

func TestCatalog_GetEntry_NotFound(t *testing.T) {
	cat := NewCatalog()
	if got := cat.GetEntry("nonexistent"); got != nil {
		t.Error("GetEntry() should return nil for nonexistent entry")
	}
}

func TestCatalog_GetEntry_ReturnsCopy(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("alpha", []string{"test"}))

	got := cat.GetEntry("alpha")
	got.Rating = 5.0 // Modify the copy

	original := cat.GetEntry("alpha")
	if original.Rating != 0 {
		t.Error("GetEntry() should return a copy, not a reference")
	}
}

func TestCatalog_RemoveEntry(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("alpha", nil))

	if !cat.RemoveEntry("alpha") {
		t.Error("RemoveEntry() should return true for existing entry")
	}
	if cat.Count() != 0 {
		t.Errorf("Count() = %d after remove, want 0", cat.Count())
	}
	if cat.RemoveEntry("alpha") {
		t.Error("RemoveEntry() should return false for already removed entry")
	}
}

func TestCatalog_Search_EmptyQuery(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("alpha", nil))
	cat.AddEntry(newTestEntry("beta", nil))

	results := cat.Search("")
	if len(results) != 2 {
		t.Errorf("Search(\"\") returned %d results, want 2 (all)", len(results))
	}
}

func TestCatalog_Search_ByName(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("weather-api", []string{"weather"}))
	cat.AddEntry(newTestEntry("code-lint", []string{"code"}))

	results := cat.Search("weather")
	if len(results) != 1 {
		t.Fatalf("Search(weather) returned %d results, want 1", len(results))
	}
	if results[0].Metadata.Name != "weather-api" {
		t.Errorf("Search(weather) returned %q, want %q", results[0].Metadata.Name, "weather-api")
	}
}

func TestCatalog_Search_ByDescription(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("alpha", nil)) // description: "Description for alpha"
	cat.AddEntry(newTestEntry("beta", nil))

	results := cat.Search("alpha")
	if len(results) != 1 {
		t.Errorf("Search(alpha) by description returned %d results, want 1", len(results))
	}
}

func TestCatalog_Search_ByTag(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("p1", []string{"monitoring", "alerts"}))
	cat.AddEntry(newTestEntry("p2", []string{"code-review"}))

	results := cat.Search("monitoring")
	if len(results) != 1 {
		t.Errorf("Search(monitoring) returned %d results, want 1", len(results))
	}
}

func TestCatalog_Search_ByCapability(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("finder", nil)) // cap: "cap-finder"

	results := cat.Search("cap-finder")
	if len(results) != 1 {
		t.Errorf("Search(cap-finder) returned %d results, want 1", len(results))
	}
}

func TestCatalog_Search_CaseInsensitive(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("MyPlugin", nil))

	results := cat.Search("myplugin")
	if len(results) != 1 {
		t.Errorf("case-insensitive search returned %d results, want 1", len(results))
	}
}

func TestCatalog_ListByTag(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("a", []string{"web", "api"}))
	cat.AddEntry(newTestEntry("b", []string{"cli"}))
	cat.AddEntry(newTestEntry("c", []string{"web"}))

	web := cat.ListByTag("web")
	if len(web) != 2 {
		t.Errorf("ListByTag(web) returned %d, want 2", len(web))
	}
}

func TestCatalog_ListByTag_CaseInsensitive(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("a", []string{"Web"}))

	results := cat.ListByTag("web")
	if len(results) != 1 {
		t.Errorf("case-insensitive ListByTag returned %d, want 1", len(results))
	}
}

func TestCatalog_ListByStatus(t *testing.T) {
	cat := NewCatalog()

	e1 := newTestEntry("installed-1", nil)
	e1.Status = StatusInstalled
	cat.AddEntry(e1)

	e2 := newTestEntry("available-1", nil)
	e2.Status = StatusAvailable
	cat.AddEntry(e2)

	e3 := newTestEntry("disabled-1", nil)
	e3.Status = StatusDisabled
	cat.AddEntry(e3)

	installed := cat.ListByStatus(StatusInstalled)
	if len(installed) != 1 {
		t.Errorf("ListByStatus(installed) = %d, want 1", len(installed))
	}
}

func TestCatalog_ListAll(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("a", nil))
	cat.AddEntry(newTestEntry("b", nil))
	cat.AddEntry(newTestEntry("c", nil))

	all := cat.ListAll()
	if len(all) != 3 {
		t.Errorf("ListAll() = %d, want 3", len(all))
	}
}

func TestCatalog_Count(t *testing.T) {
	cat := NewCatalog()
	if cat.Count() != 0 {
		t.Errorf("Count() = %d for empty catalog, want 0", cat.Count())
	}

	cat.AddEntry(newTestEntry("a", nil))
	if cat.Count() != 1 {
		t.Errorf("Count() = %d, want 1", cat.Count())
	}
}

func TestCatalog_UpdateStatus(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("alpha", nil))

	if !cat.UpdateStatus("alpha", StatusInstalled) {
		t.Error("UpdateStatus() should return true for existing entry")
	}

	got := cat.GetEntry("alpha")
	if got.Status != StatusInstalled {
		t.Errorf("Status = %q, want %q", got.Status, StatusInstalled)
	}

	if cat.UpdateStatus("ghost", StatusInstalled) {
		t.Error("UpdateStatus() should return false for nonexistent entry")
	}
}

func TestCatalog_IncrementDownloads(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("alpha", nil))

	for i := 0; i < 5; i++ {
		cat.IncrementDownloads("alpha")
	}

	got := cat.GetEntry("alpha")
	if got.DownloadCount != 5 {
		t.Errorf("DownloadCount = %d, want 5", got.DownloadCount)
	}

	if cat.IncrementDownloads("ghost") {
		t.Error("IncrementDownloads() should return false for nonexistent entry")
	}
}

func TestCatalog_SetRating(t *testing.T) {
	cat := NewCatalog()
	cat.AddEntry(newTestEntry("alpha", nil))

	cat.SetRating("alpha", 4.5)
	if got := cat.GetEntry("alpha"); got.Rating != 4.5 {
		t.Errorf("Rating = %f, want 4.5", got.Rating)
	}

	// Test clamping
	cat.SetRating("alpha", -1)
	if got := cat.GetEntry("alpha"); got.Rating != 0 {
		t.Errorf("Rating = %f after negative, want 0", got.Rating)
	}

	cat.SetRating("alpha", 10)
	if got := cat.GetEntry("alpha"); got.Rating != 5 {
		t.Errorf("Rating = %f after >5, want 5", got.Rating)
	}

	if cat.SetRating("ghost", 3.0) {
		t.Error("SetRating() should return false for nonexistent entry")
	}
}

func TestCatalog_ImportFromManifests(t *testing.T) {
	cat := NewCatalog()

	manifests := []PluginManifest{
		{Name: "a", Version: "1.0.0", Author: "x", Description: "desc a"},
		{Name: "b", Version: "2.0.0", Author: "y", Description: "desc b"},
	}

	added := cat.ImportFromManifests(manifests)
	if added != 2 {
		t.Errorf("ImportFromManifests() added %d, want 2", added)
	}
	if cat.Count() != 2 {
		t.Errorf("Count() = %d, want 2", cat.Count())
	}

	// Importing again should not overwrite
	added2 := cat.ImportFromManifests(manifests)
	if added2 != 0 {
		t.Errorf("second ImportFromManifests() added %d, want 0", added2)
	}

	// All entries should be StatusAvailable
	for _, e := range cat.ListAll() {
		if e.Status != StatusAvailable {
			t.Errorf("imported entry %q has status %q, want %q", e.Metadata.Name, e.Status, StatusAvailable)
		}
	}
}

func TestCatalog_AddEntry_Overwrites(t *testing.T) {
	cat := NewCatalog()

	e1 := newTestEntry("alpha", nil)
	e1.Rating = 3.0
	cat.AddEntry(e1)

	e2 := newTestEntry("alpha", []string{"updated"})
	e2.Rating = 5.0
	cat.AddEntry(e2)

	got := cat.GetEntry("alpha")
	if got.Rating != 5.0 {
		t.Errorf("AddEntry should overwrite: Rating = %f, want 5.0", got.Rating)
	}
}

func TestCatalog_ConcurrentAccess(t *testing.T) {
	cat := NewCatalog()
	var wg sync.WaitGroup

	// Concurrent writers
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("plugin-%d", idx)
			cat.AddEntry(newTestEntry(name, []string{"concurrent"}))
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cat.Search("plugin")
			_ = cat.ListByTag("concurrent")
			_ = cat.ListAll()
			_ = cat.Count()
		}()
	}

	wg.Wait()

	if cat.Count() != 20 {
		t.Errorf("Count() = %d after concurrent adds, want 20", cat.Count())
	}
}

// ==================== Integration: Registry + Catalog ====================

func TestIntegration_RegistryAndCatalog(t *testing.T) {
	// Discover manifests, import into catalog, then register plugins
	baseDir := t.TempDir()

	// Create a plugin manifest on disk
	pluginDir := filepath.Join(baseDir, "sample")
	os.MkdirAll(pluginDir, 0755)
	m := PluginManifest{
		Name:         "sample-plugin",
		Version:      "1.0.0",
		Description:  "a sample plugin",
		Author:       "tester",
		Entrypoint:   "./main.go",
		Tags:         []string{"sample"},
		Capabilities: []string{"demo"},
	}
	data, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(pluginDir, ManifestFileName), data, 0644)

	// Discover plugins
	manifests := DiscoverPlugins([]string{baseDir})
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %d", len(manifests))
	}

	// Import into catalog
	cat := NewCatalog()
	added := cat.ImportFromManifests(manifests)
	if added != 1 {
		t.Fatalf("expected 1 added, got %d", added)
	}

	// Verify catalog entry
	entry := cat.GetEntry("sample-plugin")
	if entry == nil {
		t.Fatal("catalog entry not found")
	}
	if entry.Status != StatusAvailable {
		t.Errorf("status = %q, want %q", entry.Status, StatusAvailable)
	}

	// Now simulate installing by creating a mock plugin and registering it
	reg := NewPluginRegistry(testContext(t))
	p := newMockPlugin("sample-plugin", "1.0.0")
	p.meta.Tags = []string{"sample"}
	p.meta.Capabilities = []string{"demo"}

	if err := reg.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Update catalog status
	cat.UpdateStatus("sample-plugin", StatusInstalled)

	installed := cat.ListByStatus(StatusInstalled)
	if len(installed) != 1 {
		t.Errorf("installed count = %d, want 1", len(installed))
	}

	// Verify tools are accessible
	tools := reg.GetAllTools()
	if len(tools) == 0 {
		t.Error("registered plugin should provide tools")
	}

	// Search catalog
	results := cat.Search("sample")
	if len(results) != 1 {
		t.Errorf("search returned %d results, want 1", len(results))
	}
}
