package skills

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// SkillValidator handles validation of skill requirements
type SkillValidator struct{}

// NewSkillValidator creates a new skill validator
func NewSkillValidator() *SkillValidator {
	return &SkillValidator{}
}

// ValidateRequirements checks if all skill requirements are satisfied
func (v *SkillValidator) ValidateRequirements(skill Skill) error {
	reqs := skill.Metadata.Conduit.Requires

	// Check binary requirements
	if err := v.validateBinaryRequirements(reqs, skill.Name); err != nil {
		return err
	}

	// Check file requirements
	if err := v.validateFileRequirements(reqs, skill.Name); err != nil {
		return err
	}

	// Check environment variable requirements
	if err := v.validateEnvironmentRequirements(reqs, skill.Name); err != nil {
		return err
	}

	return nil
}

// validateBinaryRequirements checks if required binaries are available
func (v *SkillValidator) validateBinaryRequirements(reqs SkillRequirements, skillName string) error {
	// Check anyBins - at least one must be available
	if len(reqs.AnyBins) > 0 {
		found := false
		var checkedBins []string

		for _, bin := range reqs.AnyBins {
			checkedBins = append(checkedBins, bin)
			if _, err := exec.LookPath(bin); err == nil {
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("skill %s requires at least one of these binaries: %v", skillName, checkedBins)
		}
	}

	// Check allBins - all must be available
	if len(reqs.AllBins) > 0 {
		var missingBins []string

		for _, bin := range reqs.AllBins {
			if _, err := exec.LookPath(bin); err != nil {
				missingBins = append(missingBins, bin)
			}
		}

		if len(missingBins) > 0 {
			return fmt.Errorf("skill %s requires these binaries: %v (missing: %v)",
				skillName, reqs.AllBins, missingBins)
		}
	}

	return nil
}

// validateFileRequirements checks if required files exist
func (v *SkillValidator) validateFileRequirements(reqs SkillRequirements, skillName string) error {
	var missingFiles []string

	for _, file := range reqs.Files {
		// Expand environment variables in file paths
		expandedPath := os.ExpandEnv(file)

		// Handle relative paths by making them absolute from home directory
		if !filepath.IsAbs(expandedPath) {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("skill %s: error getting home directory for file %s: %w",
					skillName, file, err)
			}
			expandedPath = filepath.Join(homeDir, expandedPath)
		}

		if _, err := os.Stat(expandedPath); err != nil {
			if os.IsNotExist(err) {
				missingFiles = append(missingFiles, file)
			} else {
				return fmt.Errorf("skill %s: error checking file %s: %w",
					skillName, file, err)
			}
		}
	}

	if len(missingFiles) > 0 {
		return fmt.Errorf("skill %s requires these files: %v", skillName, missingFiles)
	}

	return nil
}

// validateEnvironmentRequirements checks if required environment variables are set
func (v *SkillValidator) validateEnvironmentRequirements(reqs SkillRequirements, skillName string) error {
	var missingEnvVars []string

	for _, envVar := range reqs.Env {
		if os.Getenv(envVar) == "" {
			missingEnvVars = append(missingEnvVars, envVar)
		}
	}

	if len(missingEnvVars) > 0 {
		return fmt.Errorf("skill %s requires these environment variables: %v",
			skillName, missingEnvVars)
	}

	return nil
}

// ValidateSkillStructure performs structural validation of a skill
func (v *SkillValidator) ValidateSkillStructure(skill Skill) error {
	// Check required fields
	if skill.Name == "" {
		return fmt.Errorf("skill name cannot be empty")
	}

	if skill.Description == "" {
		return fmt.Errorf("skill %s: description cannot be empty", skill.Name)
	}

	if skill.Location == "" {
		return fmt.Errorf("skill %s: location cannot be empty", skill.Name)
	}

	// Validate skill location exists
	if _, err := os.Stat(skill.Location); err != nil {
		return fmt.Errorf("skill %s: location %s is not accessible: %w",
			skill.Name, skill.Location, err)
	}

	// Validate scripts exist and are executable
	for _, script := range skill.Scripts {
		scriptPath := filepath.Join(skill.Location, script.Path)

		info, err := os.Stat(scriptPath)
		if err != nil {
			return fmt.Errorf("skill %s: script %s not found at %s: %w",
				skill.Name, script.Name, scriptPath, err)
		}

		// Check if script is executable
		if info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("skill %s: script %s is not executable",
				skill.Name, script.Name)
		}
	}

	return nil
}

// GetMissingRequirements returns details about what requirements are missing
func (v *SkillValidator) GetMissingRequirements(skill Skill) map[string][]string {
	missing := make(map[string][]string)
	reqs := skill.Metadata.Conduit.Requires

	// Check binaries
	if len(reqs.AnyBins) > 0 {
		found := false
		for _, bin := range reqs.AnyBins {
			if _, err := exec.LookPath(bin); err == nil {
				found = true
				break
			}
		}
		if !found {
			missing["anyBins"] = reqs.AnyBins
		}
	}

	var missingAllBins []string
	for _, bin := range reqs.AllBins {
		if _, err := exec.LookPath(bin); err != nil {
			missingAllBins = append(missingAllBins, bin)
		}
	}
	if len(missingAllBins) > 0 {
		missing["allBins"] = missingAllBins
	}

	// Check files
	var missingFiles []string
	for _, file := range reqs.Files {
		expandedPath := os.ExpandEnv(file)
		if !filepath.IsAbs(expandedPath) {
			homeDir, _ := os.UserHomeDir()
			expandedPath = filepath.Join(homeDir, expandedPath)
		}
		if _, err := os.Stat(expandedPath); err != nil {
			missingFiles = append(missingFiles, file)
		}
	}
	if len(missingFiles) > 0 {
		missing["files"] = missingFiles
	}

	// Check environment variables
	var missingEnvVars []string
	for _, envVar := range reqs.Env {
		if os.Getenv(envVar) == "" {
			missingEnvVars = append(missingEnvVars, envVar)
		}
	}
	if len(missingEnvVars) > 0 {
		missing["env"] = missingEnvVars
	}

	return missing
}
