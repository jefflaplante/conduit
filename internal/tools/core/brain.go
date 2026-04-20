package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"conduit/internal/tools/types"
)

// parseTTLString parses a TTL string accepting Go time.ParseDuration units
// plus the convenience suffixes "d" (days) and "w" (weeks). Returns zero and
// no error for empty input.
func parseTTLString(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if n := len(s); n >= 2 {
		last := s[n-1]
		if last == 'd' || last == 'w' {
			var count int
			if _, err := fmt.Sscanf(s[:n-1], "%d", &count); err != nil {
				return 0, fmt.Errorf("parse ttl %q: %w", s, err)
			}
			mult := 24 * time.Hour
			if last == 'w' {
				mult = 7 * 24 * time.Hour
			}
			return time.Duration(count) * mult, nil
		}
	}
	return time.ParseDuration(s)
}

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
- store: Save a single key-value fact to a memory tier (longterm or working). Optional 'ttl' (e.g. "24h", "7d", "2w") sets an expiry — the entry is skipped by get/recall after expiry and deleted during consolidate/prune.
- store_bulk: Save many key-value facts in one atomic transaction (array of {key, value, tier?, source?})
- get: Retrieve a specific fact by key (checks working memory first, then LTM)
- recall: Fuzzy search across all tiers by query string, ranked by salience. Optional 'context' param biases ranking toward entries whose key/value overlap with context tokens (keyword overlap only, no semantic similarity).
- list: List all entries matching a key prefix
- delete: Remove a key from all tiers
- push: Push a value onto the per-user scratchpad stack (LIFO)
- pop: Pop the top value from the scratchpad stack
- peek: View the top value without removing it
- promote: Move a working-memory key to long-term storage
- consolidate: Sweep working memory — auto-promote high-salience keys, evict stale ones
- status: Report entry counts, scratchpad depth, and both the hottest keys (by salience) and the coldest keys (by access_count)
- rem_cycle: Run REM sleep consolidation phases (triage, consolidate, prune, integrate, groom)

Auto-extraction from files:
Files read via the Read tool are scanned for HTML comment blocks of the form
"<!-- brain-extract ... /brain-extract -->". Each "key: \"value\"" line inside
the block is auto-stored into working memory (source="file:<relpath>") via
StoreBulk, so reference docs can publish facts without explicit tool calls.

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
				"enum":        []string{"store", "store_bulk", "get", "recall", "list", "delete", "push", "pop", "peek", "promote", "consolidate", "status", "rem_cycle"},
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
			"ttl": map[string]interface{}{
				"type":        "string",
				"description": "Optional TTL for store action. Accepts Go duration strings (\"24h\", \"90m\") plus \"Nd\" (days) and \"Nw\" (weeks). After expiry, the entry is hidden from get/recall and deleted during the next consolidate/prune sweep. Omit for no expiry (default).",
			},
			"source_prefix": map[string]interface{}{
				"type":        "string",
				"description": "Filter list results by source prefix (e.g. 'file:', 'skill:')",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query for recall action",
			},
			"context": map[string]interface{}{
				"type":        "string",
				"description": "Optional context for recall action — entries whose key/value overlap with context tokens get a ranking boost (keyword overlap only, never filters results).",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum results for recall/list (default: 20)",
			},
			"auto_promote": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to auto-promote high-salience keys during consolidation (default: true)",
			},
			"phases": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "REM cycle phases to run (default: [\"triage\", \"consolidate\", \"prune\", \"integrate\", \"groom\"])",
			},
			"dry_run": map[string]interface{}{
				"type":        "boolean",
				"description": "Preview REM cycle changes without applying (default: false)",
			},
			"force_groom": map[string]interface{}{
				"type":        "boolean",
				"description": "Re-extract groom data even if hashes match (default: false)",
			},
			"entries": map[string]interface{}{
				"type":        "array",
				"description": "Array of entries for store_bulk action (at least one required)",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"key":    map[string]interface{}{"type": "string", "description": "Memory key"},
						"value":  map[string]interface{}{"type": "string", "description": "Value to store"},
						"tier":   map[string]interface{}{"type": "string", "enum": []string{"longterm", "working"}, "description": "Memory tier (default: working)"},
						"source": map[string]interface{}{"type": "string", "description": "Source label (default: tool)"},
					},
					"required": []string{"key", "value"},
				},
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
	case "store_bulk":
		return t.handleStoreBulk(ctx, args, brain)
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
	case "rem_cycle":
		return t.handleREMCycle(ctx, args, brain)
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
	ttlStr, _ := args["ttl"].(string)
	ttl, err := parseTTLString(ttlStr)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("invalid ttl: %v", err)}, nil
	}
	if ttl > 0 {
		if err := brain.StoreWithTTL(ctx, key, value, tier, source, ttl); err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("store failed: %v", err)}, nil
		}
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Stored key=%q in %s memory (source=%s, ttl=%s)", key, tier, source, ttl),
		}, nil
	}
	if err := brain.Store(ctx, key, value, tier, source); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("store failed: %v", err)}, nil
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Stored key=%q in %s memory (source=%s)", key, tier, source),
	}, nil
}

func (t *BrainTool) handleStoreBulk(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	raw, ok := args["entries"].([]interface{})
	if !ok || len(raw) == 0 {
		return &types.ToolResult{Success: false, Error: "entries is required for store_bulk (non-empty array)"}, nil
	}
	entries := make([]types.BrainBulkEntry, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("entries[%d] must be an object", i)}, nil
		}
		key, _ := m["key"].(string)
		value, _ := m["value"].(string)
		if key == "" || value == "" {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("entries[%d]: key and value are required", i)}, nil
		}
		tier := types.BrainTierWorking
		if t, ok := m["tier"].(string); ok && t != "" {
			switch strings.ToLower(t) {
			case "longterm":
				tier = types.BrainTierLongTerm
			case "working":
				tier = types.BrainTierWorking
			default:
				return &types.ToolResult{Success: false, Error: fmt.Sprintf("entries[%d]: invalid tier %q (use longterm or working)", i, t)}, nil
			}
		}
		source, _ := m["source"].(string)
		if source == "" {
			source = "tool"
		}
		entries = append(entries, types.BrainBulkEntry{
			Key:    key,
			Value:  value,
			Tier:   tier,
			Source: source,
		})
	}
	if err := brain.StoreBulk(ctx, entries); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("store_bulk failed: %v", err)}, nil
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Stored %d entries in bulk", len(entries)),
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
	contextStr, _ := args["context"].(string)
	entries, err := brain.RecallWithContext(ctx, query, limit, contextStr)
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
	sourcePrefix, _ := args["source_prefix"].(string)
	entries, err := brain.List(ctx, prefix, sourcePrefix)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("list failed: %v", err)}, nil
	}
	if len(entries) == 0 {
		msg := fmt.Sprintf("No entries with prefix=%q", prefix)
		if sourcePrefix != "" {
			msg += fmt.Sprintf(" and source_prefix=%q", sourcePrefix)
		}
		return &types.ToolResult{Success: true, Content: msg}, nil
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

func (t *BrainTool) handleREMCycle(ctx context.Context, args map[string]interface{}, brain types.BrainService) (*types.ToolResult, error) {
	// Parse phases parameter — accept both short forms (triage, consolidate, prune, integrate, groom)
	// and full forms (triage, consolidation, pruning, integration, grooming)
	defaultPhases := []string{"triage", "consolidation", "pruning", "integration", "grooming"}
	phases := defaultPhases
	if p, ok := args["phases"].([]interface{}); ok {
		phases = make([]string, len(p))
		for i, v := range p {
			if s, ok := v.(string); ok {
				phases[i] = normalizePhase(s)
			} else {
				return &types.ToolResult{Success: false, Error: fmt.Sprintf("invalid phase at index %d: must be string", i)}, nil
			}
		}
	}

	// Parse dry_run parameter
	dryRun, _ := args["dry_run"].(bool)

	// Check if REM cycle runner is available
	remRunner := t.services.REMCycle
	if remRunner == nil {
		return &types.ToolResult{Success: false, Error: "REM cycle not available — brain or REM scheduling may be disabled in config"}, nil
	}

	// Execute the REM cycle
	report, err := remRunner.RunREMCycle(ctx, phases, dryRun)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("REM cycle failed: %v", err)}, nil
	}

	data, _ := json.Marshal(report)
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

// normalizePhase maps short-form phase names to the full forms expected by REMCycle.Run().
func normalizePhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "consolidate", "consolidation":
		return "consolidation"
	case "prune", "pruning":
		return "pruning"
	case "integrate", "integration":
		return "integration"
	case "groom", "grooming":
		return "grooming"
	case "triage":
		return "triage"
	default:
		return phase // pass through — REMCycle.Run will error on unknown phases
	}
}

// SelfTest implements types.SelfTester for BrainTool.
func (t *BrainTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
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

	// Check Brain service
	brainDep := types.DependencyStatus{
		Name:     "BrainService",
		Required: true,
	}

	if t.services == nil || t.services.Brain == nil {
		brainDep.Available = false
		brainDep.Status = "not_configured"
		brainDep.Message = "Brain service not available in ToolServices"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Brain service is not enabled"
		result.Suggestions = []string{
			"Enable brain in config.json",
			"Check that brain database path is configured",
		}
	} else {
		brainDep.Available = true
		brainDep.Status = "connected"
		result.Capabilities = []string{"store", "store_bulk", "get", "recall", "list", "delete", "push", "pop", "peek", "promote", "consolidate", "status"}

		// Check REM cycle availability
		remDep := types.DependencyStatus{
			Name:     "REMCycle",
			Required: false,
		}
		if t.services.REMCycle != nil {
			remDep.Available = true
			remDep.Status = "available"
			result.Capabilities = append(result.Capabilities, "rem_cycle")
		} else {
			remDep.Available = false
			remDep.Status = "not_configured"
			remDep.Message = "REM cycle scheduling not enabled"
			result.UnavailableCapabilities = []string{"rem_cycle"}
		}
		deps = append(deps, remDep)

		// Get status for verbose output
		if opts.Verbose {
			status, err := t.services.Brain.Status(ctx)
			if err == nil {
				result.Details = map[string]interface{}{
					"brain_status": status,
				}
			}
		}

		result.Status = types.SelfTestStatusOK
		result.Message = "Brain tool is fully functional"
	}
	deps = append(deps, brainDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Store a fact",
				Description: "Store a key-value fact in working memory",
				Args: map[string]interface{}{
					"action": "store",
					"key":    "user.preference.language",
					"value":  "Go",
					"tier":   "working",
				},
				Expected: "Fact stored in working memory",
			},
			{
				Name:        "Recall facts",
				Description: "Search for facts by query",
				Args: map[string]interface{}{
					"action": "recall",
					"query":  "user preferences",
					"limit":  10,
				},
				Expected: "Returns matching facts ranked by salience",
			},
			{
				Name:        "Check status",
				Description: "Get brain status including entry counts",
				Args: map[string]interface{}{
					"action": "status",
				},
				Expected: "Returns entry counts, hottest keys, and coldest keys",
			},
		}
	}

	return result
}
