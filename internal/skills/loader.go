package skills

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillLoader handles parsing SKILL.md files with YAML frontmatter
type SkillLoader struct{}

// NewSkillLoader creates a new skill loader
func NewSkillLoader() *SkillLoader {
	return &SkillLoader{}
}

// LoadSkillFromFile loads and parses a SKILL.md file
func (l *SkillLoader) LoadSkillFromFile(filePath string) (*Skill, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading skill file %s: %w", filePath, err)
	}

	return l.LoadSkillFromContent(string(content))
}

// LoadSkillFromContent parses skill content with YAML frontmatter
func (l *SkillLoader) LoadSkillFromContent(content string) (*Skill, error) {
	frontmatter, markdownContent, err := l.parseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("error parsing frontmatter: %w", err)
	}

	skill := &Skill{
		Content: markdownContent,
	}

	// Parse YAML frontmatter into skill metadata
	if frontmatter != "" {
		var frontmatterData map[string]interface{}
		if err := yaml.Unmarshal([]byte(frontmatter), &frontmatterData); err != nil {
			return nil, fmt.Errorf("error parsing YAML frontmatter: %w", err)
		}

		if err := l.populateSkillFromFrontmatter(skill, frontmatterData); err != nil {
			return nil, fmt.Errorf("error processing frontmatter: %w", err)
		}
	}

	// Validate that required fields are present
	if skill.Name == "" {
		return nil, fmt.Errorf("skill name is required in frontmatter")
	}

	if skill.Description == "" {
		return nil, fmt.Errorf("skill description is required in frontmatter")
	}

	return skill, nil
}

// parseFrontmatter separates YAML frontmatter from markdown content
func (l *SkillLoader) parseFrontmatter(content string) (frontmatter, markdown string, err error) {
	lines := strings.Split(content, "\n")

	// Check if content starts with frontmatter delimiter
	if len(lines) == 0 || lines[0] != "---" {
		// No frontmatter, return entire content as markdown
		return "", content, nil
	}

	// Find the closing frontmatter delimiter
	frontmatterEnd := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			frontmatterEnd = i
			break
		}
	}

	if frontmatterEnd == -1 {
		return "", "", fmt.Errorf("frontmatter delimiter not properly closed")
	}

	// Extract frontmatter and markdown content
	frontmatterLines := lines[1:frontmatterEnd]
	markdownLines := lines[frontmatterEnd+1:]

	frontmatter = strings.Join(frontmatterLines, "\n")
	markdown = strings.Join(markdownLines, "\n")

	return frontmatter, markdown, nil
}

// populateSkillFromFrontmatter fills skill fields from parsed frontmatter
func (l *SkillLoader) populateSkillFromFrontmatter(skill *Skill, data map[string]interface{}) error {
	// Required fields
	if name, ok := data["name"].(string); ok {
		skill.Name = name
	}

	if description, ok := data["description"].(string); ok {
		skill.Description = description
	}

	// Optional Conduit metadata
	if conduitData, ok := data["conduit"].(map[string]interface{}); ok {
		if err := l.parseConduitMetadata(&skill.Metadata.Conduit, conduitData); err != nil {
			return fmt.Errorf("error parsing conduit metadata: %w", err)
		}
	}

	// Handle alternative metadata structures (for backward compatibility)
	if emoji, ok := data["emoji"].(string); ok {
		skill.Metadata.Conduit.Emoji = emoji
	}

	if requiresData, ok := data["requires"].(map[string]interface{}); ok {
		if err := l.parseRequirements(&skill.Metadata.Conduit.Requires, requiresData); err != nil {
			return fmt.Errorf("error parsing requirements: %w", err)
		}
	}

	return nil
}

// parseConduitMetadata parses the conduit section of frontmatter
func (l *SkillLoader) parseConduitMetadata(meta *SkillConduitMeta, data map[string]interface{}) error {
	if emoji, ok := data["emoji"].(string); ok {
		meta.Emoji = emoji
	}

	if requiresData, ok := data["requires"].(map[string]interface{}); ok {
		if err := l.parseRequirements(&meta.Requires, requiresData); err != nil {
			return fmt.Errorf("error parsing requirements: %w", err)
		}
	}

	return nil
}

// parseRequirements parses skill requirements from frontmatter
func (l *SkillLoader) parseRequirements(reqs *SkillRequirements, data map[string]interface{}) error {
	if anyBins, ok := data["anyBins"].([]interface{}); ok {
		reqs.AnyBins = l.interfaceSliceToStringSlice(anyBins)
	}

	if allBins, ok := data["allBins"].([]interface{}); ok {
		reqs.AllBins = l.interfaceSliceToStringSlice(allBins)
	}

	if files, ok := data["files"].([]interface{}); ok {
		reqs.Files = l.interfaceSliceToStringSlice(files)
	}

	if env, ok := data["env"].([]interface{}); ok {
		reqs.Env = l.interfaceSliceToStringSlice(env)
	}

	return nil
}

// interfaceSliceToStringSlice converts []interface{} to []string
func (l *SkillLoader) interfaceSliceToStringSlice(slice []interface{}) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

// ExtractActionsFromContent extracts available actions from skill content
func (l *SkillLoader) ExtractActionsFromContent(content string) []string {
	var actions []string

	// Look for action patterns in the markdown content
	// This is a heuristic approach - skills may define actions in various ways

	// Pattern 1: Look for command examples with action-like names
	actionPattern := regexp.MustCompile(`(?i)(?:action|command|do|execute)\s*:?\s*([a-zA-Z_][a-zA-Z0-9_-]+)`)
	matches := actionPattern.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) > 1 {
			actions = append(actions, match[1])
		}
	}

	// Pattern 2: Look for function/method definitions
	functionPattern := regexp.MustCompile(`(?i)(?:function|def|method)\s+([a-zA-Z_][a-zA-Z0-9_-]+)`)
	matches = functionPattern.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) > 1 {
			actions = append(actions, match[1])
		}
	}

	// Pattern 3: Look for headers that might indicate actions
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Check for markdown headers that look like actions
		if strings.HasPrefix(line, "##") {
			headerText := strings.TrimSpace(strings.TrimPrefix(line, "##"))
			headerText = strings.TrimPrefix(headerText, "#") // Handle ### etc
			headerText = strings.TrimSpace(headerText)

			// Simple action-like header names
			if l.isActionLike(headerText) {
				actions = append(actions, strings.ToLower(strings.ReplaceAll(headerText, " ", "_")))
			}
		}
	}

	// Remove duplicates and return
	return l.removeDuplicates(actions)
}

// isActionLike determines if a header text looks like an action
func (l *SkillLoader) isActionLike(text string) bool {
	actionWords := []string{
		"search", "read", "send", "list", "get", "check", "update", "create",
		"delete", "execute", "run", "start", "stop", "status", "monitor",
		"forecast", "current", "control", "toggle", "cleanup", "organize",
	}

	lowerText := strings.ToLower(text)
	for _, word := range actionWords {
		if strings.Contains(lowerText, word) {
			return true
		}
	}

	return false
}

// removeDuplicates removes duplicate strings from a slice
func (l *SkillLoader) removeDuplicates(slice []string) []string {
	keys := make(map[string]bool)
	var result []string

	for _, item := range slice {
		if !keys[item] {
			keys[item] = true
			result = append(result, item)
		}
	}

	return result
}
