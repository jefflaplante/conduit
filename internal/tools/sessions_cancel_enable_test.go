package tools

import (
	"testing"

	"conduit/internal/config"
)

// conduit-38cz: SessionsCancel is enabled whenever SessionsSpawn is, so an
// existing enabled_tools list needs no edit to get the cancel primitive.
func TestSessionsCancel_EnabledWithSessionsSpawn(t *testing.T) {
	r := NewRegistry(config.ToolsConfig{EnabledTools: []string{"SessionsSpawn"}})
	if !r.isToolEnabled("SessionsCancel") {
		t.Error("SessionsCancel must come with SessionsSpawn")
	}
	r = NewRegistry(config.ToolsConfig{EnabledTools: []string{"Read"}})
	if r.isToolEnabled("SessionsCancel") {
		t.Error("SessionsCancel must stay off without SessionsSpawn")
	}
}
