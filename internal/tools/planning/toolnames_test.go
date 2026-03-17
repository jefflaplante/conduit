package planning

import (
	"testing"
)

func TestPlanningToolNames_DefaultProfiles(t *testing.T) {
	// Verify that initializeDefaultProfiles uses the actual PascalCase tool names
	// as registered in the tool registry, not snake_case names.
	planner := &ExecutionPlanner{
		toolProfiles: make(map[string]*ToolProfile),
	}
	planner.initializeDefaultProfiles()

	expectedTools := []string{
		"WebSearch",
		"WebFetch",
		"MemorySearch",
		"Read",
		"Write",
		"Glob",
		"Bash",
	}

	for _, name := range expectedTools {
		if _, exists := planner.toolProfiles[name]; !exists {
			t.Errorf("Expected tool profile %q not found in default profiles", name)
		}
	}

	// Verify old snake_case names are NOT present
	wrongNames := []string{
		"web_search",
		"web_fetch",
		"memory_search",
		"read_file",
		"write_file",
		"list_files",
		"exec",
	}

	for _, name := range wrongNames {
		if _, exists := planner.toolProfiles[name]; exists {
			t.Errorf("Old snake_case tool name %q should not be in default profiles", name)
		}
	}
}

func TestPlanningToolNames_OptimizerNetworkTools(t *testing.T) {
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	// PascalCase names should be recognized
	if !optimizer.isNetworkTool("WebSearch") {
		t.Error("WebSearch should be recognized as network tool")
	}
	if !optimizer.isNetworkTool("WebFetch") {
		t.Error("WebFetch should be recognized as network tool")
	}
	if !optimizer.isNetworkTool("Message") {
		t.Error("Message should be recognized as network tool")
	}

	// Old snake_case names should NOT be recognized
	if optimizer.isNetworkTool("web_search") {
		t.Error("web_search (snake_case) should not be recognized as network tool")
	}
}

func TestPlanningToolNames_OptimizerFileOps(t *testing.T) {
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	// PascalCase names should be recognized
	if !optimizer.isFileOperation("Read") {
		t.Error("Read should be recognized as file operation")
	}
	if !optimizer.isFileOperation("Write") {
		t.Error("Write should be recognized as file operation")
	}
	if !optimizer.isFileOperation("Glob") {
		t.Error("Glob should be recognized as file operation")
	}

	// Old snake_case names should NOT be recognized
	if optimizer.isFileOperation("read_file") {
		t.Error("read_file (snake_case) should not be recognized as file operation")
	}
}

func TestPlanningToolNames_DependencyRules(t *testing.T) {
	analyzer := NewDependencyAnalyzer()

	// PascalCase names should be in rules
	if _, exists := analyzer.dependencyRules["WebFetch"]; !exists {
		t.Error("WebFetch dependency rule not found")
	}
	if _, exists := analyzer.dependencyRules["Write"]; !exists {
		t.Error("Write dependency rule not found")
	}

	// Old snake_case names should NOT be in rules
	if _, exists := analyzer.dependencyRules["web_fetch"]; exists {
		t.Error("web_fetch (snake_case) should not be in dependency rules")
	}
	if _, exists := analyzer.dependencyRules["write_file"]; exists {
		t.Error("write_file (snake_case) should not be in dependency rules")
	}
}

func TestPlanningToolNames_ConflictRules(t *testing.T) {
	analyzer := NewDependencyAnalyzer()

	// PascalCase conflict keys
	if _, exists := analyzer.conflictRules["Bash-Bash"]; !exists {
		t.Error("Bash-Bash conflict rule not found")
	}
	if _, exists := analyzer.conflictRules["Write-Write"]; !exists {
		t.Error("Write-Write conflict rule not found")
	}

	// Old snake_case conflict keys should NOT exist
	if _, exists := analyzer.conflictRules["exec-exec"]; exists {
		t.Error("exec-exec (snake_case) should not be in conflict rules")
	}
}

func TestPlanningToolNames_MetricsParallelizable(t *testing.T) {
	mc := NewMetricsCollector()

	// PascalCase names should be parallelizable
	if !mc.isParallelizable("WebSearch") {
		t.Error("WebSearch should be parallelizable")
	}
	if !mc.isParallelizable("Read") {
		t.Error("Read should be parallelizable")
	}

	// Old names should NOT be parallelizable
	if mc.isParallelizable("web_search") {
		t.Error("web_search (snake_case) should not be parallelizable")
	}
}

func TestPlanningToolNames_DependencyAnalyzerFileOps(t *testing.T) {
	analyzer := NewDependencyAnalyzer()

	if !analyzer.isFileOperation("Read") {
		t.Error("Read should be recognized as file operation by DependencyAnalyzer")
	}
	if !analyzer.isFileOperation("Write") {
		t.Error("Write should be recognized as file operation by DependencyAnalyzer")
	}
	if !analyzer.isFileOperation("Glob") {
		t.Error("Glob should be recognized as file operation by DependencyAnalyzer")
	}

	if analyzer.isFileOperation("read_file") {
		t.Error("read_file (snake_case) should not be recognized")
	}
}
