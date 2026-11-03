package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterSep delimits the YAML frontmatter block at the top of a case
// file.
const frontmatterSep = "---"

// LoadCaseFile parses one case file: YAML frontmatter (Case metadata +
// rubric), then a fenced ```json block of JSONL turn rows.
func LoadCaseFile(path string) (*Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseCase(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if c.ID == "" {
		return nil, fmt.Errorf("%s: case id is required", filepath.Base(path))
	}
	c.Source = path
	return c, nil
}

// ParseCase parses case file content. The body must contain exactly one
// fenced code block, holding the JSONL transcript.
func ParseCase(data []byte) (*Case, error) {
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var c Case
	if err := yaml.Unmarshal(fm, &c); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	turns, err := extractJSONBlock(string(body))
	if err != nil {
		return nil, err
	}
	c.Turns = turns
	normalizeCase(&c)
	return &c, nil
}

// splitFrontmatter splits leading "---\n...\n---" frontmatter from the body.
func splitFrontmatter(data []byte) ([]byte, []byte, error) {
	trimmed := bytes.TrimLeft(data, "\ufeff \t\r\n")
	lines := strings.Split(string(trimmed), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != frontmatterSep {
		return nil, nil, fmt.Errorf("missing frontmatter (expected leading %q line)", frontmatterSep)
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == frontmatterSep {
			fm := strings.Join(lines[1:i], "\n")
			body := strings.Join(lines[i+1:], "\n")
			return []byte(fm), []byte(body), nil
		}
	}
	return nil, nil, fmt.Errorf("unterminated frontmatter (no closing %q)", frontmatterSep)
}

// extractJSONBlock finds the single fenced code block in the body and parses
// it as JSONL turns.
func extractJSONBlock(body string) ([]Turn, error) {
	const fence = "```"
	var inside bool
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(trimmed), fence) {
			if inside {
				break
			}
			inside = true
			continue
		}
		if inside {
			lines = append(lines, trimmed)
		}
	}
	if !inside {
		return nil, fmt.Errorf("no fenced JSONL block found (expected a ```json section)")
	}
	turns, err := ParseTurns([]byte(strings.Join(lines, "\n")))
	if err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return nil, fmt.Errorf("turn transcript is empty")
	}
	return turns, nil
}

// ParseTurns parses JSONL transcript rows.
func ParseTurns(data []byte) ([]Turn, error) {
	var turns []Turn
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		var t Turn
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("turn line %d: %w", lineNo, err)
		}
		if t.Role == "" {
			return nil, fmt.Errorf("turn line %d: role is required", lineNo)
		}
		turns = append(turns, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return turns, nil
}

// LoadCorpus loads every *.md case file under dir, sorted by file name.
// A directory with no case files is an error.
func LoadCorpus(dir string) ([]*Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no eval cases (*.md) found in %s", dir)
	}
	sort.Strings(names)

	var cases []*Case
	for _, name := range names {
		c, err := LoadCaseFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, nil
}
