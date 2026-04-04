package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"conduit/internal/tools/types"
)

// BrainTool exposes the tiered memory (LTM + working + scratchpad) to the AI agent.
type BrainTool struct {
	services *types.ToolServices
}

// NewBrainTool creates a new Brain tool.
func NewBrainTool(services *types.ToolServices) *BrainTool {
	return &BrainTool{services: services}
}

func (t *BrainTool) Name() string { return "Brain" }

func (t *BrainTool) Description() string {
	return `Tiered memory system with long-term (LTM), working, and scratchpad storage.

Actions:
- store: Save a key-value fact to a memory tier (longterm or working)
- get: Retrieve a specific fact by key (checks working memory first, then LTM)
- recall: Fuzzy search across all tiers by query string, ranked by salience
- list: List all entries matching a key prefix
- delete: Remove a key from all tiers
- push: Push a value onto the per-user scratchpad stack (LIFO)
- pop: Pop the top value from the scratchpad stack
- peek: View the top value without removing it
- promote: Move a working-memory key to long-term storage
- consolidate: Sweep working memory — auto-promote high-salience keys, evict stale ones
- status: Report entry counts, scratchpad depth, and hottest keys

Typical Workflow:
1. Store important facts as they come up (tier=working for session-scoped, tier=longterm for persistent)
2. Use recall to find relevant facts when answering questions
3. Use push/pop/peek for temporary scratchpad notes during multi-step reasoning
4. Consolidate at session end to promote valuable working memory to LTM`
}

func (t *BrainTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"store", "get", "recall", "list", "delete", "push", "pop", "peek", "promote", "consolidate", "status"},
				"description": "Action to perform on the brain memory system",
			},
			"key": map[string]interface{}{
				"type":        "string",
				"description": "Memory key for store/get/delete/promote actions, or prefix for list action",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Value to store (for store action) or push onto scratchpad (for push action)",
			},
			"tier": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"longterm", "working"},
				"description": "Memory tier for store action (default: working)",
			},
			"source": map[string]interface{}{
				"type":        "string",
				"description": "Source label for store action (e.g. 'user', 'tool', 'observation')",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query for recall action",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum results for recall/list (default: 20)",
			},
			"auto_promote": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to auto-promote high-salience keys during consolidation (default: true)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *BrainTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return &types.ToolResult{Success: false, Error: "action is required"}, nil
	}

	brain := t.services.Brain
	if brain == nil {
		return &types.ToolResult{Success: false, Error: "brain service is not enabled"}, nil
	}

	switch action {
	case "store":
		return t.handleStore(ctx, args, brain)
	case "get":
		return t.handleGet(ctx, args, brain)
	case "recall":
		return t.handleRecall(ctx, args, brain)
	case "list":
		return t.handleList(ctx, args, brain)
	case "delete":
		return t.handleDelete(ctx, args, brain)
	case "push":
		return t.handlePush(ctx, args, brain)
	case "pop":
		return t.handlePop(ctx, args, brain)
	case "peek":
		return t.handlePeek(ctx, args, brain)
	case "promote":
		return t.handlePromote(ctx, args, brain)
	case "consolidate":
		return t.handleConsolidate(ctx, args, brain)
	case "status":
		return t.handleStatus(ctx, brain)
	default:
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("unknown action: %s", action)}, nil
	}
}

func (t *BrainTool) handleStore(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if key == "" || value == "" {
		return &types.ToolResult{Success: false, Error: "key and value are required for store"}, nil
	}
	tier := types.BrainTierWorking
	if t, ok := args["tier"].(string); ok && t != "" {
		switch strings.ToLower(t) {
		case "longterm":
			tier = types.BrainTierLongTerm
		case "working":
			tier = types.BrainTierWorking
		default:
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("invalid tier: %s (use longterm or working)", t)}, nil
		}
	}
	source, _ := args["source"].(string)
	if source == "" {
		source = "tool"
	}
	if err := brain.Store(ctx, key, value, tier, source); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("store failed: %v", err)}, nil
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Stored key=%q in %s memory (source=%s)", key, tier, source),
	}, nil
}

func (t *BrainTool) handleGet(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return &types.ToolResult{Success: false, Error: "key is required for get"}, nil
	}
	entry, err := brain.Get(ctx, key)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("get failed: %v", err)}, nil
	}
	if entry == nil {
		return &types.ToolResult{Success: true, Content: fmt.Sprintf("No entry found for key=%q", key)}, nil
	}
	data, _ := json.Marshal(entry)
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

func (t *BrainTool) handleRecall(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return &types.ToolResult{Success: false, Error: "query is required for recall"}, nil
	}
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	entries, err := brain.Recall(ctx, query, limit)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("recall failed: %v", err)}, nil
	}
	if len(entries) == 0 {
		return &types.ToolResult{Success: true, Content: fmt.Sprintf("No entries matching query=%q", query)}, nil
	}
	data, _ := json.Marshal(entries)
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

func (t *BrainTool) handleList(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	prefix, _ := args["key"].(string)
	entries, err := brain.List(ctx, prefix)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("list failed: %v", err)}, nil
	}
	if len(entries) == 0 {
		return &types.ToolResult{Success: true, Content: fmt.Sprintf("No entries with prefix=%q", prefix)}, nil
	}
	data, _ := json.Marshal(entries)
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

func (t *BrainTool) handleDelete(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return &types.ToolResult{Success: false, Error: "key is required for delete"}, nil
	}
	if err := brain.Delete(ctx, key); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("delete failed: %v", err)}, nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Deleted key=%q", key)}, nil
}

func (t *BrainTool) handlePush(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	value, _ := args["value"].(string)
	if value == "" {
		return &types.ToolResult{Success: false, Error: "value is required for push"}, nil
	}
	userID := types.RequestUserID(ctx)
	if err := brain.Push(ctx, userID, value); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("push failed: %v", err)}, nil
	}
	return &types.ToolResult{Success: true, Content: "Pushed value onto scratchpad"}, nil
}

func (t *BrainTool) handlePop(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	userID := types.RequestUserID(ctx)
	val, err := brain.Pop(ctx, userID)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("pop failed: %v", err)}, nil
	}
	return &types.ToolResult{Success: true, Content: val}, nil
}

func (t *BrainTool) handlePeek(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	userID := types.RequestUserID(ctx)
	val, err := brain.Peek(ctx, userID)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("peek failed: %v", err)}, nil
	}
	return &types.ToolResult{Success: true, Content: val}, nil
}

func (t *BrainTool) handlePromote(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return &types.ToolResult{Success: false, Error: "key is required for promote"}, nil
	}
	if err := brain.Promote(ctx, key); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("promote failed: %v", err)}, nil
	}
	return &types.ToolResult{Success: true, Content: fmt.Sprintf("Promoted key=%q to long-term memory", key)}, nil
}

func (t *BrainTool) handleConsolidate(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	autoPromote := true
	if ap, ok := args["auto_promote"].(bool); ok {
		autoPromote = ap
	}
	report, err := brain.Consolidate(ctx, autoPromote)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("consolidate failed: %v", err)}, nil
	}
	data, _ := json.Marshal(report)
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

func (t *BrainTool) handleStatus(ctx context.Context, brain types.BrainService) (*types.ToolResult, error) {
	status, err := brain.Status(ctx)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("status failed: %v", err)}, nil
	}
	data, _ := json.Marshal(status)
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}
