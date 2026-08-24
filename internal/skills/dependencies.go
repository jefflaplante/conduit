package skills

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Dependency size/budget constants. Values chosen to keep deps useful without
// blowing the system prompt for small-context models. See conduit-1hdg.
const (
	// MaxDependencyFileBytes is the per-file cap. Deps larger than this are skipped.
	MaxDependencyFileBytes int64 = 32 * 1024 // 32 KB
	// MaxDependenciesPerSkillBytes is the aggregate cap for all deps of one skill.
	MaxDependenciesPerSkillBytes int64 = 128 * 1024 // 128 KB
)

// backtickPathPattern matches inline-code spans: `some/path.md`.
var backtickPathPattern = regexp.MustCompile("`([^`]+)`")

// depsHeadingPattern matches the Dependencies section heading. Accepts optional
// leading decorations (emoji, punctuation) between "##" and the word
// "Dependencies" (case-insensitive). Examples that match:
//
//	## Dependencies
//	## 📎 Dependencies
//	### Dependencies
//	**Dependencies**:
var depsHeadingPattern = regexp.MustCompile(`(?i)^\s*(#{2,6}\s+.*?dependencies\s*$|\*\*dependencies:?\*\*:?\s*$)`)

// nextHeadingPattern marks the end of the Dependencies section: any following
// H1/H2/H3 heading at the same or higher level closes the section.
var nextHeadingPattern = regexp.MustCompile(`^\s*#{1,6}\s+`)

// referenceLinePrefixes identifies bullet labels whose paths should be auto-loaded.
// Entries labelled "Skills:" or "Env:" are informational and intentionally skipped.
var referenceLinePrefixes = []string{
	"references:",
	"reference:",
	"local:",
	"files:",
	"file:",
	"docs:",
	"doc:",
}

// parseDependencyPaths extracts reference-file paths from the Dependencies
// section of a skill's markdown content. It returns paths exactly as declared
// (relative). Missing section or no matches → empty slice, no error.
func parseDependencyPaths(content string) []string {
	lines := strings.Split(content, "\n")
	section, found := extractDependenciesSection(lines)
	if !found {
		return nil
	}

	var paths []string
	seen := make(map[string]bool)

	for _, line := range section {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !isReferenceBullet(trimmed) {
			continue
		}
		for _, p := range extractBacktickPaths(trimmed) {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
	}

	return paths
}

// extractDependenciesSection returns the lines between the Dependencies heading
// and the next heading. The heading line itself is excluded.
func extractDependenciesSection(lines []string) ([]string, bool) {
	start := -1
	for i, line := range lines {
		if depsHeadingPattern.MatchString(line) {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return nil, false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if nextHeadingPattern.MatchString(lines[i]) {
			end = i
			break
		}
	}
	return lines[start:end], true
}

// isReferenceBullet reports whether the line looks like a bulleted dependency
// entry whose paths we should auto-load. It also accepts unlabelled bullets
// like "- `reference/foo.md`" as reference paths.
func isReferenceBullet(line string) bool {
	// Must be a bullet: "-", "*", or "+".
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] != '-' && trimmed[0] != '*' && trimmed[0] != '+' {
		return false
	}
	// Strip bullet marker.
	body := strings.TrimSpace(trimmed[1:])
	// Normalize the body's leading label by removing markdown bold/emphasis so
	// "**Env:**" and "Env:" can be compared uniformly.
	normalized := strings.ToLower(stripLeadingEmphasis(body))

	// Reject entries explicitly marked as non-file references.
	skipLabels := []string{"env:", "skills:", "skill:", "tools:", "tool:", "fallback:", "binary:", "binaries:", "workspace:", "shared:"}
	for _, lbl := range skipLabels {
		if strings.HasPrefix(normalized, lbl) {
			return false
		}
	}

	// Accept if line begins with a known reference label, or if it contains a
	// backticked path that looks like a file (has an extension).
	for _, prefix := range referenceLinePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}

	// Fallback: unlabelled bullet with a backticked file path.
	for _, candidate := range extractBacktickPaths(body) {
		if looksLikeFilePath(candidate) {
			return true
		}
	}
	return false
}

// extractBacktickPaths returns the contents of every backtick-delimited span
// on a line (e.g. `foo/bar.md`).
func extractBacktickPaths(line string) []string {
	matches := backtickPathPattern.FindAllStringSubmatch(line, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 2 {
			candidate := strings.TrimSpace(m[1])
			if looksLikeFilePath(candidate) {
				out = append(out, candidate)
			}
		}
	}
	return out
}

// stripLeadingEmphasis removes leading markdown bold/italic markers ("**",
// "__", "*", "_") so the caller can inspect the first label token.
func stripLeadingEmphasis(s string) string {
	for {
		trimmed := strings.TrimLeft(s, " \t")
		switch {
		case strings.HasPrefix(trimmed, "**"):
			s = trimmed[2:]
		case strings.HasPrefix(trimmed, "__"):
			s = trimmed[2:]
		case strings.HasPrefix(trimmed, "*"):
			s = trimmed[1:]
		case strings.HasPrefix(trimmed, "_"):
			s = trimmed[1:]
		default:
			return trimmed
		}
	}
}

// looksLikeFilePath filters obvious non-paths (shell commands, env-var tokens).
func looksLikeFilePath(s string) bool {
	if s == "" {
		return false
	}
	// Env var references or shell expansions.
	if strings.ContainsAny(s, "$ ") {
		return false
	}
	// Must have an extension to be treated as a file.
	ext := filepath.Ext(s)
	if ext == "" || len(ext) > 6 {
		return false
	}
	return true
}

// resolveDependencies loads the content of each declared dependency for a
// skill. Paths are resolved relative to skillDir. Files that are missing,
// unsafe (outside skillDir), or oversized are recorded with a flag but do not
// fail the skill load.
func resolveDependencies(skillDir string, paths []string) []SkillDependency {
	if len(paths) == 0 {
		return nil
	}

	absSkillDir, err := filepath.Abs(skillDir)
	if err != nil {
		absSkillDir = skillDir
	}

	deps := make([]SkillDependency, 0, len(paths))
	var totalBytes int64

	for _, rel := range paths {
		dep := SkillDependency{Path: rel}

		// Reject absolute paths and upward traversal — deps must live under
		// the skill dir to avoid smuggling arbitrary files into the prompt.
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			dep.Skipped = true
			dep.SkipReason = "path must be relative and contained within skill directory"
			log.Printf("[skills] skipping dependency %q: %s", rel, dep.SkipReason)
			deps = append(deps, dep)
			continue
		}

		resolved := filepath.Join(absSkillDir, rel)
		// Double-check containment after Join (defense in depth).
		if !strings.HasPrefix(resolved, absSkillDir+string(os.PathSeparator)) && resolved != absSkillDir {
			dep.Skipped = true
			dep.SkipReason = "resolved path escapes skill directory"
			log.Printf("[skills] skipping dependency %q: %s", rel, dep.SkipReason)
			deps = append(deps, dep)
			continue
		}
		dep.ResolvedPath = resolved

		info, err := os.Stat(resolved)
		if err != nil {
			dep.Missing = true
			if os.IsNotExist(err) {
				log.Printf("[skills] dependency not found: %s (skill=%s) — skill will still load",
					rel, filepath.Base(skillDir))
			} else {
				log.Printf("[skills] dependency stat failed: %s: %v", rel, err)
			}
			deps = append(deps, dep)
			continue
		}

		if info.IsDir() {
			dep.Skipped = true
			dep.SkipReason = "path is a directory"
			log.Printf("[skills] skipping dependency %q: is a directory", rel)
			deps = append(deps, dep)
			continue
		}

		dep.Size = info.Size()

		if info.Size() > MaxDependencyFileBytes {
			dep.Skipped = true
			dep.SkipReason = fmt.Sprintf("file exceeds %d byte cap (got %d)",
				MaxDependencyFileBytes, info.Size())
			log.Printf("[skills] skipping oversized dependency %s: %d bytes > %d cap",
				rel, info.Size(), MaxDependencyFileBytes)
			deps = append(deps, dep)
			continue
		}

		if totalBytes+info.Size() > MaxDependenciesPerSkillBytes {
			dep.Skipped = true
			dep.SkipReason = fmt.Sprintf("per-skill dependency budget exceeded (%d/%d bytes)",
				totalBytes, MaxDependenciesPerSkillBytes)
			log.Printf("[skills] skipping dependency %s: skill budget exhausted", rel)
			deps = append(deps, dep)
			continue
		}

		data, err := os.ReadFile(resolved)
		if err != nil {
			dep.Missing = true
			log.Printf("[skills] dependency read failed: %s: %v", rel, err)
			deps = append(deps, dep)
			continue
		}

		dep.Content = string(data)
		totalBytes += int64(len(data))
		deps = append(deps, dep)
	}

	return deps
}
