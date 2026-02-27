package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
)

// ChatBubble represents a single chat message
type ChatBubble struct {
	Role      string
	Content   string
	Timestamp time.Time
	Tools     []ToolActivityInfo
}

// ChatViewModel manages the chat message viewport
type ChatViewModel struct {
	Messages      []ChatBubble
	Viewport      viewport.Model
	Width         int
	Height        int
	Streaming     bool
	StreamBuf     strings.Builder
	Styles        Styles
	AssistantName string
	UserName      string
	Location      *time.Location
	ThinkingFrame int
}

// NewChatViewModel creates a new chat view
func NewChatViewModel(styles Styles, assistantName string, location *time.Location) ChatViewModel {
	vp := viewport.New(80, 20)
	vp.SetContent("")
	name := assistantName
	if name == "" {
		name = "Assistant"
	}
	return ChatViewModel{
		Viewport:      vp,
		Styles:        styles,
		AssistantName: name,
		Location:      location,
	}
}

// SetSize updates the viewport dimensions
func (c *ChatViewModel) SetSize(width, height int) {
	c.Width = width
	c.Height = height
	c.Viewport.Width = width
	c.Viewport.Height = height
	c.refreshContent()
}

// AddMessage adds a complete message to the chat
func (c *ChatViewModel) AddMessage(role, content string) {
	c.Messages = append(c.Messages, ChatBubble{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	c.refreshContent()
	c.Viewport.GotoBottom()
}

// AddMessageWithTools adds a message with associated tool activity
func (c *ChatViewModel) AddMessageWithTools(role, content string, tools []ToolActivityInfo) {
	c.Messages = append(c.Messages, ChatBubble{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
		Tools:     tools,
	})
	c.refreshContent()
	c.Viewport.GotoBottom()
}

// StartStreaming begins a streaming response
func (c *ChatViewModel) StartStreaming() {
	c.Streaming = true
	c.StreamBuf.Reset()
	c.ThinkingFrame = 0
	c.refreshContent()
	c.Viewport.GotoBottom()
}

// AppendDelta appends streamed text to the current response
func (c *ChatViewModel) AppendDelta(delta string) {
	c.StreamBuf.WriteString(delta)
	c.refreshContent()
	c.Viewport.GotoBottom()
}

// EndStreaming finalizes the streaming response
func (c *ChatViewModel) EndStreaming(finalContent string) {
	content := finalContent
	if content == "" {
		content = c.StreamBuf.String()
	}
	c.Streaming = false
	// Suppress silent response tokens that may have arrived via streamed deltas
	if isSilentContent(content) {
		content = ""
	}
	if content != "" {
		c.Messages = append(c.Messages, ChatBubble{
			Role:      "assistant",
			Content:   content,
			Timestamp: time.Now(),
		})
	}
	c.StreamBuf.Reset()
	c.refreshContent()
	c.Viewport.GotoBottom()
}

// isSilentContent returns true if the content contains silent response tokens
// (NO_REPLY or HEARTBEAT_OK) that should not be shown to the user.
func isSilentContent(content string) bool {
	upper := strings.ToUpper(strings.TrimSpace(content))
	return strings.Contains(upper, "NO_REPLY") || strings.Contains(upper, "HEARTBEAT_OK")
}

// ThinkingTick advances the KITT scanner animation by one frame and refreshes.
func (c *ChatViewModel) ThinkingTick() {
	c.ThinkingFrame++
	c.refreshContent()
	c.Viewport.GotoBottom()
}

// renderKITTBar renders a KITT-style bouncing scanner bar for the thinking indicator.
func (c *ChatViewModel) renderKITTBar() string {
	const trackWidth = 16
	const barWidth = 3

	// Bounce: frame goes 0..maxPos, maxPos+1..2*maxPos = returning
	maxPos := trackWidth - barWidth // 13
	cycle := 2 * maxPos             // 26 frames per full bounce
	pos := c.ThinkingFrame % cycle
	if pos > maxPos {
		pos = cycle - pos // bounce back
	}

	label := fmt.Sprintf("  %s is thinking  ", c.AssistantName)
	styledLabel := c.Styles.Muted.Render(label)

	// Build styled track with bright scanner segment
	var styled strings.Builder
	styled.WriteString(c.Styles.ThinkingTrack.Render("["))
	for i := 0; i < trackWidth; i++ {
		if i >= pos && i < pos+barWidth {
			styled.WriteString(c.Styles.ThinkingBar.Render("="))
		} else {
			styled.WriteString(c.Styles.ThinkingTrack.Render(" "))
		}
	}
	styled.WriteString(c.Styles.ThinkingTrack.Render("]"))

	return styledLabel + styled.String()
}

// SetAssistantName updates the displayed assistant name and refreshes the view
func (c *ChatViewModel) SetAssistantName(name string) {
	c.AssistantName = name
	c.refreshContent()
}

// SetUserName updates the displayed user name and refreshes the view
func (c *ChatViewModel) SetUserName(name string) {
	c.UserName = name
	c.refreshContent()
}

// formatTimestamp formats a timestamp in the configured timezone
func (c *ChatViewModel) formatTimestamp(t time.Time) string {
	if c.Location != nil {
		return t.In(c.Location).Format("15:04")
	}
	return t.Format("15:04")
}

// ClearMessages removes all messages
func (c *ChatViewModel) ClearMessages() {
	c.Messages = nil
	c.StreamBuf.Reset()
	c.Streaming = false
	c.refreshContent()
}

// refreshContent rebuilds the viewport content from messages
func (c *ChatViewModel) refreshContent() {
	var sb strings.Builder
	maxWidth := c.Width - 6 // padding
	if maxWidth < 20 {
		maxWidth = 20
	}

	for i, msg := range c.Messages {
		if i > 0 {
			sb.WriteString(c.Styles.Divider.Render(strings.Repeat("─", maxWidth)))
			sb.WriteString("\n")
		}
		sb.WriteString(c.renderMessage(msg, maxWidth))
		sb.WriteString("\n")
	}

	// Render streaming content if active
	if c.Streaming {
		streamedText := c.StreamBuf.String()
		if streamedText != "" {
			label := c.Styles.AssistantLabel.Render(c.AssistantName)
			sb.WriteString(label + "\n")
			wrapped := wrapText(streamedText, maxWidth)
			sb.WriteString(c.Styles.AssistantBubble.Render(wrapped))
			sb.WriteString(" _\n") // cursor indicator
		} else {
			sb.WriteString(c.renderKITTBar() + "\n")
		}
	}

	c.Viewport.SetContent(sb.String())
}

// renderMessage renders a single chat bubble
func (c *ChatViewModel) renderMessage(msg ChatBubble, maxWidth int) string {
	var sb strings.Builder

	// Render any tool activity before the message
	for _, tool := range msg.Tools {
		sb.WriteString(renderToolActivity(tool, c.Styles))
		sb.WriteString("\n")
	}

	switch msg.Role {
	case "user":
		userName := c.UserName
		if userName == "" {
			userName = "You"
		}
		label := c.Styles.UserLabel.Render(userName)
		ts := c.Styles.Muted.Render(c.formatTimestamp(msg.Timestamp))
		sb.WriteString(fmt.Sprintf("%s %s\n", label, ts))
		wrapped := wrapText(msg.Content, maxWidth)
		sb.WriteString(c.Styles.UserBubble.Render(wrapped))

	case "assistant":
		label := c.Styles.AssistantLabel.Render(c.AssistantName)
		ts := c.Styles.Muted.Render(c.formatTimestamp(msg.Timestamp))
		sb.WriteString(fmt.Sprintf("%s %s\n", label, ts))
		wrapped := wrapText(msg.Content, maxWidth)
		sb.WriteString(c.Styles.AssistantBubble.Render(wrapped))

	case "system":
		sb.WriteString(c.Styles.SystemBubble.Render(msg.Content))
	}

	return sb.String()
}

// View renders the chat viewport
func (c ChatViewModel) View() string {
	return c.Viewport.View()
}

// wrapText wraps text to fit within maxWidth
func wrapText(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return text
	}

	var result strings.Builder
	lines := strings.Split(text, "\n")

	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}

		// Simple word wrapping
		if len(line) <= maxWidth {
			result.WriteString(line)
			continue
		}

		words := strings.Fields(line)
		currentLine := ""
		for _, word := range words {
			if currentLine == "" {
				currentLine = word
			} else if len(currentLine)+1+len(word) <= maxWidth {
				currentLine += " " + word
			} else {
				result.WriteString(currentLine + "\n")
				currentLine = word
			}
		}
		if currentLine != "" {
			result.WriteString(currentLine)
		}
	}

	return result.String()
}

// renderToolActivity renders a tool activity indicator
func renderToolActivity(tool ToolActivityInfo, styles Styles) string {
	switch tool.Status {
	case "running":
		return styles.ToolRunning.Render(fmt.Sprintf("  > [%s] Running...", tool.Name))
	case "complete":
		dur := ""
		if tool.Duration > 0 {
			dur = fmt.Sprintf(" (%.1fs)", tool.Duration.Seconds())
		}
		return styles.ToolComplete.Render(fmt.Sprintf("  + [%s] Done%s", tool.Name, dur))
	case "error":
		dur := ""
		if tool.Duration > 0 {
			dur = fmt.Sprintf(" (%.1fs)", tool.Duration.Seconds())
		}
		return styles.ToolError.Render(fmt.Sprintf("  x [%s] Error%s: %s", tool.Name, dur, tool.Error))
	default:
		return styles.Muted.Render(fmt.Sprintf("  ? [%s] %s", tool.Name, tool.Status))
	}
}
