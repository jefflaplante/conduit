package tools

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"conduit/internal/tools/types"
)

// ExecuteTool executes a tool by name with the given arguments, including validation
//
// conduit-31jg.19: this is the single choke point every tool invocation passes
// through (ExecutionEngine.executeSingle/executeParallel, the MCP handler,
// Chain, planning, SRE). A panicking tool is recovered here and converted into
// a failed result + error naming the tool, so it cannot take down the gateway.
func (r *Registry) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (result *types.ToolResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[Registry] PANIC in tool %q: %v\n%s", name, rec, debug.Stack())
			err = fmt.Errorf("tool %q panicked: %v", name, rec)
			result = types.NewErrorResult("tool_panic", err.Error()).
				WithSuggestions([]string{"This is a bug in the tool; try a different approach or tool"})
		}
	}()

	// Look up enablement and tool under the read lock, then release it
	// before executing (the tool may itself call RefreshSkillTools).
	r.mu.RLock()
	enabled := r.isToolEnabledLocked(name)
	tool, exists := r.tools[name]
	r.mu.RUnlock()

	// Check if tool is enabled
	if !enabled {
		result := types.NewErrorResult("tool_disabled", fmt.Sprintf("tool '%s' is not enabled", name)).
			WithSuggestions([]string{"Check the enabled_tools list in your config.json to enable this tool"})
		// Suggest enabled tools of the same category
		if similar := r.findSimilarEnabledTools(name); len(similar) > 0 {
			result.WithAvailableValues(similar)
		}
		return result, nil
	}

	if !exists {
		result := types.NewErrorResult("tool_not_found", fmt.Sprintf("tool '%s' not found", name))
		available := r.getEnabledToolNames()
		if len(available) > 0 {
			result.WithAvailableValues(available)
		}
		if closest := r.findClosestToolName(name); closest != "" {
			result.WithSuggestions([]string{fmt.Sprintf("Did you mean '%s'?", closest)})
		}
		return result, nil
	}

	// Validate parameters if tool supports validation
	if validator, ok := tool.(types.ParameterValidator); ok {
		validationResult := validator.ValidateParameters(ctx, args)
		if !validationResult.Valid {
			return r.createValidationErrorResult(name, validationResult), nil
		}
	}

	// conduit-25lt.2: classify and record the call against the tool policy.
	// Shadow mode: this never blocks.
	r.observePolicy(ctx, name, tool, args)

	// Execute tool
	toolResult, execErr := tool.Execute(ctx, args)
	if execErr != nil {
		errResult := &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool execution error: %v", execErr),
		}
		// conduit-31jg.47: keep the tool's output when it returned both a
		// result and an error (was discarded, hiding e.g. partial stderr).
		if toolResult != nil {
			errResult.Content = toolResult.Content
			errResult.Data = toolResult.Data
			errResult.ErrorDetails = toolResult.ErrorDetails
		}
		return errResult, execErr
	}

	return toolResult, nil
}

// callTimeoutProvider is implemented by tools that accept a per-call timeout
// parameter (Bash). conduit-31jg.39.
type callTimeoutProvider interface {
	CallTimeout(args map[string]interface{}) (time.Duration, bool)
}

// CallTimeout reports the per-call timeout a tool call requests, if the
// named tool supports one. The execution engine uses it to size that
// call's deadline (conduit-31jg.39).
func (r *Registry) CallTimeout(name string, args map[string]interface{}) (time.Duration, bool) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return 0, false
	}
	if p, ok := tool.(callTimeoutProvider); ok {
		return p.CallTimeout(args)
	}
	return 0, false
}

// modelDataProvider is implemented (by duck typing, so optional-tool
// subpackages need not import this package) by tools whose useful payload
// lives in ToolResult.Data rather than Content. Only for these does the
// execution engine append "Structured data: {json}" to the model-facing
// text (conduit-31jg.39).
type modelDataProvider interface {
	IncludeDataInModelOutput() bool
}

// IncludeDataInModelOutput reports whether the named tool opted into having
// its result Data rendered for the model (conduit-31jg.39).
func (r *Registry) IncludeDataInModelOutput(name string) bool {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	p, ok := tool.(modelDataProvider)
	return ok && p.IncludeDataInModelOutput()
}

// maxResultChars is the model-facing result budget (config
// tools.max_tool_result_chars, default DefaultMaxToolResultChars). Read uses
// it to page output itself instead of being middle-truncated.
func (r *Registry) maxResultChars() int {
	if r.resultChars > 0 {
		return r.resultChars
	}
	return DefaultMaxToolResultChars
}

// createValidationErrorResult creates a rich error result from validation failures
func (r *Registry) createValidationErrorResult(toolName string, validation *types.ValidationResult) *types.ToolResult {
	if len(validation.Errors) == 0 {
		return types.NewErrorResult("validation_failed", "Parameter validation failed")
	}

	// Use the first error as the primary error message
	primaryError := validation.Errors[0]
	message := fmt.Sprintf("Parameter '%s': %s", primaryError.Parameter, primaryError.Message)

	result := types.NewErrorResult("invalid_parameter", message).
		WithParameter(primaryError.Parameter, primaryError.ProvidedValue)

	if len(primaryError.AvailableValues) > 0 {
		result.WithAvailableValues(primaryError.AvailableValues)
	}

	if len(primaryError.Examples) > 0 {
		var examples []string
		for _, example := range primaryError.Examples {
			examples = append(examples, fmt.Sprintf("%v", example))
		}
		result.WithExamples(examples)
	}

	// Add suggestions from validation result
	if len(validation.Suggestions) > 0 {
		result.WithSuggestions(validation.Suggestions)
	}

	// Add discovery hint if available
	if primaryError.DiscoveryHint != "" {
		result.WithSuggestions(append(result.ErrorDetails.Suggestions, primaryError.DiscoveryHint))
	}

	// Add context about all validation errors
	context := map[string]interface{}{
		"tool":              toolName,
		"validation_errors": len(validation.Errors),
		"total_parameters":  len(validation.Errors),
	}

	if len(validation.Errors) > 1 {
		var allErrors []string
		for _, err := range validation.Errors {
			allErrors = append(allErrors, fmt.Sprintf("%s: %s", err.Parameter, err.Message))
		}
		context["all_errors"] = allErrors
	}

	return result.WithContext(context)
}
