package brain

import (
	"regexp"
	"strings"
)

// BulkExtract is a parsed entry from a <!-- brain-extract ... --> block.
// It is intentionally separate from BulkEntry so call sites can add source
// context (e.g. "file:<relpath>") before forwarding to StoreBulk.
type BulkExtract struct {
	Key    string
	Value  string
	Tier   Tier
	Source string
}

// blockRegex matches a single brain-extract HTML comment block and captures
// the body between the opening and closing markers. The `(?s)` flag makes `.`
// span newlines so multi-line blocks are captured in one shot.
var blockRegex = regexp.MustCompile(`(?s)<!--\s*brain-extract\s*(.*?)\s*/brain-extract\s*-->`)

// lineRegex matches a single `key: "value"` entry inside a block. Any line
// that does not match is silently skipped (blanks, comments, malformed text,
// or unknown directives like `brain-extract-ignore`).
var lineRegex = regexp.MustCompile(`^\s*([a-zA-Z0-9_.-]+)\s*:\s*"(.*)"\s*$`)

// ExtractBulkEntries scans content for `<!-- brain-extract ... /brain-extract -->`
// blocks and returns one BulkExtract per well-formed `key: "value"` line found
// inside them. Malformed lines and unknown directives are ignored — the parser
// is deliberately tolerant so that a typo in a single file cannot panic the
// read path.
//
// Tier and Source on returned entries are left empty; callers are expected to
// assign sensible defaults (typically TierWorking and "file:<relpath>") before
// handing them to StoreBulk.
func ExtractBulkEntries(content string) []BulkExtract {
	if !strings.Contains(content, "brain-extract") {
		return nil
	}
	matches := blockRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []BulkExtract
	for _, m := range matches {
		body := m[1]
		for _, rawLine := range strings.Split(body, "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			kv := lineRegex.FindStringSubmatch(line)
			if kv == nil {
				continue
			}
			out = append(out, BulkExtract{
				Key:   kv[1],
				Value: kv[2],
			})
		}
	}
	return out
}
