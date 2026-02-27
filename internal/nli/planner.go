package nli

import (
	"fmt"
	"time"
)

// ValidationError describes a problem with a planned execution step.
type ValidationError struct {
	StepIndex int    `json:"step_index"`
	Field     string `json:"field"`
	Message   string `json:"message"`
}

// ExecutionStep represents a single step in an execution plan.
type ExecutionStep struct {
	Intent        Intent                 `json:"intent"`
	ToolName      string                 `json:"tool_name"`
	Parameters    map[string]interface{} `json:"parameters"`
	DependsOn     []int                  `json:"depends_on"`
	EstimatedCost float64                `json:"estimated_cost"`
	Description   string                 `json:"description"`
}

// ExecutionPlan represents an ordered series of steps to satisfy a multi-step request.
type ExecutionPlan struct {
	ID                string          `json:"id"`
	Steps             []ExecutionStep `json:"steps"`
	EstimatedDuration time.Duration   `json:"estimated_duration"`
	RollbackSteps     []ExecutionStep `json:"rollback_steps,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

// toolMapping maps action + heuristic target keywords to likely tool names.
var toolMapping = map[ActionType]map[string]string{
	ActionQuery: {
		"web":     "web_search",
		"search":  "web_search",
		"url":     "web_fetch",
		"fetch":   "web_fetch",
		"memory":  "memory_search",
		"recall":  "memory_search",
		"file":    "read_file",
		"session": "session_manager",
		"status":  "gateway_control",
		"channel": "gateway_control",
		"image":   "image_analysis",
		"picture": "image_analysis",
	},
	ActionCommand: {
		"message":  "send_message",
		"schedule": "schedule_job",
		"job":      "schedule_job",
		"cron":     "schedule_job",
		"gateway":  "gateway_control",
		"restart":  "gateway_control",
		"channel":  "gateway_control",
		"exec":     "exec",
		"run":      "exec",
		"build":    "exec",
		"test":     "exec",
		"backup":   "gateway_control",
	},
	ActionCreate: {
		"file":     "write_file",
		"message":  "send_message",
		"schedule": "schedule_job",
		"job":      "schedule_job",
		"session":  "session_manager",
	},
	ActionModify: {
		"file":     "edit_file",
		"config":   "gateway_control",
		"schedule": "schedule_job",
		"session":  "session_manager",
		"context":  "context_management",
	},
	ActionDelete: {
		"file":     "exec",
		"job":      "schedule_job",
		"schedule": "schedule_job",
		"session":  "session_manager",
		"context":  "context_management",
	},
}

// toolDurations provides rough duration estimates per tool for planning.
var toolDurations = map[string]time.Duration{
	"web_search":         2 * time.Second,
	"web_fetch":          3 * time.Second,
	"memory_search":      500 * time.Millisecond,
	"read_file":          100 * time.Millisecond,
	"write_file":         100 * time.Millisecond,
	"edit_file":          100 * time.Millisecond,
	"list_files":         100 * time.Millisecond,
	"exec":               5 * time.Second,
	"send_message":       1 * time.Second,
	"schedule_job":       200 * time.Millisecond,
	"image_analysis":     3 * time.Second,
	"gateway_control":    500 * time.Millisecond,
	"session_manager":    200 * time.Millisecond,
	"context_management": 200 * time.Millisecond,
}

// toolCosts provides rough cost estimates per tool.
var toolCosts = map[string]float64{
	"web_search":         0.005,
	"web_fetch":          0.002,
	"memory_search":      0.0001,
	"read_file":          0.0001,
	"write_file":         0.0001,
	"edit_file":          0.0001,
	"list_files":         0.0001,
	"exec":               0.001,
	"send_message":       0.0005,
	"schedule_job":       0.0001,
	"image_analysis":     0.01,
	"gateway_control":    0.0001,
	"session_manager":    0.0001,
	"context_management": 0.0001,
}

// ConversationPlanner converts parsed intents into executable plans.
type ConversationPlanner struct {
	parser *IntentParser
}

// NewConversationPlanner creates a new planner.
func NewConversationPlanner() *ConversationPlanner {
	return &ConversationPlanner{
		parser: NewIntentParser(),
	}
}

// PlanExecution builds an ExecutionPlan from a slice of intents.
func (cp *ConversationPlanner) PlanExecution(intents []Intent) *ExecutionPlan {
	if len(intents) == 0 {
		return &ExecutionPlan{
			ID:        fmt.Sprintf("plan_%d", time.Now().UnixNano()),
			Steps:     nil,
			CreatedAt: time.Now(),
		}
	}

	steps := make([]ExecutionStep, 0, len(intents))
	for i, intent := range intents {
		step := cp.buildStep(i, intent, steps)
		steps = append(steps, step)
	}

	plan := &ExecutionPlan{
		ID:                fmt.Sprintf("plan_%d", time.Now().UnixNano()),
		Steps:             steps,
		EstimatedDuration: cp.estimateDuration(steps),
		RollbackSteps:     cp.generateRollbacks(steps),
		CreatedAt:         time.Now(),
	}

	return plan
}

// ValidatePlan checks the plan for feasibility issues.
func (cp *ConversationPlanner) ValidatePlan(plan *ExecutionPlan) []ValidationError {
	if plan == nil {
		return []ValidationError{{StepIndex: -1, Field: "plan", Message: "plan is nil"}}
	}

	var errors []ValidationError

	for i, step := range plan.Steps {
		// Check that a tool is assigned.
		if step.ToolName == "" {
			errors = append(errors, ValidationError{
				StepIndex: i,
				Field:     "tool_name",
				Message:   "no tool could be resolved for this step",
			})
		}

		// Check dependency indices are valid.
		for _, dep := range step.DependsOn {
			if dep < 0 || dep >= i {
				errors = append(errors, ValidationError{
					StepIndex: i,
					Field:     "depends_on",
					Message:   fmt.Sprintf("invalid dependency index %d (must be 0..%d)", dep, i-1),
				})
			}
		}

		// Check circular dependencies (within the linear chain this is just forward refs).
		for _, dep := range step.DependsOn {
			if dep >= i {
				errors = append(errors, ValidationError{
					StepIndex: i,
					Field:     "depends_on",
					Message:   fmt.Sprintf("circular/forward dependency on step %d", dep),
				})
			}
		}
	}

	return errors
}

// buildStep creates a single ExecutionStep from an intent.
func (cp *ConversationPlanner) buildStep(index int, intent Intent, priorSteps []ExecutionStep) ExecutionStep {
	toolName := cp.resolveToolName(intent)
	params := mergeParameters(intent.Parameters, intent.Entities)
	deps := cp.inferDependencies(index, intent, priorSteps)
	cost := toolCosts[toolName]

	description := fmt.Sprintf("%s %s", intent.Action, intent.Target)
	if description == string(intent.Action)+" " {
		description = string(intent.Action)
	}

	return ExecutionStep{
		Intent:        intent,
		ToolName:      toolName,
		Parameters:    params,
		DependsOn:     deps,
		EstimatedCost: cost,
		Description:   description,
	}
}

// resolveToolName determines the best tool for an intent.
func (cp *ConversationPlanner) resolveToolName(intent Intent) string {
	// If the intent explicitly mentions a tool, use it.
	if tool, ok := intent.Parameters["tool"].(string); ok && tool != "" {
		return tool
	}

	// Check entities for a tool name.
	for _, e := range intent.Entities {
		if e.Type == EntityToolName {
			return e.Value
		}
	}

	// Match by action + target keywords.
	if mapping, ok := toolMapping[intent.Action]; ok {
		target := intent.Target
		for keyword, tool := range mapping {
			if containsWord(target, keyword) {
				return tool
			}
		}
	}

	// Match by entity types present.
	for _, e := range intent.Entities {
		switch e.Type {
		case EntityURL:
			return "web_fetch"
		case EntityFilePath:
			switch intent.Action {
			case ActionCreate:
				return "write_file"
			case ActionModify:
				return "edit_file"
			case ActionDelete:
				return "exec"
			default:
				return "read_file"
			}
		case EntitySession:
			return "session_manager"
		case EntityChannel:
			return "send_message"
		}
	}

	// Fallback: action-based defaults.
	switch intent.Action {
	case ActionQuery:
		return "web_search"
	case ActionCommand:
		return "exec"
	case ActionCreate:
		return "write_file"
	case ActionModify:
		return "edit_file"
	case ActionDelete:
		return "exec"
	default:
		return "web_search"
	}
}

// inferDependencies figures out which prior steps a new step depends on.
func (cp *ConversationPlanner) inferDependencies(index int, intent Intent, priorSteps []ExecutionStep) []int {
	if index == 0 || len(priorSteps) == 0 {
		return nil
	}

	var deps []int

	// Data flow heuristics: if this step consumes an entity that a prior step produces.
	for i, prior := range priorSteps {
		if cp.hasDataDependency(prior, intent) {
			deps = append(deps, i)
		}
	}

	// If no explicit dependency found, default to sequential on the previous step
	// when the confidence suggests ordering matters.
	if len(deps) == 0 && intent.Confidence > 0.5 {
		deps = []int{index - 1}
	}

	return deps
}

// hasDataDependency checks whether a step and an intent share entity types
// suggesting a producer-consumer relationship.
func (cp *ConversationPlanner) hasDataDependency(prior ExecutionStep, intent Intent) bool {
	// If the prior step produces URLs (web_search) and this step consumes URLs (web_fetch).
	if prior.ToolName == "web_search" {
		for _, e := range intent.Entities {
			if e.Type == EntityURL {
				return true
			}
		}
		if intent.Action == ActionQuery && containsWord(intent.Target, "fetch") {
			return true
		}
	}

	// If the prior step reads/creates a file and this step modifies the same path.
	if prior.ToolName == "read_file" || prior.ToolName == "write_file" {
		priorPath, _ := prior.Parameters["path"].(string)
		intentPath, _ := intent.Parameters["path"].(string)
		if priorPath != "" && intentPath != "" && priorPath == intentPath {
			return true
		}
	}

	return false
}

// estimateDuration sums estimated tool durations, accounting for parallelism opportunities.
func (cp *ConversationPlanner) estimateDuration(steps []ExecutionStep) time.Duration {
	var total time.Duration
	for _, step := range steps {
		d, ok := toolDurations[step.ToolName]
		if !ok {
			d = 2 * time.Second // default estimate
		}
		total += d
	}
	return total
}

// generateRollbacks creates rollback steps for destructive operations.
func (cp *ConversationPlanner) generateRollbacks(steps []ExecutionStep) []ExecutionStep {
	var rollbacks []ExecutionStep

	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		switch step.Intent.Action {
		case ActionDelete:
			// No straightforward rollback for delete without backup.
			rollbacks = append(rollbacks, ExecutionStep{
				Intent: Intent{
					Action:  ActionQuery,
					Target:  step.Intent.Target,
					RawText: fmt.Sprintf("verify deletion of %s", step.Intent.Target),
				},
				ToolName:    "exec",
				Parameters:  map[string]interface{}{"note": "manual review required"},
				Description: fmt.Sprintf("review deletion of %s", step.Intent.Target),
			})
		case ActionModify:
			// Suggest reading the original first (pre-modification state).
			if path, ok := step.Parameters["path"].(string); ok && path != "" {
				rollbacks = append(rollbacks, ExecutionStep{
					Intent: Intent{
						Action:  ActionQuery,
						Target:  path,
						RawText: fmt.Sprintf("read original %s before modification", path),
					},
					ToolName:    "read_file",
					Parameters:  map[string]interface{}{"path": path},
					Description: fmt.Sprintf("capture state of %s before modification", path),
				})
			}
		}
	}

	return rollbacks
}

// mergeParameters combines intent parameters with entity-derived parameters.
func mergeParameters(params map[string]interface{}, entities []Entity) map[string]interface{} {
	merged := make(map[string]interface{})
	for k, v := range params {
		merged[k] = v
	}

	// Add any entity values not already present.
	for _, e := range entities {
		switch e.Type {
		case EntityFilePath:
			if _, exists := merged["path"]; !exists {
				merged["path"] = e.Value
			}
		case EntityURL:
			if _, exists := merged["url"]; !exists {
				merged["url"] = e.Value
			}
		case EntityModelName:
			if _, exists := merged["model"]; !exists {
				merged["model"] = e.Value
			}
		case EntitySession:
			if _, exists := merged["session"]; !exists {
				merged["session"] = e.Value
			}
		case EntityChannel:
			if _, exists := merged["channel"]; !exists {
				merged["channel"] = e.Value
			}
		case EntityToolName:
			if _, exists := merged["tool"]; !exists {
				merged["tool"] = e.Value
			}
		}
	}

	return merged
}

// containsWord checks if text contains word as a standalone word (case-insensitive).
func containsWord(text, word string) bool {
	lower := toLower(text)
	wLower := toLower(word)
	idx := 0
	for idx < len(lower) {
		pos := indexFrom(lower, wLower, idx)
		if pos < 0 {
			return false
		}
		end := pos + len(wLower)
		beforeOK := pos == 0 || !isAlpha(lower[pos-1])
		afterOK := end >= len(lower) || !isAlpha(lower[end])
		if beforeOK && afterOK {
			return true
		}
		idx = pos + 1
	}
	return false
}

// toLower is a simple ASCII-only lowercase.
func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// indexFrom returns the index of sub in s starting from offset.
func indexFrom(s, sub string, offset int) int {
	if offset >= len(s) {
		return -1
	}
	idx := -1
	rest := s[offset:]
	for i := 0; i <= len(rest)-len(sub); i++ {
		if rest[i:i+len(sub)] == sub {
			idx = offset + i
			break
		}
	}
	return idx
}
