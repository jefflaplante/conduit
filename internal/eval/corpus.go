package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Corpus file format (design doc §3a): one file per case —
//
//	---
//	id: ev-0001
//	title: "..."
//	tags: [tools]
//	turn_budget: 8
//	deny_classes: [send_message]
//	expected_fragments: ["crc", "errors"]
//	cost_budget_usd: 0.10
//	cost:
//	  model: synthetic-model-a
//	  prompt_tokens: 1200
//	  completion_tokens: 300
//	goal: "Identifies interface errors as the first diagnostic step"
//	---
//	```json
//	{"turn":1,"role":"user","content":"..."}
//	{"turn":1,"role":"assistant","content":"...","tools":[...],"allow":["bash","read"]}
//	```
//
// The fenced block is JSONL, one Turn per line. The fence must be labeled
// ```json so plain markdown fences stay available for prose.
const corpusFence = "```json"

// ParseCorpusFile parses one corpus case file.
func ParseCorpusFile(path string) (*Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseCorpus(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ParseCorpus parses corpus file content: YAML frontmatter, then a fenced
// JSONL transcript.
func ParseCorpus(content string) (*Case, error) {
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}
	var c Case
	if err := yaml.Unmarshal([]byte(frontmatter), &c); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	turns, err := parseTurns(body)
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	c.Turns = turns
	if err := validateCase(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// splitFrontmatter separates leading YAML frontmatter from the body.
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", fmt.Errorf("case file must start with --- frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", fmt.Errorf("frontmatter not closed with ---")
	}
	frontmatter = strings.Join(lines[1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return frontmatter, body, nil
}

// parseTurns extracts the ```json fence and parses it as JSONL turns.
func parseTurns(body string) ([]Turn, error) {
	start := strings.Index(body, corpusFence)
	if start < 0 {
		return nil, fmt.Errorf("no %s fenced transcript found", corpusFence)
	}
	rest := body[start+len(corpusFence):]
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		return nil, fmt.Errorf("fence not terminated by newline")
	}
	rest = rest[nl+1:]
	end := strings.Index(rest, "```")
	if end < 0 {
		return nil, fmt.Errorf("transcript fence not closed with ```")
	}
	jsonl := rest[:end]

	var turns []Turn
	sc := bufio.NewScanner(strings.NewReader(jsonl))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var t Turn
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("transcript line %d: %w", lineNo, err)
		}
		turns = append(turns, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return turns, nil
}

// validateCase enforces the required-field invariants. Kept minimal for v0.1:
// id and title identify the case; at least one turn makes it a transcript.
func validateCase(c *Case) error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("case id is required")
	}
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("case title is required")
	}
	if len(c.Turns) == 0 {
		return fmt.Errorf("case must have at least one turn")
	}
	seen := make(map[string]bool)
	for _, t := range c.Turns {
		switch t.Role {
		case "user", "assistant":
		default:
			return fmt.Errorf("turn %d: role must be user or assistant, got %q", t.Turn, t.Role)
		}
		if t.Turn <= 0 {
			return fmt.Errorf("turn number must be >= 1, got %d", t.Turn)
		}
		key := fmt.Sprintf("%d:%s", t.Turn, t.Role)
		if seen[key] {
			return fmt.Errorf("duplicate turn %d role %s", t.Turn, t.Role)
		}
		seen[key] = true
	}
	return nil
}

// LoadCorpus loads every case file in dir (sorted by filename for stable
// run ordering). Directories are not recursed; files that fail to parse
// abort the load with the offending path named.
func LoadCorpus(dir string) ([]*Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	cases := make([]*Case, 0, len(names))
	for _, name := range names {
		c, err := ParseCorpusFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("no case files (*.md) found in %s", dir)
	}
	return cases, nil
}
