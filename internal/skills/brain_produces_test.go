package skills

import (
	"context"
	"testing"
)

func TestSkillConduitMetaProduces(t *testing.T) {
	meta := SkillConduitMeta{
		Emoji: "☀️",
		Produces: []string{"solar.production", "solar.consumption"},
	}

	if len(meta.Produces) != 2 {
		t.Fatalf("expected 2 produces keys, got %d", len(meta.Produces))
	}
	if meta.Produces[0] != "solar.production" {
		t.Errorf("expected solar.production, got %s", meta.Produces[0])
	}
	if meta.Produces[1] != "solar.consumption" {
		t.Errorf("expected solar.consumption, got %s", meta.Produces[1])
	}
}

func TestSkillConduitMetaProducesEmpty(t *testing.T) {
	meta := SkillConduitMeta{
		Emoji: "🔧",
	}

	if meta.Produces != nil {
		t.Errorf("expected nil produces, got %v", meta.Produces)
	}
}

func TestSkillToolBrainProduces(t *testing.T) {
	skill := Skill{
		Name:        "solar",
		Description: "Solar monitoring",
		Metadata: SkillMetadata{
			Conduit: SkillConduitMeta{
				Emoji:    "☀️",
				Produces: []string{"solar.production", "solar.consumption"},
			},
		},
	}

	st := &SkillTool{
		skill:   skill,
		actions: []string{"status"},
	}

	produces := st.BrainProduces()
	if len(produces) != 2 {
		t.Fatalf("expected 2 produces keys, got %d", len(produces))
	}
	if produces[0] != "solar.production" {
		t.Errorf("expected solar.production, got %s", produces[0])
	}
}

func TestSkillToolBrainProducesEmpty(t *testing.T) {
	skill := Skill{
		Name:        "generic",
		Description: "Generic skill",
	}

	st := &SkillTool{
		skill:   skill,
		actions: []string{"status"},
	}

	produces := st.BrainProduces()
	if len(produces) != 0 {
		t.Errorf("expected empty produces, got %v", produces)
	}
}

// mockSkillTool implements SkillToolInterface for testing the adapter
type mockSkillToolWithProduces struct {
	name     string
	produces []string
}

func (m *mockSkillToolWithProduces) Name() string        { return m.name }
func (m *mockSkillToolWithProduces) Description() string  { return "mock skill" }
func (m *mockSkillToolWithProduces) Parameters() map[string]interface{} {
	return map[string]interface{}{}
}
func (m *mockSkillToolWithProduces) Execute(_ context.Context, _ map[string]interface{}) (*SkillToolResult, error) {
	return &SkillToolResult{Success: true}, nil
}
func (m *mockSkillToolWithProduces) BrainProduces() []string {
	return m.produces
}

// mockSkillToolNoProduces implements SkillToolInterface but NOT BrainKeyProducer
type mockSkillToolNoProduces struct{}

func (m *mockSkillToolNoProduces) Name() string        { return "noproducer" }
func (m *mockSkillToolNoProduces) Description() string  { return "mock skill" }
func (m *mockSkillToolNoProduces) Parameters() map[string]interface{} {
	return map[string]interface{}{}
}
func (m *mockSkillToolNoProduces) Execute(_ context.Context, _ map[string]interface{}) (*SkillToolResult, error) {
	return &SkillToolResult{Success: true}, nil
}

func TestSkillToolAdapterBrainProducesDelegates(t *testing.T) {
	mock := &mockSkillToolWithProduces{
		name:     "solar",
		produces: []string{"solar.production"},
	}
	adapter := NewSkillToolAdapter(mock)

	produces := adapter.BrainProduces()
	if len(produces) != 1 {
		t.Fatalf("expected 1 produces key, got %d", len(produces))
	}
	if produces[0] != "solar.production" {
		t.Errorf("expected solar.production, got %s", produces[0])
	}
}

func TestSkillToolAdapterBrainProducesNilWhenNotImplemented(t *testing.T) {
	mock := &mockSkillToolNoProduces{}
	adapter := NewSkillToolAdapter(mock)

	produces := adapter.BrainProduces()
	if produces != nil {
		t.Errorf("expected nil produces for non-producer, got %v", produces)
	}
}
