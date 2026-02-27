package tui

import "time"

// ToolActivityInfo represents the state of a tool execution
type ToolActivityInfo struct {
	Name     string
	Status   string // "running", "complete", "error"
	Args     string
	Result   string
	Error    string
	Duration time.Duration
}
