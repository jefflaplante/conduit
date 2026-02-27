package chain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Chain defines a reusable multi-tool workflow.
type Chain struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Steps       []ChainStep `json:"steps"`
	Variables   []Variable  `json:"variables,omitempty"`
}

// Variable describes a template parameter that can be substituted at runtime.
type Variable struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ChainStep is a single operation within a chain.
type ChainStep struct {
	ID        string                 `json:"id"`
	ToolName  string                 `json:"tool_name"`
	Params    map[string]interface{} `json:"params"`
	DependsOn []string               `json:"depends_on,omitempty"`
	OnError   string                 `json:"on_error,omitempty"` // "stop" (default), "skip", "retry"
}

// ChainResult captures the output of running a complete chain.
type ChainResult struct {
	ChainName     string        `json:"chain_name"`
	Success       bool          `json:"success"`
	StepResults   []StepResult  `json:"step_results"`
	TotalDuration time.Duration `json:"total_duration"`
	Error         string        `json:"error,omitempty"`
}

// StepResult is the outcome of a single chain step.
type StepResult struct {
	StepID   string        `json:"step_id"`
	ToolName string        `json:"tool_name"`
	Success  bool          `json:"success"`
	Output   string        `json:"output,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
	Skipped  bool          `json:"skipped,omitempty"`
}

// ChainSummary is a lightweight representation for list views.
type ChainSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	StepCount   int    `json:"step_count"`
	Variables   int    `json:"variables"`
}

// ToolExecutor is the interface chain execution uses to run individual tools.
// This allows the chain package to stay decoupled from the tool registry.
type ToolExecutor interface {
	// ExecuteTool runs a tool by name with the given parameters and returns its output.
	ExecuteTool(toolName string, params map[string]interface{}) (string, error)

	// ToolExists checks whether a tool with the given name is registered.
	ToolExists(toolName string) bool
}

// ChainRunner loads, validates, and executes chains.
type ChainRunner struct {
	executor ToolExecutor
}

// NewRunner creates a ChainRunner.
// If executor is nil, validation-only mode (Execute will fail).
func NewRunner(executor ToolExecutor) *ChainRunner {
	return &ChainRunner{executor: executor}
}

// LoadChain reads a chain definition from a JSON file.
func LoadChain(path string) (*Chain, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read chain file %s: %w", path, err)
	}

	var c Chain
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("failed to parse chain file %s: %w", path, err)
	}

	return &c, nil
}

// SaveChain writes a chain definition to a JSON file in the given directory.
func SaveChain(chain *Chain, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create chain directory %s: %w", dir, err)
	}

	filename := sanitizeFilename(chain.Name) + ".json"
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(chain, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal chain: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write chain file %s: %w", path, err)
	}

	return nil
}

// ValidateChain checks a chain definition for structural correctness.
// It returns all validation errors found rather than failing on the first.
func ValidateChain(chain *Chain, executor ToolExecutor) []error {
	var errs []error

	if chain.Name == "" {
		errs = append(errs, fmt.Errorf("chain name is required"))
	}

	if len(chain.Steps) == 0 {
		errs = append(errs, fmt.Errorf("chain must have at least one step"))
	}

	stepIDs := make(map[string]bool)
	for i, step := range chain.Steps {
		if step.ID == "" {
			errs = append(errs, fmt.Errorf("step %d: ID is required", i))
		} else if stepIDs[step.ID] {
			errs = append(errs, fmt.Errorf("step %d: duplicate ID %q", i, step.ID))
		} else {
			stepIDs[step.ID] = true
		}

		if step.ToolName == "" {
			errs = append(errs, fmt.Errorf("step %d (%s): tool_name is required", i, step.ID))
		} else if executor != nil && !executor.ToolExists(step.ToolName) {
			errs = append(errs, fmt.Errorf("step %d (%s): unknown tool %q", i, step.ID, step.ToolName))
		}

		// Validate dependencies reference known step IDs.
		for _, dep := range step.DependsOn {
			if !stepIDs[dep] {
				errs = append(errs, fmt.Errorf("step %d (%s): depends on unknown step %q", i, step.ID, dep))
			}
		}

		// Validate on_error value.
		switch step.OnError {
		case "", "stop", "skip", "retry":
			// valid
		default:
			errs = append(errs, fmt.Errorf("step %d (%s): invalid on_error value %q (must be stop, skip, or retry)", i, step.ID, step.OnError))
		}
	}

	// Validate variable references in step parameters.
	definedVars := make(map[string]bool)
	for _, v := range chain.Variables {
		definedVars[v.Name] = true
	}

	for i, step := range chain.Steps {
		refs := extractVariableRefs(step.Params)
		for _, ref := range refs {
			if !definedVars[ref] {
				errs = append(errs, fmt.Errorf("step %d (%s): references undefined variable %q", i, step.ID, ref))
			}
		}
	}

	// Check for dependency cycles.
	if err := detectCycles(chain.Steps); err != nil {
		errs = append(errs, err)
	}

	return errs
}

// Execute runs a chain with the given variable substitutions.
func (r *ChainRunner) Execute(chain *Chain, vars map[string]string) (*ChainResult, error) {
	if r.executor == nil {
		return nil, fmt.Errorf("no tool executor configured; cannot execute chain")
	}

	// Check required variables.
	for _, v := range chain.Variables {
		if v.Required {
			if _, ok := vars[v.Name]; !ok {
				if v.Default == "" {
					return nil, fmt.Errorf("required variable %q not provided", v.Name)
				}
			}
		}
	}

	// Build effective variable map with defaults.
	effectiveVars := make(map[string]string)
	for _, v := range chain.Variables {
		if v.Default != "" {
			effectiveVars[v.Name] = v.Default
		}
	}
	for k, v := range vars {
		effectiveVars[k] = v
	}

	start := time.Now()
	result := &ChainResult{
		ChainName: chain.Name,
		Success:   true,
	}

	// Determine execution order respecting dependencies.
	order, err := topologicalSort(chain.Steps)
	if err != nil {
		return nil, fmt.Errorf("failed to determine execution order: %w", err)
	}

	completedSteps := make(map[string]bool)

	for _, step := range order {
		// Check that all dependencies succeeded.
		allDepsMet := true
		for _, dep := range step.DependsOn {
			if !completedSteps[dep] {
				allDepsMet = false
				break
			}
		}

		stepStart := time.Now()
		stepResult := StepResult{
			StepID:   step.ID,
			ToolName: step.ToolName,
		}

		if !allDepsMet {
			stepResult.Skipped = true
			stepResult.Error = "dependency not met"
			stepResult.Duration = time.Since(stepStart)
			result.StepResults = append(result.StepResults, stepResult)

			onError := step.OnError
			if onError == "" {
				onError = "stop"
			}
			if onError == "stop" {
				result.Success = false
				result.Error = fmt.Sprintf("step %s skipped: dependency not met", step.ID)
				break
			}
			continue
		}

		// Substitute variables in parameters.
		resolvedParams := substituteVars(step.Params, effectiveVars)

		// Execute the tool.
		output, execErr := r.executor.ExecuteTool(step.ToolName, resolvedParams)
		stepResult.Duration = time.Since(stepStart)

		if execErr != nil {
			stepResult.Error = execErr.Error()

			onError := step.OnError
			if onError == "" {
				onError = "stop"
			}

			switch onError {
			case "stop":
				stepResult.Success = false
				result.StepResults = append(result.StepResults, stepResult)
				result.Success = false
				result.Error = fmt.Sprintf("step %s failed: %s", step.ID, execErr.Error())
				result.TotalDuration = time.Since(start)
				return result, nil
			case "skip":
				stepResult.Skipped = true
				result.StepResults = append(result.StepResults, stepResult)
				continue
			case "retry":
				// One retry attempt.
				output2, execErr2 := r.executor.ExecuteTool(step.ToolName, resolvedParams)
				if execErr2 != nil {
					stepResult.Error = execErr2.Error()
					stepResult.Success = false
					result.StepResults = append(result.StepResults, stepResult)
					result.Success = false
					result.Error = fmt.Sprintf("step %s failed after retry: %s", step.ID, execErr2.Error())
					result.TotalDuration = time.Since(start)
					return result, nil
				}
				stepResult.Output = output2
				stepResult.Success = true
				stepResult.Error = ""
			}
		} else {
			stepResult.Output = output
			stepResult.Success = true
		}

		completedSteps[step.ID] = true
		result.StepResults = append(result.StepResults, stepResult)
	}

	result.TotalDuration = time.Since(start)
	return result, nil
}

// ListChains returns summaries of all chain files in a directory.
func ListChains(dir string) ([]ChainSummary, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read chain directory %s: %w", dir, err)
	}

	var summaries []ChainSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		c, err := LoadChain(path)
		if err != nil {
			continue
		}

		desc := c.Description
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}

		summaries = append(summaries, ChainSummary{
			Name:        c.Name,
			Description: desc,
			StepCount:   len(c.Steps),
			Variables:   len(c.Variables),
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Name < summaries[j].Name
	})

	return summaries, nil
}

// FindChain looks for a chain by name in the given directory.
func FindChain(dir, name string) (*Chain, error) {
	// Try exact filename match first.
	path := filepath.Join(dir, sanitizeFilename(name)+".json")
	if _, err := os.Stat(path); err == nil {
		return LoadChain(path)
	}

	// Fall back to scanning all files for a name match.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read chain directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		c, err := LoadChain(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if strings.EqualFold(c.Name, name) {
			return c, nil
		}
	}

	return nil, fmt.Errorf("chain %q not found in %s", name, dir)
}

// DeleteChain removes a chain file by name from the given directory.
func DeleteChain(dir, name string) error {
	path := filepath.Join(dir, sanitizeFilename(name)+".json")
	if _, err := os.Stat(path); err == nil {
		return os.Remove(path)
	}

	// Fall back to scanning for the chain.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read chain directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, entry.Name())
		c, err := LoadChain(p)
		if err != nil {
			continue
		}
		if strings.EqualFold(c.Name, name) {
			return os.Remove(p)
		}
	}

	return fmt.Errorf("chain %q not found in %s", name, dir)
}

// --- internal helpers ---

// sanitizeFilename converts a chain name to a safe filename component.
func sanitizeFilename(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	// Strip characters that are not alphanumeric, dash, or underscore.
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		}
	}
	result := sb.String()
	if result == "" {
		return "unnamed"
	}
	return result
}

// extractVariableRefs finds all {{variable}} references in parameter values.
func extractVariableRefs(params map[string]interface{}) []string {
	var refs []string
	seen := make(map[string]bool)

	for _, v := range params {
		switch val := v.(type) {
		case string:
			extracted := findTemplateVars(val)
			for _, ref := range extracted {
				if !seen[ref] {
					seen[ref] = true
					refs = append(refs, ref)
				}
			}
		case map[string]interface{}:
			nested := extractVariableRefs(val)
			for _, ref := range nested {
				if !seen[ref] {
					seen[ref] = true
					refs = append(refs, ref)
				}
			}
		}
	}

	return refs
}

// findTemplateVars extracts variable names from {{name}} patterns.
func findTemplateVars(s string) []string {
	var vars []string
	for {
		start := strings.Index(s, "{{")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "}}")
		if end == -1 {
			break
		}
		varName := strings.TrimSpace(s[start+2 : start+end])
		if varName != "" {
			vars = append(vars, varName)
		}
		s = s[start+end+2:]
	}
	return vars
}

// substituteVars replaces {{variable}} templates in parameter values.
func substituteVars(params map[string]interface{}, vars map[string]string) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range params {
		switch val := v.(type) {
		case string:
			resolved := val
			for varName, varValue := range vars {
				resolved = strings.ReplaceAll(resolved, "{{"+varName+"}}", varValue)
			}
			result[k] = resolved
		case map[string]interface{}:
			result[k] = substituteVars(val, vars)
		default:
			result[k] = v
		}
	}
	return result
}

// topologicalSort returns steps in a valid execution order respecting dependencies.
func topologicalSort(steps []ChainStep) ([]ChainStep, error) {
	// Build adjacency and in-degree maps.
	stepMap := make(map[string]*ChainStep)
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // dep -> list of steps that depend on it

	for i := range steps {
		s := &steps[i]
		stepMap[s.ID] = s
		if _, ok := inDegree[s.ID]; !ok {
			inDegree[s.ID] = 0
		}
		for _, dep := range s.DependsOn {
			inDegree[s.ID]++
			dependents[dep] = append(dependents[dep], s.ID)
		}
	}

	// Kahn's algorithm.
	var queue []string
	for _, s := range steps {
		if inDegree[s.ID] == 0 {
			queue = append(queue, s.ID)
		}
	}

	var sorted []ChainStep
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, *stepMap[id])

		for _, depID := range dependents[id] {
			inDegree[depID]--
			if inDegree[depID] == 0 {
				queue = append(queue, depID)
			}
		}
	}

	if len(sorted) != len(steps) {
		return nil, fmt.Errorf("dependency cycle detected in chain steps")
	}

	return sorted, nil
}

// detectCycles checks for circular dependencies among steps.
func detectCycles(steps []ChainStep) error {
	_, err := topologicalSort(steps)
	return err
}
