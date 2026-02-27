package skills

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// SkillDiscovery handles finding and loading skills from the filesystem
type SkillDiscovery struct {
	searchPaths []string
	loader      *SkillLoader
	validator   *SkillValidator
}

// NewSkillDiscovery creates a new skill discovery instance
func NewSkillDiscovery(searchPaths []string) *SkillDiscovery {
	return &SkillDiscovery{
		searchPaths: searchPaths,
		loader:      NewSkillLoader(),
		validator:   NewSkillValidator(),
	}
}

// DiscoverSkills searches all configured paths for skills
func (d *SkillDiscovery) DiscoverSkills(ctx context.Context) ([]Skill, error) {
	var allSkills []Skill

	// Default search paths if none configured
	if len(d.searchPaths) == 0 {
		d.searchPaths = d.getDefaultSearchPaths()
	}

	for _, basePath := range d.searchPaths {
		skillsInPath, err := d.discoverInPath(ctx, basePath)
		if err != nil {
			log.Printf("Error discovering skills in %s: %v", basePath, err)
			continue
		}
		allSkills = append(allSkills, skillsInPath...)
	}

	log.Printf("Discovered %d skills from %d search paths", len(allSkills), len(d.searchPaths))
	return allSkills, nil
}

// discoverInPath searches for skills in a specific directory
func (d *SkillDiscovery) discoverInPath(ctx context.Context, basePath string) ([]Skill, error) {
	// Check if path exists
	if _, err := os.Stat(basePath); err != nil {
		if os.IsNotExist(err) {
			log.Printf("Skills path %s does not exist, skipping", basePath)
			return []Skill{}, nil
		}
		return nil, fmt.Errorf("error accessing path %s: %w", basePath, err)
	}

	var skills []Skill

	entries, err := os.ReadDir(basePath)
	if err != nil {
		return nil, fmt.Errorf("error reading directory %s: %w", basePath, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(basePath, entry.Name())
		skillMdPath := filepath.Join(skillPath, "SKILL.md")

		// Check if SKILL.md exists
		if _, err := os.Stat(skillMdPath); err != nil {
			if os.IsNotExist(err) {
				continue // No SKILL.md, skip this directory
			}
			log.Printf("Error checking SKILL.md in %s: %v", skillPath, err)
			continue
		}

		skill, err := d.loadSkill(ctx, skillPath)
		if err != nil {
			log.Printf("Error loading skill %s: %v", entry.Name(), err)
			continue
		}

		// Validate skill requirements
		if err := d.validator.ValidateRequirements(*skill); err != nil {
			log.Printf("Skill %s failed validation: %v", skill.Name, err)
			continue
		}

		skills = append(skills, *skill)
	}

	return skills, nil
}

// loadSkill loads and parses a single skill from its directory
func (d *SkillDiscovery) loadSkill(ctx context.Context, skillPath string) (*Skill, error) {
	skillMdPath := filepath.Join(skillPath, "SKILL.md")

	skill, err := d.loader.LoadSkillFromFile(skillMdPath)
	if err != nil {
		return nil, fmt.Errorf("error parsing SKILL.md: %w", err)
	}

	// Set the skill location
	skill.Location = skillPath

	// Discover scripts in the skill directory
	scripts, err := d.discoverScripts(skillPath)
	if err != nil {
		log.Printf("Error discovering scripts for skill %s: %v", skill.Name, err)
	}
	skill.Scripts = scripts

	// Discover reference files
	references, err := d.discoverReferences(skillPath)
	if err != nil {
		log.Printf("Error discovering references for skill %s: %v", skill.Name, err)
	}
	skill.References = references

	return skill, nil
}

// discoverScripts finds executable scripts in the skill directory
func (d *SkillDiscovery) discoverScripts(skillPath string) ([]SkillScript, error) {
	var scripts []SkillScript

	err := filepath.Walk(skillPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Check if file is executable and has a recognized script extension
		if d.isExecutableScript(path, info) {
			relPath, _ := filepath.Rel(skillPath, path)
			script := SkillScript{
				Name:     strings.TrimSuffix(info.Name(), filepath.Ext(info.Name())),
				Path:     relPath,
				Language: d.detectScriptLanguage(path),
			}
			scripts = append(scripts, script)
		}

		return nil
	})

	return scripts, err
}

// discoverReferences finds supporting files for the skill
func (d *SkillDiscovery) discoverReferences(skillPath string) ([]SkillReference, error) {
	var references []SkillReference

	// Common reference file patterns
	referencePatterns := map[string]string{
		"README.md":    "documentation",
		"EXAMPLES.md":  "examples",
		"CONFIG.md":    "configuration",
		"*.json":       "data",
		"*.yaml":       "configuration",
		"*.yml":        "configuration",
		"package.json": "dependencies",
	}

	for pattern, refType := range referencePatterns {
		matches, err := filepath.Glob(filepath.Join(skillPath, pattern))
		if err != nil {
			continue
		}

		for _, match := range matches {
			if filepath.Base(match) == "SKILL.md" {
				continue // Skip the main skill file
			}

			relPath, _ := filepath.Rel(skillPath, match)
			reference := SkillReference{
				Name: filepath.Base(match),
				Path: relPath,
				Type: refType,
			}
			references = append(references, reference)
		}
	}

	return references, nil
}

// isExecutableScript determines if a file is an executable script
func (d *SkillDiscovery) isExecutableScript(path string, info os.FileInfo) bool {
	// Check if file is executable
	if info.Mode().Perm()&0111 == 0 {
		return false
	}

	// Check for known script extensions
	ext := strings.ToLower(filepath.Ext(path))
	scriptExtensions := []string{".sh", ".py", ".js", ".rb", ".pl", ".php"}

	for _, scriptExt := range scriptExtensions {
		if ext == scriptExt {
			return true
		}
	}

	return false
}

// detectScriptLanguage determines the scripting language based on file extension
func (d *SkillDiscovery) detectScriptLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	languageMap := map[string]string{
		".sh":  "bash",
		".py":  "python",
		".js":  "javascript",
		".rb":  "ruby",
		".pl":  "perl",
		".php": "php",
	}

	if lang, exists := languageMap[ext]; exists {
		return lang
	}

	return "unknown"
}

// getDefaultSearchPaths returns the default locations to search for skills
func (d *SkillDiscovery) getDefaultSearchPaths() []string {
	return []string{
		"/usr/local/lib/conduit/skills",
		"./skills",
		"/opt/conduit/skills",
	}
}
