package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Styles holds all the TUI styling definitions
type Styles struct {
	// App layout
	App lipgloss.Style

	// Tab bar
	TabActive   lipgloss.Style
	TabInactive lipgloss.Style
	TabUnread   lipgloss.Style
	TabBar      lipgloss.Style

	// Chat bubbles
	UserBubble      lipgloss.Style
	AssistantBubble lipgloss.Style
	SystemBubble    lipgloss.Style
	UserLabel       lipgloss.Style
	AssistantLabel  lipgloss.Style
	Divider         lipgloss.Style

	// Tool activity
	ToolRunning  lipgloss.Style
	ToolComplete lipgloss.Style
	ToolError    lipgloss.Style

	// Sidebar
	SidebarBorder      lipgloss.Style
	SidebarTitle       lipgloss.Style
	SidebarContent     lipgloss.Style
	SidebarTabActive   lipgloss.Style
	SidebarTabInactive lipgloss.Style

	// Status bar
	StatusBar          lipgloss.Style
	StatusConnected    lipgloss.Style
	StatusDisconnected lipgloss.Style
	StatusReconnecting lipgloss.Style

	// Input
	InputStyle lipgloss.Style

	// Context usage (status bar)
	ContextLow    lipgloss.Style // < 50%
	ContextMedium lipgloss.Style // 50-80%
	ContextHigh   lipgloss.Style // > 80%

	// Thinking indicator (KITT scanner)
	ThinkingBar   lipgloss.Style
	ThinkingTrack lipgloss.Style

	// General
	Muted       lipgloss.Style
	Bold        lipgloss.Style
	Accent      lipgloss.Style
	WhiteCursor lipgloss.Style
}

// DefaultStyles creates the default style set using the default renderer.
func DefaultStyles() Styles {
	return NewStyles(lipgloss.DefaultRenderer())
}

// NewStyles creates the style set using the given renderer.
// Over SSH, pass the renderer from wishbubbletea.MakeRenderer(sess)
// so that styles emit ANSI colors appropriate for the SSH client's terminal.
func NewStyles(r *lipgloss.Renderer) Styles {
	return Styles{
		App: r.NewStyle(),

		// Tab bar
		TabActive: r.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 2),
		TabInactive: r.NewStyle().
			Foreground(lipgloss.Color("245")).
			Padding(0, 2),
		TabUnread: r.NewStyle().
			Bold(true).
			Underline(true).
			Foreground(lipgloss.Color("212")).
			Padding(0, 2),
		TabBar: r.NewStyle().
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("238")),

		// Chat bubbles
		UserBubble: r.NewStyle().
			Foreground(lipgloss.Color("75")).
			Padding(0, 1).
			MarginLeft(4),
		AssistantBubble: r.NewStyle().
			Foreground(lipgloss.Color("252")).
			Padding(0, 1).
			MarginRight(4),
		SystemBubble: r.NewStyle().
			Foreground(lipgloss.Color("244")).
			Italic(true).
			Padding(0, 1),
		UserLabel: r.NewStyle().
			Foreground(lipgloss.Color("75")).
			Bold(true),
		AssistantLabel: r.NewStyle().
			Foreground(lipgloss.Color("213")).
			Bold(true),
		Divider: r.NewStyle().
			Foreground(lipgloss.Color("238")),

		// Tool activity
		ToolRunning: r.NewStyle().
			Foreground(lipgloss.Color("81")),
		ToolComplete: r.NewStyle().
			Foreground(lipgloss.Color("76")),
		ToolError: r.NewStyle().
			Foreground(lipgloss.Color("196")),

		// Sidebar
		SidebarBorder: r.NewStyle().
			BorderLeft(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1),
		SidebarTitle: r.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("213")).
			MarginBottom(1),
		SidebarContent: r.NewStyle().
			Foreground(lipgloss.Color("252")),
		SidebarTabActive: r.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Underline(true),
		SidebarTabInactive: r.NewStyle().
			Foreground(lipgloss.Color("245")),

		// Status bar
		StatusBar: r.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1),
		StatusConnected: r.NewStyle().
			Foreground(lipgloss.Color("76")).
			Bold(true),
		StatusDisconnected: r.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true),
		StatusReconnecting: r.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true),

		// Input
		InputStyle: r.NewStyle().
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("238")),

		// Context usage (status bar)
		ContextLow: r.NewStyle().
			Foreground(lipgloss.Color("76")), // green
		ContextMedium: r.NewStyle().
			Foreground(lipgloss.Color("214")), // orange
		ContextHigh: r.NewStyle().
			Foreground(lipgloss.Color("196")), // red

		// Thinking indicator (KITT scanner)
		ThinkingBar: r.NewStyle().
			Foreground(lipgloss.Color("213")).
			Bold(true),
		ThinkingTrack: r.NewStyle().
			Foreground(lipgloss.Color("238")),

		// General
		Muted: r.NewStyle().
			Foreground(lipgloss.Color("245")),
		Bold: r.NewStyle().
			Bold(true),
		Accent: r.NewStyle().
			Foreground(lipgloss.Color("213")),
		WhiteCursor: r.NewStyle().
			Foreground(lipgloss.Color("15")),
	}
}
