package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// SidebarTab represents the active tab in the sidebar
type SidebarTab int

const (
	SidebarTabSession SidebarTab = iota
	SidebarTabTools
	SidebarTabStatus
)

// SidebarModel manages the information sidebar
type SidebarModel struct {
	ActiveTab SidebarTab
	Width     int
	Height    int
	Visible   bool
	Styles    Styles

	// Session info
	SessionKey   string
	MessageCount int
	Model        string
	UserID       string

	// Context usage
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ContextWindow    int
	ContextPercent   float64

	// Cost tracking
	RequestCost float64
	SessionCost float64

	// Status info
	Connected  bool
	GatewayURL string

	// Gateway metadata (from GatewayInfo)
	Version       string
	GitCommit     string
	UptimeSeconds int64
	ModelAliases  map[string]string
	ToolCount     int
	SkillCount    int

	// Per-session state
	SessionState     string // "idle", "processing", "tool: X", "error"
	LastResponseTime time.Duration
	SessionCreatedAt time.Time

	// Tool activity
	ActiveTools []ToolActivityInfo
}

// NewSidebarModel creates a new sidebar
func NewSidebarModel(styles Styles) SidebarModel {
	return SidebarModel{
		ActiveTab: SidebarTabSession,
		Width:     40,
		Visible:   false,
		Styles:    styles,
	}
}

// CycleTab cycles to the next sidebar tab
func (s *SidebarModel) CycleTab() {
	s.ActiveTab = (s.ActiveTab + 1) % 3
}

// View renders the sidebar
func (s SidebarModel) View() string {
	if !s.Visible {
		return ""
	}

	var sb strings.Builder

	// Tab header
	tabs := []string{"Session", "Tools", "Status"}
	var tabLine []string
	for i, tab := range tabs {
		if SidebarTab(i) == s.ActiveTab {
			tabLine = append(tabLine, s.Styles.SidebarTabActive.Render(tab))
		} else {
			tabLine = append(tabLine, s.Styles.SidebarTabInactive.Render(tab))
		}
	}
	sb.WriteString(strings.Join(tabLine, " | "))
	sb.WriteString("\n\n")

	// Tab content
	switch s.ActiveTab {
	case SidebarTabSession:
		sb.WriteString(s.renderSessionTab())
	case SidebarTabTools:
		sb.WriteString(s.renderToolsTab())
	case SidebarTabStatus:
		sb.WriteString(s.renderStatusTab())
	}

	content := sb.String()

	return s.Styles.SidebarBorder.
		Width(s.Width).
		Height(s.Height).
		Render(content)
}

func (s SidebarModel) renderSessionTab() string {
	var sb strings.Builder
	sb.WriteString(s.Styles.SidebarTitle.Render("Session Info"))
	sb.WriteString("\n")

	key := s.SessionKey
	if key == "" {
		key = "(none)"
	}
	if len(key) > s.Width-4 {
		key = key[:s.Width-7] + "..."
	}
	sb.WriteString(fmt.Sprintf("Key: %s\n", key))
	sb.WriteString(fmt.Sprintf("Messages: %d\n", s.MessageCount))

	model := s.Model
	if model == "" {
		model = "sonnet (default)"
	}
	sb.WriteString(fmt.Sprintf("Model: %s\n", model))

	if s.UserID != "" {
		sb.WriteString(fmt.Sprintf("User: %s\n", s.UserID))
	}

	// Context usage section
	sb.WriteString("\n")
	sb.WriteString(s.Styles.SidebarTitle.Render("Context"))
	sb.WriteString("\n")

	if s.TotalTokens > 0 {
		// Visual context bar
		barWidth := s.Width - 8 // room for [, ], space, percent
		sb.WriteString(renderContextBar(s.Styles, s.ContextPercent, barWidth))
		sb.WriteString("\n")

		// Token counts
		sb.WriteString(fmt.Sprintf("Prompt:     %s\n", formatSidebarNumber(s.PromptTokens)))
		sb.WriteString(fmt.Sprintf("Completion: %s\n", formatSidebarNumber(s.CompletionTokens)))
		sb.WriteString(fmt.Sprintf("Total:      %s\n", formatSidebarNumber(s.TotalTokens)))

		if s.ContextWindow > 0 {
			sb.WriteString(fmt.Sprintf("Window:     %s\n", formatSidebarNumber(s.ContextWindow)))
		}
	} else {
		sb.WriteString(s.Styles.Muted.Render("No usage data yet"))
	}

	// Cost section
	if s.SessionCost > 0 {
		sb.WriteString("\n")
		sb.WriteString(s.Styles.SidebarTitle.Render("Cost"))
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("Session: $%.4f\n", s.SessionCost))
		if s.RequestCost > 0 {
			sb.WriteString(fmt.Sprintf("Last:    $%.4f\n", s.RequestCost))
		}
	}

	return sb.String()
}

// renderContextBar renders a visual context usage bar like [========--------] 42%
func renderContextBar(styles Styles, percent float64, width int) string {
	if width < 4 {
		width = 4
	}

	pct := percent
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
	label := fmt.Sprintf(" %d%%", int(pct))

	var style lipgloss.Style
	switch {
	case pct > 80:
		style = styles.ContextHigh
	case pct > 50:
		style = styles.ContextMedium
	default:
		style = styles.ContextLow
	}

	return style.Render("["+bar+"]") + label
}

// formatSidebarNumber formats an integer with comma separators (e.g., 1,234,567)
func formatSidebarNumber(n int) string {
	if n < 0 {
		return "-" + formatSidebarNumber(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	var result strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		result.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if result.Len() > 0 {
			result.WriteByte(',')
		}
		result.WriteString(s[i : i+3])
	}
	return result.String()
}

func (s SidebarModel) renderToolsTab() string {
	var sb strings.Builder
	sb.WriteString(s.Styles.SidebarTitle.Render("Tool Activity"))
	sb.WriteString("\n")

	if len(s.ActiveTools) == 0 {
		sb.WriteString(s.Styles.Muted.Render("No recent activity"))
		return sb.String()
	}

	for _, tool := range s.ActiveTools {
		sb.WriteString(renderToolDetail(tool, s.Styles, s.Width))
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderToolDetail renders a tool activity line plus a detail line for the sidebar.
func renderToolDetail(tool ToolActivityInfo, styles Styles, width int) string {
	line := renderToolActivity(tool, styles)

	// Build a detail string from args/result
	maxDetail := width - 6
	if maxDetail < 10 {
		maxDetail = 10
	}

	var detail string
	switch tool.Status {
	case "complete":
		if tool.Result != "" {
			detail = tool.Result
		} else if tool.Args != "" {
			detail = tool.Args
		}
	default:
		detail = tool.Args
	}

	if detail != "" {
		// Collapse newlines to spaces for single-line display
		detail = strings.ReplaceAll(detail, "\n", " ")
		if len(detail) > maxDetail {
			detail = detail[:maxDetail-3] + "..."
		}
		line += "\n" + styles.Muted.Render("    "+detail)
	}

	return line
}

func (s SidebarModel) renderStatusTab() string {
	var sb strings.Builder

	// Connection section
	sb.WriteString(s.Styles.SidebarTitle.Render("Connection"))
	sb.WriteString("\n")

	if s.Connected {
		sb.WriteString(s.Styles.StatusConnected.Render("* Connected"))
	} else {
		sb.WriteString(s.Styles.StatusDisconnected.Render("* Disconnected"))
	}
	sb.WriteString("\n")

	if s.GatewayURL != "" {
		url := s.GatewayURL
		if len(url) > s.Width-4 {
			url = url[:s.Width-7] + "..."
		}
		sb.WriteString(fmt.Sprintf("URL: %s\n", url))
	}

	// Gateway section
	if s.Version != "" {
		sb.WriteString("\n")
		sb.WriteString(s.Styles.SidebarTitle.Render("Gateway"))
		sb.WriteString("\n")

		ver := s.Version
		if s.GitCommit != "" && len(s.GitCommit) >= 7 {
			ver += " (" + s.GitCommit[:7] + ")"
		}
		sb.WriteString(fmt.Sprintf("Version: %s\n", ver))

		if s.UptimeSeconds > 0 {
			sb.WriteString(fmt.Sprintf("Uptime:  %s\n", formatDuration(time.Duration(s.UptimeSeconds)*time.Second)))
		}
		if s.ToolCount > 0 {
			sb.WriteString(fmt.Sprintf("Tools:   %d\n", s.ToolCount))
		}
		if s.SkillCount > 0 {
			sb.WriteString(fmt.Sprintf("Skills:  %d\n", s.SkillCount))
		}
	}

	// Model aliases section
	if len(s.ModelAliases) > 0 {
		sb.WriteString("\n")
		sb.WriteString(s.Styles.SidebarTitle.Render("Models"))
		sb.WriteString("\n")
		aliases := make([]string, 0, len(s.ModelAliases))
		for alias := range s.ModelAliases {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			display := s.ModelAliases[alias]
			if display == "" {
				display = "(default)"
			}
			// Truncate long model IDs
			maxLen := s.Width - len(alias) - 6
			if maxLen > 0 && len(display) > maxLen {
				display = display[:maxLen-3] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s -> %s\n", alias, display))
		}
	}

	// Activity section
	sb.WriteString("\n")
	sb.WriteString(s.Styles.SidebarTitle.Render("Activity"))
	sb.WriteString("\n")

	state := s.SessionState
	if state == "" {
		state = "idle"
	}
	sb.WriteString(fmt.Sprintf("State: %s\n", state))

	if !s.SessionCreatedAt.IsZero() {
		age := time.Since(s.SessionCreatedAt)
		sb.WriteString(fmt.Sprintf("Age:   %s\n", formatDuration(age)))
	}

	if s.LastResponseTime > 0 {
		sb.WriteString(fmt.Sprintf("Last:  %s\n", formatResponseTime(s.LastResponseTime)))
	}

	return sb.String()
}

// formatDuration formats a duration in a human-readable compact form.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h > 0 {
		return fmt.Sprintf("%dd%dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}

// formatResponseTime formats a response time in a compact form.
func formatResponseTime(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// SidebarWidth returns the width of the sidebar when visible, 0 otherwise
func (s SidebarModel) SidebarWidth() int {
	if !s.Visible {
		return 0
	}
	return s.Width
}

// The sidebar border style includes a left border character, account for it
func sidebarBorderWidth() int {
	return 1 // lipgloss NormalBorder left border is 1 char
}

// TotalSidebarWidth returns the total width including border
func (s SidebarModel) TotalSidebarWidth() int {
	if !s.Visible {
		return 0
	}
	return s.Width + sidebarBorderWidth() + 2 // border + padding
}
