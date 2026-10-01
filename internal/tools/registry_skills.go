package tools

import (
	"context"
	"fmt"
	"log"

	"conduit/internal/policy"
	"conduit/internal/skills"
	"conduit/internal/tools/types"
)

// skillToolBridge adapts *skills.SkillToolAdapter to satisfy types.Tool.
// SkillToolAdapter.Execute returns *skills.RegistryToolResult (to avoid circular imports),
// so this bridge converts it to *types.ToolResult.
type skillToolBridge struct {
	adapter  *skills.SkillToolAdapter
	services *types.ToolServices
}

func (b *skillToolBridge) Name() string                       { return b.adapter.Name() }
func (b *skillToolBridge) Description() string                { return b.adapter.Description() }
func (b *skillToolBridge) Parameters() map[string]interface{} { return b.adapter.Parameters() }

// ClassifyActions implements policy.Classifier (conduit-25lt.2).
func (b *skillToolBridge) ClassifyActions(ctx context.Context, args map[string]interface{}) []policy.Action {
	return b.adapter.ClassifyActions(ctx, args)
}

func (b *skillToolBridge) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	result, err := b.adapter.Execute(ctx, args)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return types.NewErrorResult("skill_error", result.Error), nil
	}

	toolResult := &types.ToolResult{Success: true, Content: result.Content, Data: result.Data}

	// Auto-cache declared brain keys from skill output
	b.autoCacheBrainKeys(ctx, result)

	return toolResult, nil
}

// autoCacheBrainKeys stores declared Produces keys from skill output into Brain working memory.
func (b *skillToolBridge) autoCacheBrainKeys(ctx context.Context, result *skills.RegistryToolResult) {
	if b.services == nil || b.services.Brain == nil {
		return
	}
	produces := b.adapter.BrainProduces()
	if len(produces) == 0 || result.Data == nil {
		return
	}
	source := "skill:" + b.adapter.Name()
	for _, key := range produces {
		if val, ok := result.Data[key]; ok {
			valStr := fmt.Sprintf("%v", val)
			if err := b.services.Brain.Store(ctx, key, valStr, types.BrainTierWorking, source); err != nil {
				log.Printf("Brain auto-cache failed for %s: %v", key, err)
			}
		}
	}
}

// buildSkillBridges discovers skill adapters. It touches no registry maps
// and runs without r.mu so skill discovery I/O never blocks tool lookups.
func (r *Registry) buildSkillBridges() []*skillToolBridge {
	if r.services.SkillsManager == nil || !r.services.SkillsManager.IsEnabled() {
		return nil
	}
	skillAdapters, err := skills.GenerateToolAdapters(context.Background(), r.services.SkillsManager)
	if err != nil {
		log.Printf("Failed to register skill tools: %v", err)
		return nil
	}
	bridges := make([]*skillToolBridge, 0, len(skillAdapters))
	for _, adapter := range skillAdapters {
		bridges = append(bridges, &skillToolBridge{adapter: adapter, services: r.services})
	}
	return bridges
}

// addSkillBridgesLocked registers bridges as enabled tools. Caller holds r.mu (write).
func (r *Registry) addSkillBridgesLocked(bridges []*skillToolBridge) {
	for _, bridge := range bridges {
		r.tools[bridge.Name()] = bridge
		r.enabledTools[normalizeToolName(bridge.Name())] = true
	}
	if len(bridges) > 0 {
		log.Printf("Registered and enabled %d skill-based tools", len(bridges))
	}
}

// registerSkillTools discovers skill adapters and registers them as enabled tools.
func (r *Registry) registerSkillTools() {
	bridges := r.buildSkillBridges()
	r.mu.Lock()
	r.addSkillBridgesLocked(bridges)
	r.mu.Unlock()
}

// RefreshSkillTools removes old skill tools and re-registers from fresh state.
// Returns the number of skill tools now registered.
func (r *Registry) RefreshSkillTools() int {
	// conduit-31jg.19: discover outside the lock, then swap atomically under
	// the write lock so readers never observe a half-refreshed registry.
	bridges := r.buildSkillBridges()

	r.mu.Lock()
	defer r.mu.Unlock()
	// Remove old skill tools (identified by bridge type)
	for name, tool := range r.tools {
		if _, ok := tool.(*skillToolBridge); ok {
			delete(r.tools, name)
			// enabledTools is keyed by normalized name (see addSkillBridgesLocked).
			delete(r.enabledTools, normalizeToolName(name))
		}
	}
	// Re-register from fresh skills manager state
	r.addSkillBridgesLocked(bridges)
	// Count registered skill tools
	count := 0
	for _, tool := range r.tools {
		if _, ok := tool.(*skillToolBridge); ok {
			count++
		}
	}
	return count
}
