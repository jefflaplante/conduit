package fts

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Frontmatter is the structured metadata parsed from a markdown file's
// leading YAML block (bd-3o6h). It is per-file, not per-chunk. Unknown keys
// are ignored.
type Frontmatter struct {
	// Date is normalized to YYYY-MM-DD when it parses as a date; otherwise
	// the raw value is kept (and never matches date filters).
	Date     string   `json:"date,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	People   []string `json:"people,omitempty"`
	Projects []string `json:"projects,omitempty"`
	Category string   `json:"category,omitempty"`
	Summary  string   `json:"summary,omitempty"`
}

// rawFrontmatter is the YAML decoding target. Date is a node so unquoted
// YAML timestamps keep their source text instead of becoming time.Time.
type rawFrontmatter struct {
	Date     yaml.Node  `yaml:"date"`
	Tags     stringList `yaml:"tags"`
	People   stringList `yaml:"people"`
	Projects stringList `yaml:"projects"`
	Category string     `yaml:"category"`
	Summary  string     `yaml:"summary"`
}

// stringList accepts a YAML sequence of scalars or a single comma-separated
// scalar ("tags: solar, cron").
type stringList []string

func (l *stringList) UnmarshalYAML(n *yaml.Node) error {
	var raw []string
	switch n.Kind {
	case yaml.ScalarNode:
		raw = strings.Split(n.Value, ",")
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: list items must be scalars", item.Line)
			}
			raw = append(raw, item.Value)
		}
	default:
		return fmt.Errorf("line %d: expected a list or scalar", n.Line)
	}
	*l = cleanStringList(raw)
	return nil
}

// cleanStringList trims entries and drops empties and case-insensitive
// duplicates, keeping first-seen order and spelling.
func cleanStringList(in []string) []string {
	var out []string
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		key := strings.ToLower(s)
		if s == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// dateLayouts are the frontmatter date forms normalized to YYYY-MM-DD.
var dateLayouts = []string{
	"2006-01-02",
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// NormalizeDate returns s as YYYY-MM-DD and true when it parses as one of the
// accepted date layouts; otherwise the trimmed input and false.
func NormalizeDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return s, false
}

// ParseFrontmatter splits a leading YAML frontmatter block (between "---"
// lines; the closing line may also be "...") from the markdown body.
//
// ok is false when the content has no frontmatter or the block is malformed
// (unclosed, invalid YAML, not a mapping, wrong field types); body is then
// the content unchanged, so the file indexes exactly as if it had none.
func ParseFrontmatter(content string) (fm Frontmatter, body string, ok bool) {
	rest := strings.TrimPrefix(content, "\uFEFF")
	first, rest, found := strings.Cut(rest, "\n")
	if !found || strings.TrimRight(first, " \t\r") != "---" {
		return Frontmatter{}, content, false
	}

	var block []string
	closed := false
	for rest != "" {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == "---" || trimmed == "..." {
			closed = true
			break
		}
		block = append(block, line)
	}
	if !closed {
		return Frontmatter{}, content, false
	}

	yamlText := strings.Join(block, "\n")
	var raw rawFrontmatter
	if strings.TrimSpace(yamlText) != "" {
		var root yaml.Node
		if err := yaml.Unmarshal([]byte(yamlText), &root); err != nil {
			return Frontmatter{}, content, false
		}
		if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
			return Frontmatter{}, content, false
		}
		if err := root.Content[0].Decode(&raw); err != nil {
			return Frontmatter{}, content, false
		}
	}

	if raw.Date.Kind != 0 && raw.Date.Kind != yaml.ScalarNode {
		return Frontmatter{}, content, false
	}
	date, _ := NormalizeDate(raw.Date.Value)

	fm = Frontmatter{
		Date:     date,
		Tags:     raw.Tags,
		People:   raw.People,
		Projects: raw.Projects,
		Category: strings.TrimSpace(raw.Category),
		Summary:  strings.TrimSpace(raw.Summary),
	}
	return fm, rest, true
}
