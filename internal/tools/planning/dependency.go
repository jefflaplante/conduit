package planning

import (
	"context"
	"fmt"
	"log"
	"strings"

	"conduit/internal/ai"
)

// DependencyAnalyzer analyzes dependencies between tool calls for optimal execution planning
type DependencyAnalyzer struct {
	dependencyRules map[string]*DependencyRule
	conflictRules   map[string]*ConflictRule
	outputMatchers  map[string]*OutputMatcher
}

// DependencyRule defines when one tool depends on another
type DependencyRule struct {
	ToolName        string            `json:"tool_name"`
	DependsOn       []string          `json:"depends_on"`       // Tools this tool depends on
	OutputsUsedBy   []string          `json:"outputs_used_by"`  // Tools that use this tool's output
	ParameterDeps   map[string]string `json:"parameter_deps"`   // param_name -> source_tool
	RequiredOutputs []string          `json:"required_outputs"` // Required output fields
	ConflictsWith   []string          `json:"conflicts_with"`   // Tools that cannot run in parallel
}

// ConflictRule defines when two tools cannot run concurrently
type ConflictRule struct {
	Tool1    string `json:"tool1"`
	Tool2    string `json:"tool2"`
	Reason   string `json:"reason"`
	Severity string `json:"severity"` // "blocking", "performance", "safety"
}

// OutputMatcher helps identify when tool outputs are used as inputs
type OutputMatcher struct {
	ToolName       string            `json:"tool_name"`
	OutputPatterns []string          `json:"output_patterns"` // Regex patterns for outputs
	InputPatterns  map[string]string `json:"input_patterns"`  // param -> pattern mapping
}

// DependencyGraph represents the execution dependency graph
type DependencyGraph struct {
	Nodes map[string]*DependencyNode `json:"nodes"`
	Edges []*DependencyEdge          `json:"edges"`
}

// DependencyNode represents a tool in the dependency graph
type DependencyNode struct {
	StepID       string   `json:"step_id"`
	ToolName     string   `json:"tool_name"`
	Dependencies []string `json:"dependencies"` // Step IDs this node depends on
	Dependents   []string `json:"dependents"`   // Step IDs that depend on this node
	Level        int      `json:"level"`        // Execution level (0 = no deps, higher = later)
	CanParallel  bool     `json:"can_parallel"` // Can run in parallel with others at same level
}

// DependencyEdge represents a dependency relationship
type DependencyEdge struct {
	From        string  `json:"from"`        // Source step ID
	To          string  `json:"to"`          // Target step ID
	Type        string  `json:"type"`        // "data", "ordering", "conflict"
	Strength    float64 `json:"strength"`    // 0-1, how strong the dependency is
	Description string  `json:"description"` // Human-readable description
}

// NewDependencyAnalyzer creates a new dependency analyzer
func NewDependencyAnalyzer() *DependencyAnalyzer {
	analyzer := &DependencyAnalyzer{
		dependencyRules: make(map[string]*DependencyRule),
		conflictRules:   make(map[string]*ConflictRule),
		outputMatchers:  make(map[string]*OutputMatcher),
	}

	analyzer.initializeDefaultRules()
	return analyzer
}

// AnalyzeDependencies analyzes dependencies between tool calls
func (da *DependencyAnalyzer) AnalyzeDependencies(ctx context.Context, toolCalls []ai.ToolCall) (map[string][]string, error) {
	if len(toolCalls) <= 1 {
		// No dependencies for single tool or empty list
		return make(map[string][]string), nil
	}

	log.Printf("Analyzing dependencies for %d tool calls", len(toolCalls))

	// Build dependency graph
	graph, err := da.buildDependencyGraph(toolCalls)
	if err != nil {
		return nil, fmt.Errorf("failed to build dependency graph: %w", err)
	}

	// Convert graph to simple dependency map
	dependencies := make(map[string][]string)
	for stepID, node := range graph.Nodes {
		dependencies[stepID] = node.Dependencies
	}

	log.Printf("Found %d dependency relationships", len(graph.Edges))

	return dependencies, nil
}

// buildDependencyGraph creates a complete dependency graph from tool calls
func (da *DependencyAnalyzer) buildDependencyGraph(toolCalls []ai.ToolCall) (*DependencyGraph, error) {
	graph := &DependencyGraph{
		Nodes: make(map[string]*DependencyNode),
		Edges: []*DependencyEdge{},
	}

	// Create nodes for each tool call
	for i, call := range toolCalls {
		stepID := fmt.Sprintf("step_%d", i)
		node := &DependencyNode{
			StepID:       stepID,
			ToolName:     call.Name,
			Dependencies: []string{},
			Dependents:   []string{},
			Level:        0,
			CanParallel:  true,
		}
		graph.Nodes[stepID] = node
	}

	// Analyze different types of dependencies
	if err := da.analyzeDataDependencies(graph, toolCalls); err != nil {
		return nil, err
	}

	if err := da.analyzeOrderingDependencies(graph, toolCalls); err != nil {
		return nil, err
	}

	if err := da.analyzeConflicts(graph, toolCalls); err != nil {
		return nil, err
	}

	// Calculate execution levels
	if err := da.calculateExecutionLevels(graph); err != nil {
		return nil, err
	}

	return graph, nil
}

// analyzeDataDependencies finds dependencies based on data flow between tools
func (da *DependencyAnalyzer) analyzeDataDependencies(graph *DependencyGraph, toolCalls []ai.ToolCall) error {
	// Pattern-based dependency detection
	for i, call := range toolCalls {
		stepID := fmt.Sprintf("step_%d", i)

		// Check if this tool's arguments reference outputs from previous tools
		for j := 0; j < i; j++ {
			prevStepID := fmt.Sprintf("step_%d", j)
			prevCall := toolCalls[j]

			if da.hasDataDependency(call, prevCall) {
				da.addDependencyEdge(graph, prevStepID, stepID, "data", 0.9,
					fmt.Sprintf("%s depends on output from %s", call.Name, prevCall.Name))
			}
		}

		// Check tool-specific dependency rules
		if rule, exists := da.dependencyRules[call.Name]; exists {
			for j := 0; j < i; j++ {
				prevStepID := fmt.Sprintf("step_%d", j)
				prevCall := toolCalls[j]

				for _, depTool := range rule.DependsOn {
					if prevCall.Name == depTool {
						da.addDependencyEdge(graph, prevStepID, stepID, "ordering", 0.8,
							fmt.Sprintf("%s requires %s to complete first", call.Name, depTool))
					}
				}
			}
		}
	}

	return nil
}

// analyzeOrderingDependencies finds dependencies based on execution order requirements
func (da *DependencyAnalyzer) analyzeOrderingDependencies(graph *DependencyGraph, toolCalls []ai.ToolCall) error {
	// Special ordering rules for certain tool combinations
	for i, call := range toolCalls {
		stepID := fmt.Sprintf("step_%d", i)

		// File operations should typically happen in sequence if they affect same files
		if da.isFileOperation(call.Name) {
			for j := 0; j < i; j++ {
				prevStepID := fmt.Sprintf("step_%d", j)
				prevCall := toolCalls[j]

				if da.isFileOperation(prevCall.Name) && da.affectsSameFile(call, prevCall) {
					da.addDependencyEdge(graph, prevStepID, stepID, "ordering", 0.7,
						fmt.Sprintf("File operation ordering for %s", da.extractFilePath(call)))
				}
			}
		}

		// Search -> Fetch patterns (web_search followed by web_fetch)
		if call.Name == "web_fetch" {
			for j := 0; j < i; j++ {
				prevStepID := fmt.Sprintf("step_%d", j)
				prevCall := toolCalls[j]

				if prevCall.Name == "web_search" {
					// Check if fetch URL might come from search results
					if da.couldUseFetchURL(call, prevCall) {
						da.addDependencyEdge(graph, prevStepID, stepID, "data", 0.85,
							"web_fetch likely uses results from web_search")
					}
				}
			}
		}
	}

	return nil
}

// analyzeConflicts finds tools that cannot run in parallel
func (da *DependencyAnalyzer) analyzeConflicts(graph *DependencyGraph, toolCalls []ai.ToolCall) error {
	for i, call1 := range toolCalls {
		stepID1 := fmt.Sprintf("step_%d", i)

		for j := i + 1; j < len(toolCalls); j++ {
			stepID2 := fmt.Sprintf("step_%d", j)
			call2 := toolCalls[j]

			// Check for conflicts
			if da.hasConflict(call1, call2) {
				// Create ordering dependency to resolve conflict
				da.addDependencyEdge(graph, stepID1, stepID2, "conflict", 0.6,
					fmt.Sprintf("Conflict resolution: %s before %s", call1.Name, call2.Name))

				// Mark nodes as non-parallel
				graph.Nodes[stepID1].CanParallel = false
				graph.Nodes[stepID2].CanParallel = false
			}
		}
	}

	return nil
}

// calculateExecutionLevels assigns execution levels based on dependencies
func (da *DependencyAnalyzer) calculateExecutionLevels(graph *DependencyGraph) error {
	// Topological sort to assign levels
	inDegree := make(map[string]int)
	for stepID := range graph.Nodes {
		inDegree[stepID] = 0
	}

	// Count incoming edges
	for _, edge := range graph.Edges {
		inDegree[edge.To]++
	}

	// Level assignment using BFS
	queue := []string{}
	levels := make(map[string]int)

	// Start with nodes that have no dependencies
	for stepID, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, stepID)
			levels[stepID] = 0
		}
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		currentLevel := levels[current]

		// Update dependent nodes
		for _, edge := range graph.Edges {
			if edge.From == current {
				inDegree[edge.To]--

				// Calculate level for dependent node
				newLevel := currentLevel + 1
				if existingLevel, exists := levels[edge.To]; !exists || newLevel > existingLevel {
					levels[edge.To] = newLevel
				}

				if inDegree[edge.To] == 0 {
					queue = append(queue, edge.To)
				}
			}
		}
	}

	// Assign levels to nodes
	for stepID, level := range levels {
		graph.Nodes[stepID].Level = level
	}

	return nil
}

// addDependencyEdge adds a dependency edge to the graph
func (da *DependencyAnalyzer) addDependencyEdge(graph *DependencyGraph, from, to, edgeType string, strength float64, description string) {
	// Add edge
	edge := &DependencyEdge{
		From:        from,
		To:          to,
		Type:        edgeType,
		Strength:    strength,
		Description: description,
	}
	graph.Edges = append(graph.Edges, edge)

	// Update node relationships
	fromNode := graph.Nodes[from]
	toNode := graph.Nodes[to]

	if fromNode != nil && toNode != nil {
		toNode.Dependencies = append(toNode.Dependencies, from)
		fromNode.Dependents = append(fromNode.Dependents, to)
	}
}

// hasDataDependency checks if one tool call depends on data from another
func (da *DependencyAnalyzer) hasDataDependency(call, prevCall ai.ToolCall) bool {
	// Check for URL references (common pattern)
	if url, hasURL := call.Args["url"]; hasURL {
		if urlStr, ok := url.(string); ok {
			// Very basic heuristic - if URL looks like it might be a variable reference
			if strings.Contains(urlStr, "$") || strings.Contains(urlStr, "{") ||
				len(urlStr) < 10 || !strings.Contains(urlStr, ".") {
				return true
			}
		}
	}

	// Check for query expansion patterns
	if query, hasQuery := call.Args["query"]; hasQuery && prevCall.Name == "web_search" {
		if queryStr, ok := query.(string); ok {
			// If query contains "more", "additional", "details", it might expand on search
			expandWords := []string{"more", "additional", "details", "follow", "continue"}
			for _, word := range expandWords {
				if strings.Contains(strings.ToLower(queryStr), word) {
					return true
				}
			}
		}
	}

	// Check for path references from previous file operations
	if path, hasPath := call.Args["path"]; hasPath && da.isFileOperation(prevCall.Name) {
		if pathStr, ok := path.(string); ok {
			if prevPath, hasPrevPath := prevCall.Args["path"]; hasPrevPath {
				if prevPathStr, ok := prevPath.(string); ok {
					// If paths are related (same directory or file), there might be a dependency
					if strings.HasPrefix(pathStr, prevPathStr) ||
						strings.HasPrefix(prevPathStr, pathStr) {
						return true
					}
				}
			}
		}
	}

	return false
}

// hasConflict checks if two tools conflict with each other
func (da *DependencyAnalyzer) hasConflict(call1, call2 ai.ToolCall) bool {
	// Check predefined conflict rules
	conflictKey1 := fmt.Sprintf("%s-%s", call1.Name, call2.Name)
	conflictKey2 := fmt.Sprintf("%s-%s", call2.Name, call1.Name)

	if _, exists := da.conflictRules[conflictKey1]; exists {
		return true
	}
	if _, exists := da.conflictRules[conflictKey2]; exists {
		return true
	}

	// File system conflicts
	if da.isFileOperation(call1.Name) && da.isFileOperation(call2.Name) {
		if da.affectsSameFile(call1, call2) {
			return true
		}
	}

	// Command execution conflicts (exec tools might interfere)
	if call1.Name == "exec" && call2.Name == "exec" {
		// Commands might conflict if they affect the same directory
		if da.affectsSameDirectory(call1, call2) {
			return true
		}
	}

	return false
}

// Utility functions for dependency analysis

func (da *DependencyAnalyzer) isFileOperation(toolName string) bool {
	fileOps := []string{"read_file", "write_file", "list_files"}
	for _, op := range fileOps {
		if toolName == op {
			return true
		}
	}
	return false
}

func (da *DependencyAnalyzer) affectsSameFile(call1, call2 ai.ToolCall) bool {
	path1 := da.extractFilePath(call1)
	path2 := da.extractFilePath(call2)

	if path1 == "" || path2 == "" {
		return false
	}

	// Normalize paths and check for overlap
	return strings.TrimSpace(path1) == strings.TrimSpace(path2)
}

func (da *DependencyAnalyzer) affectsSameDirectory(call1, call2 ai.ToolCall) bool {
	cwd1 := da.extractWorkingDirectory(call1)
	cwd2 := da.extractWorkingDirectory(call2)

	if cwd1 == "" || cwd2 == "" {
		return false
	}

	return cwd1 == cwd2
}

func (da *DependencyAnalyzer) extractFilePath(call ai.ToolCall) string {
	if path, ok := call.Args["path"].(string); ok {
		return path
	}
	return ""
}

func (da *DependencyAnalyzer) extractWorkingDirectory(call ai.ToolCall) string {
	if cwd, ok := call.Args["cwd"].(string); ok {
		return cwd
	}
	return "" // Default working directory
}

func (da *DependencyAnalyzer) couldUseFetchURL(fetchCall, searchCall ai.ToolCall) bool {
	// This is a heuristic - in a real implementation, we'd need to examine
	// the search results to see if any URLs match the fetch URL
	// For now, assume that if a web_fetch follows a web_search, they're likely related
	if fetchCall.Name == "web_fetch" && searchCall.Name == "web_search" {
		// Check if the URL in fetch could reasonably come from search
		if url, hasURL := fetchCall.Args["url"]; hasURL {
			if urlStr, ok := url.(string); ok {
				// Very basic check - if URL is a real URL (not a reference),
				// it might still use search results
				return strings.HasPrefix(urlStr, "http")
			}
		}
	}
	return false
}

// initializeDefaultRules sets up default dependency and conflict rules
func (da *DependencyAnalyzer) initializeDefaultRules() {
	// Dependency rules
	da.dependencyRules["web_fetch"] = &DependencyRule{
		ToolName:        "web_fetch",
		DependsOn:       []string{"web_search"}, // Often follows web search
		OutputsUsedBy:   []string{"memory_search"},
		RequiredOutputs: []string{"content"},
		ConflictsWith:   []string{},
	}

	da.dependencyRules["write_file"] = &DependencyRule{
		ToolName:        "write_file",
		DependsOn:       []string{"read_file", "web_fetch"},
		OutputsUsedBy:   []string{"read_file", "exec"},
		RequiredOutputs: []string{},
		ConflictsWith:   []string{"write_file"}, // Multiple writes to same file
	}

	// Conflict rules
	da.conflictRules["exec-exec"] = &ConflictRule{
		Tool1:    "exec",
		Tool2:    "exec",
		Reason:   "Command execution might interfere",
		Severity: "performance",
	}

	da.conflictRules["write_file-write_file"] = &ConflictRule{
		Tool1:    "write_file",
		Tool2:    "write_file",
		Reason:   "Concurrent file writes might conflict",
		Severity: "safety",
	}

	// Output matchers for pattern detection
	da.outputMatchers["web_search"] = &OutputMatcher{
		ToolName:       "web_search",
		OutputPatterns: []string{`"url":\s*"([^"]+)"`},
		InputPatterns: map[string]string{
			"url": `https?://[^\s]+`,
		},
	}

	log.Printf("Initialized dependency analyzer with %d rules",
		len(da.dependencyRules)+len(da.conflictRules))
}

// GetDependencyGraph returns the full dependency graph for debugging/visualization
func (da *DependencyAnalyzer) GetDependencyGraph(ctx context.Context, toolCalls []ai.ToolCall) (*DependencyGraph, error) {
	return da.buildDependencyGraph(toolCalls)
}

// AddDependencyRule adds a custom dependency rule
func (da *DependencyAnalyzer) AddDependencyRule(rule *DependencyRule) {
	da.dependencyRules[rule.ToolName] = rule
	log.Printf("Added dependency rule for tool: %s", rule.ToolName)
}

// AddConflictRule adds a custom conflict rule
func (da *DependencyAnalyzer) AddConflictRule(rule *ConflictRule) {
	key := fmt.Sprintf("%s-%s", rule.Tool1, rule.Tool2)
	da.conflictRules[key] = rule
	log.Printf("Added conflict rule: %s conflicts with %s", rule.Tool1, rule.Tool2)
}
