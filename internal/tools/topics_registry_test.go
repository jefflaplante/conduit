package tools

import (
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// conduit-43vk: topic_* tools are registered with the rest and enabled by
// name like any other tool.
func TestRegistry_RegistersTopicTools(t *testing.T) {
	r := NewRegistry(config.ToolsConfig{EnabledTools: []string{"topic_list", "topic_switch"}})
	r.SetServices(&types.ToolServices{})
	avail := r.GetAvailableTools()
	for _, name := range []string{"topic_list", "topic_switch"} {
		if _, ok := avail[name]; !ok {
			t.Errorf("%s not available", name)
		}
	}
	if _, ok := avail["topic_close"]; ok {
		t.Errorf("topic_close available without being enabled")
	}
}
