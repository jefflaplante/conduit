package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// collectBeads detects beads and reports issue status by parsing JSONL files
// directly, with optional enrichment from br CLI commands when available.
func (t *ContextTool) collectBeads(verbose bool) (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Beads\n\n")

	workDir := t.getWorkspaceDir()
	beadsDir := filepath.Join(workDir, ".beads")

	// Check if .beads directory exists
	info, err := os.Stat(beadsDir)
	if err != nil || !info.IsDir() {
		result["detected"] = false
		builder.WriteString("No .beads directory detected.\n\n")
		return result, builder.String()
	}

	result["detected"] = true
	result["path"] = beadsDir
	builder.WriteString(fmt.Sprintf("Beads Dir: %s\n", beadsDir))

	// Load metadata.json if present
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	var metadata map[string]interface{}
	if data, err := os.ReadFile(metadataPath); err == nil {
		if json.Unmarshal(data, &metadata) == nil && len(metadata) > 0 {
			result["metadata"] = metadata
			if backend, ok := metadata["backend"].(string); ok {
				builder.WriteString(fmt.Sprintf("Backend: %s\n", backend))
			}
		}
	}

	// Determine the JSONL file path.
	// Priority: metadata.jsonl_export (relative to workDir) -> .beads/issues.jsonl -> workspace root issues.jsonl
	jsonlPath := ""
	if metadata != nil {
		if exportPath, ok := metadata["jsonl_export"].(string); ok && exportPath != "" {
			candidate := exportPath
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(workDir, candidate)
			}
			if _, err := os.Stat(candidate); err == nil {
				jsonlPath = candidate
			}
		}
	}
	if jsonlPath == "" {
		candidate := filepath.Join(beadsDir, "issues.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			jsonlPath = candidate
		}
	}
	if jsonlPath == "" {
		candidate := filepath.Join(workDir, "issues.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			jsonlPath = candidate
		}
	}

	// Parse JSONL issues
	if jsonlPath != "" {
		issues, allParsed := t.parseIssuesJSONL(jsonlPath)
		issuesData := t.summarizeIssues(issues, allParsed, verbose)
		result["issues"] = issuesData

		total, _ := issuesData["total"].(int)
		openCount, _ := issuesData["open"].(int)
		inProgressCount, _ := issuesData["in_progress"].(int)
		closedCount, _ := issuesData["closed"].(int)

		builder.WriteString(fmt.Sprintf("%d total, %d open, %d in-progress, %d closed\n",
			total, openCount, inProgressCount, closedCount))

		if verbose {
			if openIssues, ok := issuesData["open_issues"].([]map[string]interface{}); ok && len(openIssues) > 0 {
				builder.WriteString("\nOpen Issues:\n")
				for _, oi := range openIssues {
					line := fmt.Sprintf("  %s: %s", oi["id"], oi["title"])
					if assignee, ok := oi["assignee"].(string); ok && assignee != "" {
						line += fmt.Sprintf(" (@%s)", assignee)
					}
					builder.WriteString(line + "\n")
				}
			}
		}
	}

	// Optionally enrich with br CLI output (non-fatal if unavailable)
	if ver, err := t.runBeadsCommand(workDir, "--version"); err == nil {
		result["version"] = strings.TrimSpace(ver)
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// ---------- Parsing & Helpers ----------

// parseIssuesJSONL reads a JSONL file and returns parsed issue maps.
// Malformed lines and blank lines are silently skipped.
func (t *ContextTool) parseIssuesJSONL(path string) ([]map[string]interface{}, []map[string]interface{}) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	var issues []map[string]interface{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var issue map[string]interface{}
		if json.Unmarshal([]byte(line), &issue) == nil {
			issues = append(issues, issue)
		}
	}
	return issues, issues
}

// classifyStatus normalizes a beads issue status into one of: "open", "in_progress", "closed".
func classifyStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in_progress", "in-progress", "active", "doing":
		return "in_progress"
	case "closed", "done", "resolved", "completed":
		return "closed"
	default:
		// open, new, backlog, todo, and anything unrecognized count as open
		return "open"
	}
}

// summarizeIssues builds the issues data map from parsed issues.
func (t *ContextTool) summarizeIssues(issues []map[string]interface{}, _ []map[string]interface{}, verbose bool) map[string]interface{} {
	data := make(map[string]interface{})
	total := len(issues)
	data["total"] = total

	var openCount, inProgressCount, closedCount int
	var openIssues []map[string]interface{}

	for _, issue := range issues {
		status, _ := issue["status"].(string)
		cat := classifyStatus(status)
		switch cat {
		case "open":
			openCount++
			openIssues = append(openIssues, issue)
		case "in_progress":
			inProgressCount++
			openIssues = append(openIssues, issue)
		case "closed":
			closedCount++
		}
	}

	data["open"] = openCount
	data["in_progress"] = inProgressCount
	data["closed"] = closedCount

	if verbose && len(openIssues) > 0 {
		var summaries []map[string]interface{}
		for _, oi := range openIssues {
			summary := map[string]interface{}{
				"id":    oi["id"],
				"title": oi["title"],
			}
			if status, ok := oi["status"].(string); ok {
				summary["status"] = status
			}
			if priority, ok := oi["priority"]; ok {
				summary["priority"] = priority
			}
			if assignee, ok := oi["assignee"].(string); ok && assignee != "" {
				summary["assignee"] = assignee
			}
			summaries = append(summaries, summary)
		}
		data["open_issues"] = summaries
	}

	return data
}

// runBeadsCommand executes a br command in the given directory and returns stdout.
func (t *ContextTool) runBeadsCommand(dir string, beadsArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "br", beadsArgs...)
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}
