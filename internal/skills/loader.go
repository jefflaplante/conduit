package skills

import (
	"fmt"
	"os"
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

	// Explicit action list — frontmatter "actions: [a, b]" or multi-line form.
	// When present, this is the authoritative action enum for the skill tool.
	if actionsData, ok := data["actions"].([]interface{}); ok {
		for _, a := range actionsData {
			if s, ok := a.(string); ok && strings.TrimSpace(s) != "" {
				skill.Actions = append(skill.Actions, strings.TrimSpace(s))
			}
		}
	}

	if argv, ok := data["argv"].(bool); ok {
		skill.Argv = argv
	}
	if pos, ok := data["positional"].(map[string]interface{}); ok {
		skill.Positional = make(map[string][]string, len(pos))
		for action, names := range pos {
			list, _ := names.([]interface{})
			for _, n := range list {
				if s, ok := n.(string); ok && strings.TrimSpace(s) != "" {
					skill.Positional[action] = append(skill.Positional[action], strings.TrimSpace(s))
				}
			}
		}
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

// ExtractActions returns the honest action set for a skill: the explicit
// frontmatter list when declared, otherwise the names of declared scripts.
// (The old ExtractActionsFromContent prose scraper — source of garbage enum
// entries like "NOT" and "a" pre-conduit-1jd9 — was removed entirely once no
// callers remained; see TestRealSkills_NoHollowActions for the live audit.)
func ExtractActions(skill Skill) []string {
	if len(skill.Actions) > 0 {
		return append([]string(nil), skill.Actions...)
	}
	if len(skill.Scripts) > 0 {
		names := make([]string, 0, len(skill.Scripts))
		for _, s := range skill.Scripts {
			if s.Name != "" {
				names = append(names, s.Name)
			}
		}
		return names
	}
	return nil
}
