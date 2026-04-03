package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewSidebarModel(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)

	assert.Equal(t, SidebarTabSession, sidebar.ActiveTab, "should start on session tab")
	assert.Equal(t, 40, sidebar.Width, "default width should be 40")
	assert.False(t, sidebar.Visible, "should start hidden")
}

func TestSidebarModel_CycleTab(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)

	assert.Equal(t, SidebarTabSession, sidebar.ActiveTab)

	sidebar.CycleTab()
	assert.Equal(t, SidebarTabTools, sidebar.ActiveTab)

	sidebar.CycleTab()
	assert.Equal(t, SidebarTabStatus, sidebar.ActiveTab)

	sidebar.CycleTab()
	assert.Equal(t, SidebarTabSession, sidebar.ActiveTab, "should cycle back to first tab")
}

func TestSidebarModel_SidebarWidth(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)

	// When not visible
	assert.Equal(t, 0, sidebar.SidebarWidth(), "should return 0 when not visible")

	// When visible
	sidebar.Visible = true
	assert.Equal(t, 40, sidebar.SidebarWidth(), "should return width when visible")

	sidebar.Width = 50
	assert.Equal(t, 50, sidebar.SidebarWidth(), "should return updated width")
}

func TestSidebarModel_TotalSidebarWidth(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)

	// When not visible
	assert.Equal(t, 0, sidebar.TotalSidebarWidth(), "should return 0 when not visible")

	// When visible (includes border and padding)
	sidebar.Visible = true
	expected := sidebar.Width + sidebarBorderWidth() + 2 // border + padding
	assert.Equal(t, expected, sidebar.TotalSidebarWidth())
}

func TestSidebarModel_View_NotVisible(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)

	view := sidebar.View()
	assert.Empty(t, view, "view should be empty when not visible")
}

func TestSidebarModel_View_SessionTab(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 20
	sidebar.ActiveTab = SidebarTabSession
	sidebar.SessionKey = "test-session-key"
	sidebar.MessageCount = 5
	sidebar.Model = "claude-3-opus"

	view := sidebar.View()
	assert.NotEmpty(t, view)
	assert.Contains(t, view, "Session")
}

func TestSidebarModel_View_ToolsTab(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 20
	sidebar.ActiveTab = SidebarTabTools

	// With no tools
	view := sidebar.View()
	assert.Contains(t, view, "No recent activity")

	// With tools
	sidebar.ActiveTools = []ToolActivityInfo{
		{Name: "search", Status: "complete", Duration: time.Second},
	}
	view = sidebar.View()
	assert.Contains(t, view, "search")
}

func TestSidebarModel_View_StatusTab(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 20
	sidebar.ActiveTab = SidebarTabStatus
	sidebar.Connected = true
	sidebar.GatewayURL = "ws://localhost:18789/ws"
	sidebar.Version = "1.0.0"
	sidebar.GitCommit = "abc1234567890"

	view := sidebar.View()
	assert.Contains(t, view, "Connection")
	assert.Contains(t, view, "Connected")
}

func TestFormatSidebarNumber(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{12, "12"},
		{123, "123"},
		{1234, "1,234"},
		{12345, "12,345"},
		{123456, "123,456"},
		{1234567, "1,234,567"},
		{-1, "-1"},
		{-1234, "-1,234"},
		{-123456, "-123,456"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatSidebarNumber(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{0 * time.Second, "0s"},
		{30 * time.Second, "30s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{60 * time.Minute, "1h"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{24 * time.Hour, "1d"},
		{25 * time.Hour, "1d1h"},
		{48 * time.Hour, "2d"},
		{50 * time.Hour, "2d2h"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatResponseTime(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{100 * time.Millisecond, "100ms"},
		{500 * time.Millisecond, "500ms"},
		{999 * time.Millisecond, "999ms"},
		{1 * time.Second, "1.0s"},
		{1500 * time.Millisecond, "1.5s"},
		{2 * time.Second, "2.0s"},
		{10 * time.Second, "10.0s"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatResponseTime(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRenderContextBar(t *testing.T) {
	styles := DefaultStyles()

	tests := []struct {
		percent float64
		width   int
	}{
		{0, 10},
		{25, 10},
		{50, 10},
		{75, 10},
		{100, 10},
		{120, 10}, // over 100%
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			result := renderContextBar(styles, tt.percent, tt.width)
			assert.NotEmpty(t, result)
			assert.Contains(t, result, "[")
			assert.Contains(t, result, "]")
		})
	}
}

func TestRenderContextBar_SmallWidth(t *testing.T) {
	styles := DefaultStyles()

	// Should handle very small widths
	result := renderContextBar(styles, 50, 2)
	assert.NotEmpty(t, result)
}

func TestSidebarBorderWidth(t *testing.T) {
	// Should return 1 for the left border
	assert.Equal(t, 1, sidebarBorderWidth())
}

func TestSidebarTab_Constants(t *testing.T) {
	// Ensure constants are defined correctly
	assert.Equal(t, SidebarTab(0), SidebarTabSession)
	assert.Equal(t, SidebarTab(1), SidebarTabTools)
	assert.Equal(t, SidebarTab(2), SidebarTabStatus)
}

func TestSidebarModel_SessionKeyTruncation(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 20 // Very narrow
	sidebar.Height = 20
	sidebar.ActiveTab = SidebarTabSession
	sidebar.SessionKey = "this-is-a-very-long-session-key-that-should-be-truncated"

	view := sidebar.View()
	// Just verify it renders without error
	assert.NotEmpty(t, view)
}

func TestSidebarModel_ModelAliases(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 30
	sidebar.ActiveTab = SidebarTabStatus
	sidebar.Version = "1.0.0"
	sidebar.ModelAliases = map[string]string{
		"sonnet": "claude-3-sonnet-20240229",
		"opus":   "claude-3-opus-20240229",
	}

	view := sidebar.View()
	assert.Contains(t, view, "Models")
	assert.Contains(t, view, "sonnet")
	assert.Contains(t, view, "opus")
}

func TestSidebarModel_WithCost(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 20
	sidebar.ActiveTab = SidebarTabSession
	sidebar.TotalTokens = 1000
	sidebar.SessionCost = 0.0123
	sidebar.RequestCost = 0.0045

	view := sidebar.View()
	assert.Contains(t, view, "Cost")
	assert.Contains(t, view, "Session")
}

func TestSidebarModel_WithUptime(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 30
	sidebar.ActiveTab = SidebarTabStatus
	sidebar.Version = "1.0.0"
	sidebar.UptimeSeconds = 3600 // 1 hour
	sidebar.ToolCount = 15
	sidebar.SkillCount = 3

	view := sidebar.View()
	assert.Contains(t, view, "Gateway")
	assert.Contains(t, view, "Tools")
	assert.Contains(t, view, "Skills")
}

func TestSidebarModel_SessionCreatedAt(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 30
	sidebar.ActiveTab = SidebarTabStatus
	sidebar.SessionCreatedAt = time.Now().Add(-30 * time.Minute)

	view := sidebar.View()
	assert.Contains(t, view, "Activity")
	assert.Contains(t, view, "Age")
}

func TestSidebarModel_LastResponseTime(t *testing.T) {
	styles := DefaultStyles()
	sidebar := NewSidebarModel(styles)
	sidebar.Visible = true
	sidebar.Width = 40
	sidebar.Height = 30
	sidebar.ActiveTab = SidebarTabStatus
	sidebar.LastResponseTime = 2500 * time.Millisecond

	view := sidebar.View()
	assert.Contains(t, view, "Last")
}
