package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"conduit/internal/chain"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// ChainToolExecutor decouples the chain tool from the registry package.
// *Registry satisfies this interface directly.
type ChainToolExecutor interface {
	ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error)
	GetAvailableTools() map[string]types.Tool
}

// ChainTool exposes chain list/show/validate/run actions to the AI agent.
type ChainTool struct {
	services   *types.ToolServices
	sandboxCfg config.SandboxConfig
	executor   ChainToolExecutor
}

func NewChainTool(services *types.ToolServices, sandboxCfg config.SandboxConfig, executor ChainToolExecutor) *ChainTool {
	return &ChainTool{
		services:   services,
		sandboxCfg: sandboxCfg,
		executor:   executor,
	}
}

func (t *ChainTool) Name() string { return "Chain" }

func (t *ChainTool) Description() string {
	return `Run saved multi-tool workflows (chains). Chains are JSON files in workspace/chains/ that define tool sequences with variable substitution and dependency ordering.

Actions:
- list: Show all available chains
- show: Display chain details (name required)
- validate: Check a chain for errors (name required)
- run: Execute a chain (name required, variables optional)`
}

func (t *ChainTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"list", "show", "validate", "run"},
				"description": "Chain operation to perform",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Chain name (required for show, validate, run)",
			},
			"variables": map[string]interface{}{
				"type":        "object",
				"description": "Key-value variables to substitute in chain steps (for run action)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *ChainTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := getStringParam(args, "action", "")

	switch action {
	case "list":
		return t.listChains()
	case "show":
		return t.showChain(args)
	case "validate":
		return t.validateChain(args)
	case "run":
		return t.runChain(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s (valid: list, show, validate, run)", action),
		}, nil
	}
}

func (t *ChainTool) chainsDir() string {
	workspace := t.sandboxCfg.WorkspaceDir
	if workspace == "" {
		workspace = "."
	}
	return workspace + "/chains"
}

// --- action handlers ---

func (t *ChainTool) listChains() (*types.ToolResult, error) {
	summaries, err := chain.ListChains(t.chainsDir())
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to list chains: %v", err),
		}, nil
	}

	if len(summaries) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No chains found. Create chain JSON files in workspace/chains/.",
			Data:    map[string]interface{}{"count": 0},
		}, nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d chain(s):\n\n", len(summaries)))
	for _, s := range summaries {
		vars := ""
		if s.Variables > 0 {
			vars = fmt.Sprintf(" (%d variables)", s.Variables)
		}
		b.WriteString(fmt.Sprintf("- %s: %s (%d steps)%s\n", s.Name, s.Description, s.StepCount, vars))
	}

	return &types.ToolResult{
		Success: true,
		Content: b.String(),
		Data: map[string]interface{}{
			"chains": summaries,
			"count":  len(summaries),
		},
	}, nil
}

func (t *ChainTool) showChain(args map[string]interface{}) (*types.ToolResult, error) {
	name := getStringParam(args, "name", "")
	if name == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "name parameter is required for show action",
		}, nil
	}

	c, err := chain.FindChain(t.chainsDir(), name)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("chain not found: %v", err),
		}, nil
	}

	data, _ := json.MarshalIndent(c, "", "  ")

	return &types.ToolResult{
		Success: true,
		Content: string(data),
		Data: map[string]interface{}{
			"chain": c,
		},
	}, nil
}

func (t *ChainTool) validateChain(args map[string]interface{}) (*types.ToolResult, error) {
	name := getStringParam(args, "name", "")
	if name == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "name parameter is required for validate action",
		}, nil
	}

	c, err := chain.FindChain(t.chainsDir(), name)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("chain not found: %v", err),
		}, nil
	}

	// Build a ToolExecutor adapter for validation (tool existence checks).
	executor := &validationToolExecutor{tools: t.executor.GetAvailableTools()}
	errs := chain.ValidateChain(c, executor)

	if len(errs) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Chain %q is valid (%d steps, %d variables).", c.Name, len(c.Steps), len(c.Variables)),
			Data: map[string]interface{}{
				"valid":     true,
				"steps":     len(c.Steps),
				"variables": len(c.Variables),
			},
		}, nil
	}

	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}

	return &types.ToolResult{
		Success: false,
		Error:   fmt.Sprintf("chain %q has %d validation error(s):\n- %s", c.Name, len(errs), strings.Join(msgs, "\n- ")),
		Data: map[string]interface{}{
			"valid":  false,
			"errors": msgs,
		},
	}, nil
}

func (t *ChainTool) runChain(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	name := getStringParam(args, "name", "")
	if name == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "name parameter is required for run action",
		}, nil
	}

	c, err := chain.FindChain(t.chainsDir(), name)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("chain not found: %v", err),
		}, nil
	}

	// Validate first.
	valExec := &validationToolExecutor{tools: t.executor.GetAvailableTools()}
	if errs := chain.ValidateChain(c, valExec); len(errs) > 0 {
		var msgs []string
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("chain validation failed:\n- %s", strings.Join(msgs, "\n- ")),
		}, nil
	}

	// Parse variables.
	vars := make(map[string]string)
	if v, ok := args["variables"].(map[string]interface{}); ok {
		for k, val := range v {
			vars[k] = fmt.Sprintf("%v", val)
		}
	}

	// Build runtime adapter that bridges ChainToolExecutor → chain.ToolExecutor.
	adapter := &registryChainAdapter{executor: t.executor, ctx: ctx}
	runner := chain.NewRunner(adapter)

	result, err := runner.Execute(c, vars)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("chain execution failed: %v", err),
		}, nil
	}

	// Format output.
	var b strings.Builder
	if result.Success {
		b.WriteString(fmt.Sprintf("Chain %q completed successfully (%s)\n\n", result.ChainName, result.TotalDuration.Round(1e6)))
	} else {
		b.WriteString(fmt.Sprintf("Chain %q failed: %s (%s)\n\n", result.ChainName, result.Error, result.TotalDuration.Round(1e6)))
	}

	for _, sr := range result.StepResults {
		status := "OK"
		if sr.Skipped {
			status = "SKIP"
		} else if !sr.Success {
			status = "FAIL"
		}
		b.WriteString(fmt.Sprintf("[%s] %s (%s) %s\n", status, sr.StepID, sr.ToolName, sr.Duration.Round(1e6)))
		if sr.Error != "" {
			b.WriteString(fmt.Sprintf("  error: %s\n", sr.Error))
		}
		if sr.Output != "" {
			out := sr.Output
			if len(out) > 500 {
				out = out[:497] + "..."
			}
			b.WriteString(fmt.Sprintf("  output: %s\n", out))
		}
	}

	return &types.ToolResult{
		Success: result.Success,
		Content: b.String(),
		Data: map[string]interface{}{
			"chain_name":     result.ChainName,
			"success":        result.Success,
			"step_results":   result.StepResults,
			"total_duration": result.TotalDuration.String(),
		},
	}, nil
}

// --- adapters ---

// registryChainAdapter bridges ChainToolExecutor (registry) to chain.ToolExecutor.
// Created per-invocation so chain steps inherit the request ctx.
type registryChainAdapter struct {
	executor ChainToolExecutor
	ctx      context.Context
}

func (a *registryChainAdapter) ExecuteTool(toolName string, params map[string]interface{}) (string, error) {
	result, err := a.executor.ExecuteTool(a.ctx, toolName, params)
	if err != nil {
		return "", err
	}
	if !result.Success {
		return "", fmt.Errorf("%s", result.Error)
	}
	return result.Content, nil
}

func (a *registryChainAdapter) ToolExists(toolName string) bool {
	_, ok := a.executor.GetAvailableTools()[toolName]
	return ok
}

// validationToolExecutor implements chain.ToolExecutor for validation-only checks.
type validationToolExecutor struct {
	tools map[string]types.Tool
}

func (v *validationToolExecutor) ExecuteTool(toolName string, params map[string]interface{}) (string, error) {
	return "", fmt.Errorf("validation-only executor cannot run tools")
}

func (v *validationToolExecutor) ToolExists(toolName string) bool {
	_, ok := v.tools[toolName]
	return ok
}

// --- helpers ---

func getStringParam(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}
