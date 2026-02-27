package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// TabInfo holds metadata about a session tab
type TabInfo struct {
	SessionKey string
	Label      string
	HasUnread  bool
}

// TabBarModel manages the session tab bar
type TabBarModel struct {
	Tabs      []TabInfo
	ActiveIdx int
	Width     int
	Styles    Styles
}

// NewTabBarModel creates a tab bar with an initial tab
func NewTabBarModel(styles Styles) TabBarModel {
	return TabBarModel{
		Tabs: []TabInfo{
			{Label: "Chat 1", SessionKey: ""},
		},
		ActiveIdx: 0,
		Styles:    styles,
	}
}

// AddTab adds a new tab and returns its index
func (t *TabBarModel) AddTab(sessionKey, label string) int {
	if label == "" {
		label = fmt.Sprintf("Chat %d", len(t.Tabs)+1)
	}
	t.Tabs = append(t.Tabs, TabInfo{
		SessionKey: sessionKey,
		Label:      label,
	})
	return len(t.Tabs) - 1
}

// RemoveTab removes the tab at the given index
func (t *TabBarModel) RemoveTab(idx int) {
	if idx < 0 || idx >= len(t.Tabs) || len(t.Tabs) <= 1 {
		return
	}
	t.Tabs = append(t.Tabs[:idx], t.Tabs[idx+1:]...)
	if t.ActiveIdx >= len(t.Tabs) {
		t.ActiveIdx = len(t.Tabs) - 1
	}
}

// ActiveTab returns the active tab info
func (t *TabBarModel) ActiveTab() *TabInfo {
	if t.ActiveIdx >= 0 && t.ActiveIdx < len(t.Tabs) {
		return &t.Tabs[t.ActiveIdx]
	}
	return nil
}

// View renders the tab bar
func (t TabBarModel) View() string {
	if len(t.Tabs) == 0 {
		return ""
	}

	var tabs []string
	for i, tab := range t.Tabs {
		label := tab.Label
		if tab.HasUnread && i != t.ActiveIdx {
			label = "● " + label
		}

		var style lipgloss.Style
		if i == t.ActiveIdx {
			style = t.Styles.TabActive
		} else if tab.HasUnread {
			style = t.Styles.TabUnread
		} else {
			style = t.Styles.TabInactive
		}

		tabs = append(tabs, style.Render(label))
	}

	bar := strings.Join(tabs, " ")
	return t.Styles.TabBar.Width(t.Width).Render(bar)
}
