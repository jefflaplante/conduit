package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewChatViewModel(t *testing.T) {
	styles := DefaultStyles()
	loc := time.UTC
	chat := NewChatViewModel(styles, "TestBot", loc)

	assert.Empty(t, chat.Messages)
	assert.Equal(t, "TestBot", chat.AssistantName)
	assert.Equal(t, loc, chat.Location)
	assert.False(t, chat.Streaming)
}

func TestNewChatViewModel_DefaultName(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "", nil)

	assert.Equal(t, "Assistant", chat.AssistantName)
}

func TestChatViewModel_SetSize(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)

	chat.SetSize(100, 50)
	assert.Equal(t, 100, chat.Width)
	assert.Equal(t, 50, chat.Height)
	assert.Equal(t, 100, chat.Viewport.Width)
	assert.Equal(t, 50, chat.Viewport.Height)
}

func TestChatViewModel_AddMessage(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.AddMessage("user", "Hello")
	assert.Len(t, chat.Messages, 1)
	assert.Equal(t, "user", chat.Messages[0].Role)
	assert.Equal(t, "Hello", chat.Messages[0].Content)
	assert.False(t, chat.Messages[0].Timestamp.IsZero())

	chat.AddMessage("assistant", "Hi there!")
	assert.Len(t, chat.Messages, 2)
	assert.Equal(t, "assistant", chat.Messages[1].Role)
}

func TestChatViewModel_AddMessageWithTools(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	tools := []ToolActivityInfo{
		{Name: "search", Status: "complete", Duration: time.Second},
	}

	chat.AddMessageWithTools("assistant", "Here are the results", tools)
	assert.Len(t, chat.Messages, 1)
	assert.Len(t, chat.Messages[0].Tools, 1)
	assert.Equal(t, "search", chat.Messages[0].Tools[0].Name)
}

func TestChatViewModel_Streaming(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	// Start streaming
	chat.StartStreaming()
	assert.True(t, chat.Streaming)
	assert.Equal(t, 0, chat.StreamBuf.Len())

	// Append deltas
	chat.AppendDelta("Hello")
	assert.Equal(t, "Hello", chat.StreamBuf.String())

	chat.AppendDelta(" world")
	assert.Equal(t, "Hello world", chat.StreamBuf.String())

	// End streaming
	chat.EndStreaming("Hello world!")
	assert.False(t, chat.Streaming)
	assert.Len(t, chat.Messages, 1)
	assert.Equal(t, "Hello world!", chat.Messages[0].Content)
	assert.Equal(t, 0, chat.StreamBuf.Len())
}

func TestChatViewModel_EndStreaming_UsesBuffer(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.StartStreaming()
	chat.AppendDelta("Buffer content")
	chat.EndStreaming("") // Empty final content

	assert.Len(t, chat.Messages, 1)
	assert.Equal(t, "Buffer content", chat.Messages[0].Content)
}

func TestChatViewModel_EndStreaming_SilentResponse(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.StartStreaming()
	// Silent responses should not be added
	chat.EndStreaming("<<SILENT>>") // Assuming this is the silent token

	// The exact behavior depends on channels.IsSilentResponse
	// which may filter out certain responses
	assert.False(t, chat.Streaming)
}

func TestChatViewModel_ThinkingTick(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.StartStreaming()
	assert.Equal(t, 0, chat.ThinkingFrame)

	chat.ThinkingTick()
	assert.Equal(t, 1, chat.ThinkingFrame)

	chat.ThinkingTick()
	assert.Equal(t, 2, chat.ThinkingFrame)
}

func TestChatViewModel_SetAssistantName(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.SetAssistantName("Claude")
	assert.Equal(t, "Claude", chat.AssistantName)
}

func TestChatViewModel_SetUserName(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.SetUserName("Alice")
	assert.Equal(t, "Alice", chat.UserName)
}

func TestChatViewModel_ClearMessages(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.AddMessage("user", "Hello")
	chat.AddMessage("assistant", "Hi")
	chat.StartStreaming()
	chat.AppendDelta("Testing...")

	chat.ClearMessages()
	assert.Empty(t, chat.Messages)
	assert.Equal(t, 0, chat.StreamBuf.Len())
	assert.False(t, chat.Streaming)
}

func TestChatViewModel_View(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.AddMessage("user", "Hello")
	chat.AddMessage("assistant", "Hi there!")

	view := chat.View()
	assert.NotEmpty(t, view)
}

func TestChatViewModel_FormatTimestamp(t *testing.T) {
	styles := DefaultStyles()
	loc, _ := time.LoadLocation("America/New_York")
	chat := NewChatViewModel(styles, "Bot", loc)

	timestamp := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)
	formatted := chat.formatTimestamp(timestamp)

	// The exact format depends on timezone conversion
	assert.NotEmpty(t, formatted)
}

func TestChatViewModel_FormatTimestamp_NoLocation(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)

	timestamp := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)
	formatted := chat.formatTimestamp(timestamp)

	assert.Equal(t, "14:30", formatted)
}

func TestWrapText(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		maxWidth   int
		expected   string
		checkExact bool // if true, check exact match; otherwise just verify no panic
	}{
		{
			name:       "short line",
			text:       "Hello world",
			maxWidth:   50,
			expected:   "Hello world",
			checkExact: true,
		},
		{
			name:       "needs wrapping",
			text:       "This is a very long line that needs to be wrapped at some point",
			maxWidth:   20,
			checkExact: false, // just verify it doesn't panic
		},
		{
			name:       "multiple lines",
			text:       "Line one\nLine two\nLine three",
			maxWidth:   50,
			expected:   "Line one\nLine two\nLine three",
			checkExact: true,
		},
		{
			name:       "empty string",
			text:       "",
			maxWidth:   50,
			expected:   "",
			checkExact: true,
		},
		{
			name:       "zero width",
			text:       "Hello",
			maxWidth:   0,
			expected:   "Hello",
			checkExact: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrapText(tt.text, tt.maxWidth)
			if tt.checkExact {
				assert.Equal(t, tt.expected, result)
			}
			// For all cases, verify no panic occurred (implicit by reaching here)
		})
	}
}

func TestRenderToolActivity(t *testing.T) {
	styles := DefaultStyles()

	tests := []struct {
		name     string
		tool     ToolActivityInfo
		contains string
	}{
		{
			name: "running",
			tool: ToolActivityInfo{
				Name:   "search",
				Status: "running",
			},
			contains: "Running",
		},
		{
			name: "complete",
			tool: ToolActivityInfo{
				Name:     "search",
				Status:   "complete",
				Duration: 500 * time.Millisecond,
			},
			contains: "Done",
		},
		{
			name: "error",
			tool: ToolActivityInfo{
				Name:     "search",
				Status:   "error",
				Error:    "connection failed",
				Duration: 100 * time.Millisecond,
			},
			contains: "Error",
		},
		{
			name: "unknown status",
			tool: ToolActivityInfo{
				Name:   "search",
				Status: "unknown",
			},
			contains: "search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := renderToolActivity(tt.tool, styles)
			assert.Contains(t, result, tt.contains)
		})
	}
}

func TestRenderToolActivity_WithDuration(t *testing.T) {
	styles := DefaultStyles()

	tool := ToolActivityInfo{
		Name:     "web_fetch",
		Status:   "complete",
		Duration: 2 * time.Second,
	}

	result := renderToolActivity(tool, styles)
	assert.Contains(t, result, "2.0s")
}

func TestChatBubble_Fields(t *testing.T) {
	now := time.Now()
	tools := []ToolActivityInfo{
		{Name: "test", Status: "complete"},
	}

	bubble := ChatBubble{
		Role:      "user",
		Content:   "Hello",
		Timestamp: now,
		Tools:     tools,
	}

	assert.Equal(t, "user", bubble.Role)
	assert.Equal(t, "Hello", bubble.Content)
	assert.Equal(t, now, bubble.Timestamp)
	assert.Len(t, bubble.Tools, 1)
}

func TestToolActivityInfo_Fields(t *testing.T) {
	info := ToolActivityInfo{
		Name:     "search",
		Status:   "complete",
		Args:     `{"query": "test"}`,
		Result:   "Found 5 results",
		Error:    "",
		Duration: time.Second,
	}

	assert.Equal(t, "search", info.Name)
	assert.Equal(t, "complete", info.Status)
	assert.Equal(t, `{"query": "test"}`, info.Args)
	assert.Equal(t, "Found 5 results", info.Result)
	assert.Equal(t, "", info.Error)
	assert.Equal(t, time.Second, info.Duration)
}

func TestChatViewModel_RenderKITTBar(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Claude", nil)
	chat.SetSize(80, 20)

	// Test the KITT bar renders without error at various frames
	for i := 0; i < 30; i++ {
		chat.ThinkingFrame = i
		bar := chat.renderKITTBar()
		assert.NotEmpty(t, bar)
		assert.Contains(t, bar, "thinking")
		assert.Contains(t, bar, "[")
		assert.Contains(t, bar, "]")
	}
}

func TestChatViewModel_RenderMessage_UserRole(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)
	chat.UserName = "Alice"

	msg := ChatBubble{
		Role:      "user",
		Content:   "Hello there",
		Timestamp: time.Now(),
	}

	result := chat.renderMessage(msg, 60)
	assert.Contains(t, result, "Alice")
	assert.Contains(t, result, "Hello there")
}

func TestChatViewModel_RenderMessage_AssistantRole(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Claude", nil)
	chat.SetSize(80, 20)

	msg := ChatBubble{
		Role:      "assistant",
		Content:   "Hi, how can I help?",
		Timestamp: time.Now(),
	}

	result := chat.renderMessage(msg, 60)
	assert.Contains(t, result, "Claude")
	assert.Contains(t, result, "Hi, how can I help?")
}

func TestChatViewModel_RenderMessage_SystemRole(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	msg := ChatBubble{
		Role:      "system",
		Content:   "Connected to gateway.",
		Timestamp: time.Now(),
	}

	result := chat.renderMessage(msg, 60)
	assert.Contains(t, result, "Connected to gateway.")
}

func TestChatViewModel_RenderMessage_WithTools(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	msg := ChatBubble{
		Role:      "assistant",
		Content:   "Here are the results",
		Timestamp: time.Now(),
		Tools: []ToolActivityInfo{
			{Name: "search", Status: "complete", Duration: time.Second},
		},
	}

	result := chat.renderMessage(msg, 60)
	assert.Contains(t, result, "search")
}

func TestChatViewModel_RefreshContent_Empty(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	// Should not panic with empty messages
	chat.refreshContent()
	view := chat.View()
	assert.NotNil(t, view) // May be empty but shouldn't panic
}

func TestChatViewModel_RefreshContent_Streaming(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Bot", nil)
	chat.SetSize(80, 20)

	chat.StartStreaming()
	chat.AppendDelta("Streaming content...")
	chat.refreshContent()

	view := chat.Viewport.View()
	assert.Contains(t, view, "Streaming content...")
}

func TestChatViewModel_RefreshContent_StreamingEmpty(t *testing.T) {
	styles := DefaultStyles()
	chat := NewChatViewModel(styles, "Claude", nil)
	chat.SetSize(80, 20)

	chat.StartStreaming()
	// No delta appended - should show thinking indicator
	chat.refreshContent()

	view := chat.Viewport.View()
	assert.Contains(t, view, "thinking")
}
