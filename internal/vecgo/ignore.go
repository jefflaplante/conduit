package vecgo

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// ignoreRules holds parsed .vectorignore patterns for filtering files
// from vector indexing.
type ignoreRules struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	pattern  string // cleaned pattern for matching
	matchDir bool   // true if pattern ends with '/' (match directory prefix)
}

// loadIgnoreFile reads and parses a .vectorignore file.
// Returns empty rules (match nothing) if the file doesn't exist or can't be read.
func loadIgnoreFile(path string) *ignoreRules {
	f, err := os.Open(path)
	if err != nil {
		return &ignoreRules{}
	}
	defer f.Close()

	var patterns []ignorePattern
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasSuffix(line, "/") {
			patterns = append(patterns, ignorePattern{
				pattern:  strings.TrimSuffix(line, "/"),
				matchDir: true,
			})
		} else {
			patterns = append(patterns, ignorePattern{
				pattern: line,
			})
		}
	}

	return &ignoreRules{patterns: patterns}
}

// isIgnored checks if a relative path should be excluded from indexing.
func (r *ignoreRules) isIgnored(relPath string) bool {
	if r == nil || len(r.patterns) == 0 {
		return false
	}

	relPath = filepath.ToSlash(relPath)
	filename := filepath.Base(relPath)

	for _, p := range r.patterns {
		if p.matchDir {
			// Directory pattern: match if relPath starts with the directory name
			if relPath == p.pattern || strings.HasPrefix(relPath, p.pattern+"/") {
				return true
			}
			continue
		}

		// If pattern contains '/', match against full relative path
		if strings.Contains(p.pattern, "/") {
			if matched, _ := filepath.Match(p.pattern, relPath); matched {
				return true
			}
			continue
		}

		// Pattern without '/': match against filename only
		if matched, _ := filepath.Match(p.pattern, filename); matched {
			return true
		}
	}

	return false
}
