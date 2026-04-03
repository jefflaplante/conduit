package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTabBarModel(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	assert.Len(t, tabBar.Tabs, 1, "should start with one tab")
	assert.Equal(t, 0, tabBar.ActiveIdx, "active index should be 0")
	assert.Equal(t, "Chat 1", tabBar.Tabs[0].Label, "first tab should be labeled 'Chat 1'")
	assert.Equal(t, "", tabBar.Tabs[0].SessionKey, "first tab should have empty session key")
}

func TestTabBarModel_AddTab(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	// Add a tab with a label
	idx := tabBar.AddTab("session-123", "My Session")
	assert.Equal(t, 1, idx, "new tab should be at index 1")
	assert.Len(t, tabBar.Tabs, 2, "should have 2 tabs now")
	assert.Equal(t, "My Session", tabBar.Tabs[1].Label)
	assert.Equal(t, "session-123", tabBar.Tabs[1].SessionKey)

	// Add a tab without a label (auto-generated)
	idx = tabBar.AddTab("session-456", "")
	assert.Equal(t, 2, idx, "new tab should be at index 2")
	assert.Len(t, tabBar.Tabs, 3, "should have 3 tabs now")
	assert.Equal(t, "Chat 3", tabBar.Tabs[2].Label, "should auto-generate label")
}

func TestTabBarModel_RemoveTab(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	// Add some tabs
	tabBar.AddTab("session-1", "Tab 2")
	tabBar.AddTab("session-2", "Tab 3")
	tabBar.ActiveIdx = 1

	// Remove middle tab
	tabBar.RemoveTab(1)
	assert.Len(t, tabBar.Tabs, 2, "should have 2 tabs after removal")
	assert.Equal(t, "Chat 1", tabBar.Tabs[0].Label)
	assert.Equal(t, "Tab 3", tabBar.Tabs[1].Label)

	// Active index should be adjusted
	assert.Equal(t, 1, tabBar.ActiveIdx, "active index should remain valid")
}

func TestTabBarModel_RemoveTab_Last(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	tabBar.AddTab("session-1", "Tab 2")
	tabBar.ActiveIdx = 1

	// Remove the last tab
	tabBar.RemoveTab(1)
	assert.Len(t, tabBar.Tabs, 1, "should have 1 tab after removal")
	assert.Equal(t, 0, tabBar.ActiveIdx, "active index should be 0")
}

func TestTabBarModel_RemoveTab_OnlyTab(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	// Try to remove the only tab (should be no-op)
	tabBar.RemoveTab(0)
	assert.Len(t, tabBar.Tabs, 1, "cannot remove only tab")
}

func TestTabBarModel_RemoveTab_InvalidIndex(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	// Try to remove with invalid indices
	tabBar.RemoveTab(-1)
	assert.Len(t, tabBar.Tabs, 1, "should not remove with negative index")

	tabBar.RemoveTab(10)
	assert.Len(t, tabBar.Tabs, 1, "should not remove with out-of-bounds index")
}

func TestTabBarModel_ActiveTab(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	tab := tabBar.ActiveTab()
	require.NotNil(t, tab, "should return active tab")
	assert.Equal(t, "Chat 1", tab.Label)

	// Add another tab and switch to it
	tabBar.AddTab("session-1", "Tab 2")
	tabBar.ActiveIdx = 1
	tab = tabBar.ActiveTab()
	require.NotNil(t, tab)
	assert.Equal(t, "Tab 2", tab.Label)
}

func TestTabBarModel_ActiveTab_InvalidIndex(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)

	// Set invalid active index
	tabBar.ActiveIdx = 10
	tab := tabBar.ActiveTab()
	assert.Nil(t, tab, "should return nil for invalid index")

	tabBar.ActiveIdx = -1
	tab = tabBar.ActiveTab()
	assert.Nil(t, tab, "should return nil for negative index")
}

func TestTabBarModel_View(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)
	tabBar.Width = 80

	view := tabBar.View()
	assert.NotEmpty(t, view, "view should not be empty")
	assert.Contains(t, view, "Chat 1", "view should contain tab label")
}

func TestTabBarModel_View_WithUnread(t *testing.T) {
	styles := DefaultStyles()
	tabBar := NewTabBarModel(styles)
	tabBar.Width = 80

	// Add a second tab with unread messages
	tabBar.AddTab("session-1", "Tab 2")
	tabBar.Tabs[1].HasUnread = true

	view := tabBar.View()
	// The unread indicator is a bullet point
	assert.Contains(t, view, "Tab 2", "view should contain second tab label")
}

func TestTabBarModel_View_Empty(t *testing.T) {
	styles := DefaultStyles()
	tabBar := TabBarModel{
		Tabs:   []TabInfo{},
		Styles: styles,
	}

	view := tabBar.View()
	assert.Empty(t, view, "view should be empty with no tabs")
}

func TestTabInfo_Fields(t *testing.T) {
	tab := TabInfo{
		SessionKey: "test-key",
		Label:      "Test Tab",
		HasUnread:  true,
	}

	assert.Equal(t, "test-key", tab.SessionKey)
	assert.Equal(t, "Test Tab", tab.Label)
	assert.True(t, tab.HasUnread)
}
