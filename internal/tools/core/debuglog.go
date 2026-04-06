package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"conduit/internal/tools/debuglog"
	"conduit/internal/tools/types"
)

// DebugLogTool lets the AI inspect the in-memory debug log ring buffer.
type DebugLogTool struct {
	services *types.ToolServices
	buffer   *debuglog.RingBuffer
}

func NewDebugLogTool(services *types.ToolServices, buffer *debuglog.RingBuffer) *DebugLogTool {
	return &DebugLogTool{services: services, buffer: buffer}
}

func (t *DebugLogTool) Name() string { return "DebugLog" }

func (t *DebugLogTool) Description() string {
	return "Inspect the in-memory debug log of recent tool calls, LLM requests, and model thinking. " +
		"Entries are kept in a rolling buffer and never written to disk logs."
}

func (t *DebugLogTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"dump", "clear", "status"},
				"description": "Operation to perform on the debug log",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of entries to return (default: 50, max: 500)",
			},
			"filter": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"tool_start", "tool_complete", "tool_error", "thinking", "llm_request", "llm_response"},
				"description": "Only return entries of this type",
			},
		},
		"required": []string{"action"},
	}
}

func (t *DebugLogTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"dump": {
			Description:    "Dump recent debug log entries from the ring buffer",
			OptionalParams: []string{"limit", "filter"},
			Returns:        "Array of log entries with timestamps, tool names, args, results, and durations",
		},
		"clear": {
			Description: "Clear the debug log ring buffer",
			Returns:     "Confirmation",
		},
		"status": {
			Description: "Get current buffer status (entry count, capacity)",
			Returns:     "Entry count and capacity",
		},
	}
}

func (t *DebugLogTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action, _ := args["action"].(string)
	if action == "" {
		action = "status"
	}

	switch action {
	case "dump":
		return t.dump(args)
	case "clear":
		return t.clear()
	case "status":
		return t.status()
	default:
		return types.NewErrorResult("invalid_action", fmt.Sprintf("unknown action: %s", action)), nil
	}
}

func (t *DebugLogTool) dump(args map[string]interface{}) (*types.ToolResult, error) {
	limit := 50
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if limit > 500 {
		limit = 500
	}

	filterType := ""
	if f, ok := args["filter"].(string); ok {
		filterType = f
	}

	var entries []debuglog.Entry
	if filterType != "" {
		et := debuglog.EntryType(filterType)
		all := t.buffer.Entries(func(e debuglog.Entry) bool {
			return e.Type == et
		})
		// Apply limit to filtered results
		if limit > 0 && limit < len(all) {
			entries = all[len(all)-limit:]
		} else {
			entries = all
		}
	} else {
		entries = t.buffer.Last(limit)
	}

	if len(entries) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "Debug log is empty.",
		}, nil
	}

	// Format entries as readable text + include JSON data
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Debug log: %d entries (of %d total in buffer)\n\n", len(entries), t.buffer.Len()))

	for _, e := range entries {
		ts := e.Timestamp.Format("15:04:05.000")
		switch e.Type {
		case debuglog.EntryToolStart:
			argsStr := summarizeArgs(e.Args)
			sb.WriteString(fmt.Sprintf("[%s] ▶ TOOL %s %s\n", ts, e.ToolName, argsStr))
		case debuglog.EntryToolComplete:
			resultStr := truncateStr(e.Result, 200)
			sb.WriteString(fmt.Sprintf("[%s] ✓ TOOL %s (%s) %s\n", ts, e.ToolName, e.Duration, resultStr))
		case debuglog.EntryToolError:
			sb.WriteString(fmt.Sprintf("[%s] ✗ TOOL %s (%s) ERROR: %s\n", ts, e.ToolName, e.Duration, e.Error))
		case debuglog.EntryThinking:
			sb.WriteString(fmt.Sprintf("[%s] 💭 %s\n", ts, e.Result))
		case debuglog.EntryLLMRequest:
			sb.WriteString(fmt.Sprintf("[%s] → LLM %s\n", ts, e.Result))
		case debuglog.EntryLLMResponse:
			sb.WriteString(fmt.Sprintf("[%s] ← LLM %s (%s)\n", ts, e.Result, e.Duration))
		default:
			sb.WriteString(fmt.Sprintf("[%s] ? %s %s\n", ts, e.Type, e.Result))
		}
	}

	// Also provide structured data
	jsonData, _ := json.Marshal(entries)
	data := map[string]interface{}{
		"entry_count":  len(entries),
		"buffer_total": t.buffer.Len(),
	}

	return &types.ToolResult{
		Success: true,
		Content: sb.String() + "\nStructured entries (JSON):\n" + string(jsonData),
		Data:    data,
	}, nil
}

func (t *DebugLogTool) clear() (*types.ToolResult, error) {
	count := t.buffer.Len()
	t.buffer.Clear()
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Debug log cleared. %d entries removed.", count),
		Data: map[string]interface{}{
			"cleared":   count,
			"timestamp": time.Now(),
		},
	}, nil
}

func (t *DebugLogTool) status() (*types.ToolResult, error) {
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Debug log: %d entries in buffer (capacity: %d)", t.buffer.Len(), t.buffer.Len()),
		Data: map[string]interface{}{
			"entries":  t.buffer.Len(),
			"capacity": debuglog.DefaultCapacity,
		},
	}, nil
}

// summarizeArgs produces a compact string of tool arguments for display.
func summarizeArgs(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		vs := fmt.Sprintf("%v", v)
		vs = truncateStr(vs, 120)
		parts = append(parts, fmt.Sprintf("%s=%s", k, vs))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// SelfTest implements types.SelfTester for DebugLogTool.
func (t *DebugLogTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check RingBuffer dependency
	bufferDep := types.DependencyStatus{
		Name:     "RingBuffer",
		Required: true,
	}

	if t.buffer == nil {
		bufferDep.Available = false
		bufferDep.Status = "not_configured"
		bufferDep.Message = "RingBuffer not provided"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Debug log ring buffer is not available"
		result.Suggestions = []string{
			"Ensure DebugLogTool is initialized with a RingBuffer",
			"Check that debug logging is enabled in config",
		}
	} else {
		bufferDep.Available = true
		bufferDep.Status = "ready"

		currentLen := t.buffer.Len()
		bufferDep.Message = fmt.Sprintf("%d entries in buffer (capacity: %d)", currentLen, debuglog.DefaultCapacity)

		result.Capabilities = []string{"dump", "clear", "status"}
		result.Message = "Debug log tool is fully functional"

		if opts.Verbose {
			result.Details = map[string]interface{}{
				"buffer_entries":  currentLen,
				"buffer_capacity": debuglog.DefaultCapacity,
			}

			// Include entry type breakdown if there are entries
			if currentLen > 0 {
				entries := t.buffer.Entries(nil)
				typeCounts := map[string]int{}
				for _, e := range entries {
					typeCounts[string(e.Type)]++
				}
				result.Details["entry_types"] = typeCounts
			}
		}
	}
	deps = append(deps, bufferDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Dump recent entries",
				Description: "Show the last 50 debug log entries",
				Args: map[string]interface{}{
					"action": "dump",
					"limit":  50,
				},
				Expected: "Recent tool calls, LLM requests, and thinking entries",
			},
			{
				Name:        "Filter by type",
				Description: "Show only tool error entries",
				Args: map[string]interface{}{
					"action": "dump",
					"filter": "tool_error",
				},
				Expected: "Only entries of type tool_error",
			},
			{
				Name:        "Check status",
				Description: "Get current buffer status",
				Args: map[string]interface{}{
					"action": "status",
				},
				Expected: "Entry count and capacity",
			},
		}
	}

	return result
}
