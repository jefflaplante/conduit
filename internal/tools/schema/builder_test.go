package schema

import (
	"context"
	"testing"
	"time"
)

// mockDiscoveryProvider is a test implementation of DiscoveryProvider
type mockDiscoveryProvider struct {
	discoveryType string
	values        []DynamicValue
	err           error
}

func (m *mockDiscoveryProvider) GetDiscoveryData(ctx context.Context, discoveryType string) (*DynamicValues, error) {
	if m.err != nil {
		return nil, m.err
	}
	if discoveryType != m.discoveryType {
		return nil, nil
	}
	return &DynamicValues{
		Source:      m.discoveryType,
		Values:      m.values,
		LastUpdated: time.Now(),
	}, nil
}

func TestNewBuilder(t *testing.T) {
	providers := map[string]DiscoveryProvider{
		"test": &mockDiscoveryProvider{discoveryType: "test"},
	}
	builder := NewBuilder(providers)
	if builder == nil {
		t.Fatal("NewBuilder returned nil")
	}
	if len(builder.providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(builder.providers))
	}
}

func TestBuilder_EnhanceSchema_WithExamples(t *testing.T) {
	builder := NewBuilder(nil)

	staticSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"type":        "string",
				"description": "Target channel",
			},
		},
	}

	hints := map[string]SchemaHints{
		"target": {
			Examples: []interface{}{"telegram:123", "discord:456"},
		},
	}

	enhanced := builder.EnhanceSchema(context.Background(), staticSchema, hints)

	props := enhanced["properties"].(map[string]interface{})
	targetProp := props["target"].(map[string]interface{})

	examples, ok := targetProp["examples"].([]interface{})
	if !ok {
		t.Fatal("examples not added to schema")
	}
	if len(examples) != 2 {
		t.Errorf("expected 2 examples, got %d", len(examples))
	}
}

func TestBuilder_EnhanceSchema_WithValidationHints(t *testing.T) {
	builder := NewBuilder(nil)

	staticSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path",
			},
		},
	}

	hints := map[string]SchemaHints{
		"path": {
			ValidationHints: []string{"Must be within workspace", "Relative paths preferred"},
		},
	}

	enhanced := builder.EnhanceSchema(context.Background(), staticSchema, hints)

	props := enhanced["properties"].(map[string]interface{})
	pathProp := props["path"].(map[string]interface{})

	validationHints, ok := pathProp["validation_hints"].([]string)
	if !ok {
		t.Fatal("validation_hints not added to schema")
	}
	if len(validationHints) != 2 {
		t.Errorf("expected 2 validation hints, got %d", len(validationHints))
	}
}

func TestBuilder_EnhanceSchema_WithDiscovery(t *testing.T) {
	mockProvider := &mockDiscoveryProvider{
		discoveryType: "channels",
		values: []DynamicValue{
			{Value: "telegram:main", Label: "Main Channel", Status: "online"},
			{Value: "discord:dev", Label: "Dev Channel", Status: "offline"},
		},
	}

	builder := NewBuilder(map[string]DiscoveryProvider{
		"channels": mockProvider,
	})

	staticSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"type":        "string",
				"description": "Target channel",
			},
		},
	}

	hints := map[string]SchemaHints{
		"target": {
			DiscoveryType: "channels",
		},
	}

	enhanced := builder.EnhanceSchema(context.Background(), staticSchema, hints)

	props := enhanced["properties"].(map[string]interface{})
	targetProp := props["target"].(map[string]interface{})

	discovery, ok := targetProp["x_discovery"].(*DynamicValues)
	if !ok {
		t.Fatal("x_discovery not added to schema")
	}
	if discovery.Source != "channels" {
		t.Errorf("expected source 'channels', got '%s'", discovery.Source)
	}
	if len(discovery.Values) != 2 {
		t.Errorf("expected 2 values, got %d", len(discovery.Values))
	}
}

func TestBuilder_EnhanceSchema_EnumFromDiscovery(t *testing.T) {
	mockProvider := &mockDiscoveryProvider{
		discoveryType: "channels",
		values: []DynamicValue{
			{Value: "channel1"},
			{Value: "channel2"},
			{Value: "channel3"},
		},
	}

	builder := NewBuilder(map[string]DiscoveryProvider{
		"channels": mockProvider,
	})

	staticSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"type":        "string",
				"description": "Target channel",
			},
		},
	}

	hints := map[string]SchemaHints{
		"target": {
			DiscoveryType:     "channels",
			EnumFromDiscovery: true,
		},
	}

	enhanced := builder.EnhanceSchema(context.Background(), staticSchema, hints)

	props := enhanced["properties"].(map[string]interface{})
	targetProp := props["target"].(map[string]interface{})

	enum, ok := targetProp["enum"].([]string)
	if !ok {
		t.Fatal("enum not added to schema")
	}
	if len(enum) != 3 {
		t.Errorf("expected 3 enum values, got %d", len(enum))
	}
}

func TestBuilder_EnhanceSchema_NoProperties(t *testing.T) {
	builder := NewBuilder(nil)

	// Schema without properties should be returned unchanged
	staticSchema := map[string]interface{}{
		"type": "object",
	}

	hints := map[string]SchemaHints{
		"target": {
			Examples: []interface{}{"test"},
		},
	}

	enhanced := builder.EnhanceSchema(context.Background(), staticSchema, hints)

	if _, ok := enhanced["properties"]; ok {
		t.Error("properties should not be added if not present in original")
	}
}

func TestBuilder_EnhanceSchema_NilHints(t *testing.T) {
	builder := NewBuilder(nil)

	staticSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"type": "string",
			},
		},
	}

	// Should not panic with nil hints
	enhanced := builder.EnhanceSchema(context.Background(), staticSchema, nil)

	if enhanced == nil {
		t.Fatal("EnhanceSchema returned nil")
	}
}

func TestDeepCopyMap(t *testing.T) {
	original := map[string]interface{}{
		"string": "value",
		"number": 42,
		"nested": map[string]interface{}{
			"inner": "data",
		},
		"array":       []interface{}{"a", "b"},
		"stringArray": []string{"x", "y"},
	}

	cpy := deepCopyMap(original)

	// Verify it's a deep copy by modifying original
	original["string"] = "modified"
	original["nested"].(map[string]interface{})["inner"] = "modified"

	if cpy["string"] != "value" {
		t.Error("copy was affected by modification to original string")
	}
	if cpy["nested"].(map[string]interface{})["inner"] != "data" {
		t.Error("copy was affected by modification to original nested map")
	}
}

func TestDeepCopyMap_Nil(t *testing.T) {
	cpy := deepCopyMap(nil)
	if cpy != nil {
		t.Error("deepCopyMap(nil) should return nil")
	}
}

func TestChannelDiscoveryProvider(t *testing.T) {
	mockGetter := &mockChannelStatusGetter{
		status: map[string]interface{}{
			"telegram:main": map[string]interface{}{
				"status": "online",
				"name":   "Main Chat",
			},
			"discord:dev": map[string]interface{}{
				"status": "offline",
			},
		},
	}

	provider := NewChannelDiscoveryProvider(mockGetter)

	// Test with correct discovery type
	result, err := provider.GetDiscoveryData(context.Background(), "channels")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Source != "channels" {
		t.Errorf("expected source 'channels', got '%s'", result.Source)
	}
	if len(result.Values) != 2 {
		t.Errorf("expected 2 values, got %d", len(result.Values))
	}

	// Test with wrong discovery type
	result2, err := provider.GetDiscoveryData(context.Background(), "wrong_type")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2 != nil {
		t.Error("expected nil for wrong discovery type")
	}
}

func TestWorkspaceDiscoveryProvider(t *testing.T) {
	provider := NewWorkspaceDiscoveryProvider("/home/user/workspace", []string{"/home/user/workspace", "/tmp"})

	// Test with correct discovery type
	result, err := provider.GetDiscoveryData(context.Background(), "workspace_paths")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Source != "workspace_paths" {
		t.Errorf("expected source 'workspace_paths', got '%s'", result.Source)
	}
	// Should have workspace root + /tmp (workspace root not duplicated) + common examples
	if len(result.Values) != 5 {
		t.Errorf("expected 5 values, got %d", len(result.Values))
	}

	// Test with wrong discovery type
	result2, err := provider.GetDiscoveryData(context.Background(), "wrong_type")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2 != nil {
		t.Error("expected nil for wrong discovery type")
	}
}

// mockChannelStatusGetter implements ChannelStatusGetter for testing
type mockChannelStatusGetter struct {
	status map[string]interface{}
}

func (m *mockChannelStatusGetter) GetStatus() map[string]interface{} {
	return m.status
}
