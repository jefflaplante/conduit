package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"conduit/internal/tools/types"
)

// BrainTool provides tiered cognitive memory operations.
type BrainTool struct {
	services *types.ToolServices
}

// NewBrainTool creates a new BrainTool.
func NewBrainTool(services *types.ToolServices) *BrainTool {
	return &BrainTool{services: services}
}

func (t *BrainTool) Name() string        { return "Brain" }
func (t *BrainTool) Description() string {
	return "Tiered cognitive memory: store, retrieve, search, and manage facts across long-term memory (persisted), working memory (session), and scratchpad (temporary stack)."
}

func (t *BrainTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"store", "get", "recall", "list", "delete", "push", "pop", "peek", "promote", "consolidate", "status"},
				"description": "Operation to perform",
			},
			"key":          map[string]interface{}{"type": "string", "description": "Dot-separated key (e.g. solar.panel_count)"},
			"value":        map[string]interface{}{"type": "string", "description": "Value to store or push"},
			"tier":         map[string]interface{}{"type": "string", "enum": []string{"longterm", "working"}, "description": "Memory tier (default: working)"},
			"query":        map[string]interface{}{"type": "string", "description": "Search query for recall"},
			"prefix":       map[string]interface{}{"type": "string", "description": "Key prefix for list (e.g. 'solar.')"},
			"limit":        map[string]interface{}{"type": "integer", "description": "Max results for recall (default: 20)"},
			"auto_promote": map[string]interface{}{"type": "boolean", "description": "Auto-promote during consolidation (default: true)"},
		},
		"required": []string{"action"},
	}
}

func (t *BrainTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"store":       {Description: "Store a key-value fact", RequiredParams: []string{"key", "value"}, OptionalParams: []string{"tier"}, Returns: "Confirmation"},
		"get":         {Description: "Retrieve a fact by key", RequiredParams: []string{"key"}, Returns: "Entry with value, tier, salience"},
		"recall":      {Description: "Search all tiers", RequiredParams: []string{"query"}, OptionalParams: []string{"limit"}, Returns: "Ranked entries"},
		"list":        {Description: "List entries by prefix", RequiredParams: []string{"prefix"}, Returns: "Matching entries"},
		"delete":      {Description: "Remove a key", RequiredParams: []string{"key"}, Returns: "Confirmation"},
		"push":        {Description: "Push to scratchpad", RequiredParams: []string{"value"}, Returns: "Confirmation"},
		"pop":         {Description: "Pop from scratchpad", Returns: "Value"},
		"peek":        {Description: "Peek scratchpad top", Returns: "Value"},
		"promote":     {Description: "Move working→longterm", RequiredParams: []string{"key"}, Returns: "Confirmation"},
		"consolidate": {Description: "Session-end sweep", OptionalParams: []string{"auto_promote"}, Returns: "Report"},
		"status":      {Description: "Brain status", Returns: "Counts and hottest keys"},
	}
}

func (t *BrainTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{Name: "Store", Description: "Cache a fact", Args: map[string]interface{}{"action": "store", "key": "solar.panel_count", "value": "30", "tier": "working"}},
		{Name: "Lookup", Description: "Check cache", Args: map[string]interface{}{"action": "get", "key": "solar.panel_count"}},
		{Name: "Search", Description: "Find facts", Args: map[string]interface{}{"action": "recall", "query": "solar"}},
	}
}

func (t *BrainTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Brain == nil {
		return types.NewErrorResult("not_available", "Brain service is not enabled"), nil
	}
	action, _ := args["action"].(string)
	if action == "" {
		action = "status"
	}
	switch action {
	case "store":
		return t.store(ctx, args)
	case "get":
		return t.get(ctx, args)
	case "recall":
		return t.recall(ctx, args)
	case "list":
		return t.list(ctx, args)
	case "delete":
		return t.del(ctx, args)
	case "push":
		return t.push(ctx, args)
	case "pop":
		return t.pop(ctx)
	case "peek":
		return t.peek(ctx)
	case "promote":
		return t.promote(ctx, args)
	case "consolidate":
		return t.consolidate(ctx, args)
	case "status":
		return t.status(ctx)
	default:
		return types.NewErrorResult("invalid_action", fmt.Sprintf("unknown action: %s", action)).
			WithAvailableValues([]string{"store", "get", "recall", "list", "delete", "push", "pop", "peek", "promote", "consolidate", "status"}), nil
	}
}

func (t *BrainTool) store(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return types.NewErrorResult("missing_param", "key is required").WithParameter("key", nil), nil
	}
	value, _ := args["value"].(string)
	if value == "" {
		return types.NewErrorResult("missing_param", "value is required").WithParameter("value", nil), nil
	}
	tierStr, _ := args["tier"].(string)
	if tierStr == "" {
		tierStr = "working"
	}
	tier := types.BrainTier(tierStr)
	if tier != types.BrainTierLongTerm && tier != types.BrainTierWorking {
		return types.NewErrorResult("invalid_param", "tier must be 'longterm' or 'working'").WithParameter("tier", tierStr), nil
	}
	if err := t.services.Brain.Store(ctx, key, value, tier, "tool"); err != nil {
		return types.NewErrorResult("store_failed", err.Error()), nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Stored %s=%s (tier: %s)", key, brainTruncate(value, 100), tierStr), Data: map[string]interface{}{"key": key, "tier": tierStr}}, nil
}

func (t *BrainTool) get(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return types.NewErrorResult("missing_param", "key is required").WithParameter("key", nil), nil
	}
	entry, err := t.services.Brain.Get(ctx, key)
	if err != nil {
		return types.NewErrorResult("get_failed", err.Error()), nil
	}
	if entry == nil {
		return &types.ToolResult{Success: true, Content: fmt.Sprintf("Key %q not found", key), Data: map[string]interface{}{"found": false}}, nil
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("%s = %s (tier: %s, salience: %.2f, accessed: %dx)", entry.Key, entry.Value, entry.Tier, entry.Salience, entry.AccessCount),
		Data: map[string]interface{}{"found": true, "key": entry.Key, "value": entry.Value, "tier": string(entry.Tier), "salience": entry.Salience, "access_count": entry.AccessCount},
	}, nil
}

func (t *BrainTool) recall(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return types.NewErrorResult("missing_param", "query is required").WithParameter("query", nil), nil
	}
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	entries, err := t.services.Brain.Recall(ctx, query, limit)
	if err != nil {
		return types.NewErrorResult("recall_failed", err.Error()), nil
	}
	if len(entries) == 0 {
		return &types.ToolResult{Success: true, Content: fmt.Sprintf("No results for %q", query), Data: map[string]interface{}{"count": 0}}, nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d results for %q:\n", len(entries), query))
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("  [%s] %s = %s (salience: %.2f)\n", e.Tier, e.Key, brainTruncate(e.Value, 80), e.Salience))
	}
	return &types.ToolResult{Success: true, Content: sb.String(), Data: map[string]interface{}{"count": len(entries)}}, nil
}

func (t *BrainTool) list(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	prefix, _ := args["prefix"].(string)
	entries, err := t.services.Brain.List(ctx, prefix)
	if err != nil {
		return types.NewErrorResult("list_failed", err.Error()), nil
	}
	if len(entries) == 0 {
		return &types.ToolResult{Success: true, Content: fmt.Sprintf("No entries with prefix %q", prefix), Data: map[string]interface{}{"count": 0}}, nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d entries matching %q:\n", len(entries), prefix))
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("  [%s] %s = %s\n", e.Tier, e.Key, brainTruncate(e.Value, 80)))
	}
	return &types.ToolResult{Success: true, Content: sb.String(), Data: map[string]interface{}{"count": len(entries)}}, nil
}

func (t *BrainTool) del(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return types.NewErrorResult("missing_param", "key is required").WithParameter("key", nil), nil
	}
	if err := t.services.Brain.Delete(ctx, key); err != nil {
		return types.NewErrorResult("delete_failed", err.Error()), nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Deleted %q", key)}, nil
}

func (t *BrainTool) push(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	value, _ := args["value"].(string)
	if value == "" {
		return types.NewErrorResult("missing_param", "value is required").WithParameter("value", nil), nil
	}
	userID := types.RequestUserID(ctx)
	if err := t.services.Brain.Push(ctx, userID, value); err != nil {
		return types.NewErrorResult("push_failed", err.Error()), nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Pushed to scratchpad: %s", brainTruncate(value, 100))}, nil
}

func (t *BrainTool) pop(ctx context.Context) (*types.ToolResult, error) {
	userID := types.RequestUserID(ctx)
	val, err := t.services.Brain.Pop(ctx, userID)
	if err != nil {
		return types.NewErrorResult("pop_failed", err.Error()), nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Popped: %s", val), Data: map[string]interface{}{"value": val}}, nil
}

func (t *BrainTool) peek(ctx context.Context) (*types.ToolResult, error) {
	userID := types.RequestUserID(ctx)
	val, err := t.services.Brain.Peek(ctx, userID)
	if err != nil {
		return types.NewErrorResult("peek_failed", err.Error()), nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Top: %s", val), Data: map[string]interface{}{"value": val}}, nil
}

func (t *BrainTool) promote(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return types.NewErrorResult("missing_param", "key is required").WithParameter("key", nil), nil
	}
	if err := t.services.Brain.Promote(ctx, key); err != nil {
		return types.NewErrorResult("promote_failed", err.Error()), nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Promoted %q to longterm", key)}, nil
}

func (t *BrainTool) consolidate(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	autoPromote := true
	if ap, ok := args["auto_promote"].(bool); ok {
		autoPromote = ap
	}
	report, err := t.services.Brain.Consolidate(ctx, autoPromote)
	if err != nil {
		return types.NewErrorResult("consolidate_failed", err.Error()), nil
	}
	data, _ := json.Marshal(report)
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Consolidation: promoted=%d evicted=%d ltm=%d", report.PromotedCount, report.EvictedCount, report.LTMSize),
		Data:    map[string]interface{}{"promoted": report.PromotedCount, "evicted": report.EvictedCount, "ltm_size": report.LTMSize, "report": string(data)},
	}, nil
}

func (t *BrainTool) status(ctx context.Context) (*types.ToolResult, error) {
	st, err := t.services.Brain.Status(ctx)
	if err != nil {
		return types.NewErrorResult("status_failed", err.Error()), nil
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Brain: LTM=%d WM=%d scratch=%d avg_salience=%.2f hottest=%s", st.LTMEntries, st.WMEntries, st.ScratchDepth, st.AvgSalience, strings.Join(st.HottestKeys, ", ")),
		Data:    map[string]interface{}{"ltm_entries": st.LTMEntries, "wm_entries": st.WMEntries, "scratch_depth": st.ScratchDepth, "avg_salience": st.AvgSalience, "hottest_keys": st.HottestKeys},
	}, nil
}

func brainTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
