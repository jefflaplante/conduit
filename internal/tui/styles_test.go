package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

func TestDefaultStyles(t *testing.T) {
	styles := DefaultStyles()

	// Verify styles are initialized (not nil/zero)
	assert.NotNil(t, styles.App)
	assert.NotNil(t, styles.TabActive)
	assert.NotNil(t, styles.TabInactive)
	assert.NotNil(t, styles.TabUnread)
	assert.NotNil(t, styles.TabBar)
	assert.NotNil(t, styles.UserBubble)
	assert.NotNil(t, styles.AssistantBubble)
	assert.NotNil(t, styles.SystemBubble)
	assert.NotNil(t, styles.ToolRunning)
	assert.NotNil(t, styles.ToolComplete)
	assert.NotNil(t, styles.ToolError)
	assert.NotNil(t, styles.StatusBar)
	assert.NotNil(t, styles.StatusConnected)
	assert.NotNil(t, styles.StatusDisconnected)
	assert.NotNil(t, styles.StatusReconnecting)
	assert.NotNil(t, styles.SidebarBorder)
	assert.NotNil(t, styles.SidebarTitle)
	assert.NotNil(t, styles.SidebarContent)
	assert.NotNil(t, styles.ContextLow)
	assert.NotNil(t, styles.ContextMedium)
	assert.NotNil(t, styles.ContextHigh)
	assert.NotNil(t, styles.ThinkingBar)
	assert.NotNil(t, styles.ThinkingTrack)
	assert.NotNil(t, styles.Muted)
	assert.NotNil(t, styles.Bold)
	assert.NotNil(t, styles.Accent)
}

func TestNewStyles(t *testing.T) {
	renderer := lipgloss.DefaultRenderer()
	styles := NewStyles(renderer)

	// Verify styles are initialized
	assert.NotNil(t, styles.App)
	assert.NotNil(t, styles.TabActive)
	assert.NotNil(t, styles.TabInactive)
}

func TestNewStyles_CustomRenderer(t *testing.T) {
	// Create a custom renderer (simulates SSH session)
	renderer := lipgloss.NewRenderer(nil)
	styles := NewStyles(renderer)

	// Styles should still be initialized
	assert.NotNil(t, styles.App)
	assert.NotNil(t, styles.TabActive)
}

func TestStyles_TabStylesRender(t *testing.T) {
	styles := DefaultStyles()

	// Test that styles can render text without panic
	active := styles.TabActive.Render("Active Tab")
	inactive := styles.TabInactive.Render("Inactive Tab")
	unread := styles.TabUnread.Render("Unread Tab")

	assert.Contains(t, active, "Active Tab")
	assert.Contains(t, inactive, "Inactive Tab")
	assert.Contains(t, unread, "Unread Tab")
}

func TestStyles_BubbleStylesRender(t *testing.T) {
	styles := DefaultStyles()

	user := styles.UserBubble.Render("User message")
	assistant := styles.AssistantBubble.Render("Assistant message")
	system := styles.SystemBubble.Render("System message")

	assert.Contains(t, user, "User message")
	assert.Contains(t, assistant, "Assistant message")
	assert.Contains(t, system, "System message")
}

func TestStyles_ToolStylesRender(t *testing.T) {
	styles := DefaultStyles()

	running := styles.ToolRunning.Render("Running...")
	complete := styles.ToolComplete.Render("Complete")
	err := styles.ToolError.Render("Error!")

	assert.Contains(t, running, "Running...")
	assert.Contains(t, complete, "Complete")
	assert.Contains(t, err, "Error!")
}

func TestStyles_StatusStylesRender(t *testing.T) {
	styles := DefaultStyles()

	connected := styles.StatusConnected.Render("Connected")
	disconnected := styles.StatusDisconnected.Render("Disconnected")
	reconnecting := styles.StatusReconnecting.Render("Reconnecting")

	assert.Contains(t, connected, "Connected")
	assert.Contains(t, disconnected, "Disconnected")
	assert.Contains(t, reconnecting, "Reconnecting")
}

func TestStyles_ContextStylesRender(t *testing.T) {
	styles := DefaultStyles()

	low := styles.ContextLow.Render("25%")
	medium := styles.ContextMedium.Render("60%")
	high := styles.ContextHigh.Render("90%")

	assert.Contains(t, low, "25%")
	assert.Contains(t, medium, "60%")
	assert.Contains(t, high, "90%")
}

func TestStyles_GeneralStylesRender(t *testing.T) {
	styles := DefaultStyles()

	muted := styles.Muted.Render("muted text")
	bold := styles.Bold.Render("bold text")
	accent := styles.Accent.Render("accent text")

	assert.Contains(t, muted, "muted text")
	assert.Contains(t, bold, "bold text")
	assert.Contains(t, accent, "accent text")
}

func TestStyles_SidebarStylesRender(t *testing.T) {
	styles := DefaultStyles()

	title := styles.SidebarTitle.Render("Title")
	content := styles.SidebarContent.Render("Content")
	tabActive := styles.SidebarTabActive.Render("Active")
	tabInactive := styles.SidebarTabInactive.Render("Inactive")

	assert.Contains(t, title, "Title")
	assert.Contains(t, content, "Content")
	assert.Contains(t, tabActive, "Active")
	assert.Contains(t, tabInactive, "Inactive")
}

func TestStyles_InputStyleRender(t *testing.T) {
	styles := DefaultStyles()

	input := styles.InputStyle.Render("Input area")
	assert.Contains(t, input, "Input area")
}

func TestStyles_ThinkingStylesRender(t *testing.T) {
	styles := DefaultStyles()

	bar := styles.ThinkingBar.Render("===")
	track := styles.ThinkingTrack.Render("[   ]")

	assert.Contains(t, bar, "===")
	assert.Contains(t, track, "[   ]")
}

func TestStyles_LabelStylesRender(t *testing.T) {
	styles := DefaultStyles()

	userLabel := styles.UserLabel.Render("You")
	assistantLabel := styles.AssistantLabel.Render("Claude")
	divider := styles.Divider.Render("---")

	assert.Contains(t, userLabel, "You")
	assert.Contains(t, assistantLabel, "Claude")
	assert.Contains(t, divider, "---")
}

func TestStyles_WhiteCursorRender(t *testing.T) {
	styles := DefaultStyles()

	cursor := styles.WhiteCursor.Render("_")
	assert.Contains(t, cursor, "_")
}
