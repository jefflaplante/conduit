package tools

import (
	"context"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"

	"conduit/internal/tools/types"
)

// brainExtractMarker is a cheap substring check before running the real regex.
// The extract parser itself is also defensive but this avoids any work for the
// overwhelmingly common case of files that contain no hints.
const brainExtractMarker = "brain-extract"

// extractBlockRegex matches a single brain-extract HTML comment block and
// captures the body between the opening and closing markers ((?s) lets `.`
// span newlines). extractLineRegex matches one `key: "value"` entry inside a
// block; any line that does not match is silently skipped.
var extractLineRegex = regexp.MustCompile(`^\s*([a-zA-Z0-9_.-]+)\s*:\s*"(.*)"\s*$`)
var extractBlockRegex = regexp.MustCompile(`(?s)<!--\s*brain-extract\s*(.*?)\s*/brain-extract\s*-->`)

// textualExtensions lists file extensions we will scan for brain-extract hints.
// An empty extension is also allowed (e.g. MEMORY, LICENSE); for that case we
// additionally require the content to be valid UTF-8 via a simple ASCII sniff.
var textualExtensions = map[string]bool{
	".md":   true,
	".txt":  true,
	".yaml": true,
	".yml":  true,
	".json": true,
}

// maybeExtractBrainFacts scans textual file content for brain-extract blocks and
// forwards the parsed entries to BrainService.StoreBulk. All errors are logged
// (slog) and swallowed; the read must not fail because of a parsing or storage
// issue in this hook.
func (t *ReadFileTool) maybeExtractBrainFacts(ctx context.Context, userPath, resolvedPath string, content []byte) {
	if t.registry == nil || t.registry.services == nil || t.registry.services.Brain == nil {
		return
	}
	// Cheap substring check — skips binaries and normal text files alike.
	if !strings.Contains(string(content), brainExtractMarker) {
		return
	}
	// Extension sniff — only scan files we're reasonably sure are textual.
	ext := strings.ToLower(filepath.Ext(resolvedPath))
	if ext != "" && !textualExtensions[ext] {
		return
	}
	if ext == "" && !looksTextual(content) {
		return
	}

	entries := parseBrainExtractBlocks(string(content))
	if len(entries) == 0 {
		return
	}

	source := "file:" + userPath
	bulk := make([]types.BrainBulkEntry, len(entries))
	for i, e := range entries {
		bulk[i] = types.BrainBulkEntry{
			Key:    e.Key,
			Value:  e.Value,
			Tier:   types.BrainTierWorking,
			Source: source,
		}
	}
	if err := t.registry.services.Brain.StoreBulk(ctx, bulk); err != nil {
		slog.Warn("Read: brain-extract StoreBulk failed",
			"path", userPath, "resolved_path", resolvedPath,
			"entries", len(bulk), "err", err)
		return
	}
	slog.Debug("Read: auto-stored brain-extract entries",
		"path", userPath, "entries", len(bulk))
}

// extractedEntry is the internal shape returned by parseBrainExtractBlocks.
type extractedEntry struct {
	Key   string
	Value string
}

// parseBrainExtractBlocks scans content for `<!-- brain-extract ... /brain-extract -->`
// blocks and returns one entry per well-formed `key: "value"` line inside them.
// Malformed lines and unknown directives are ignored: the parser is
// deliberately tolerant so a typo in one file cannot break the read path.
func parseBrainExtractBlocks(content string) []extractedEntry {
	matches := extractBlockRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []extractedEntry
	for _, m := range matches {
		body := m[1]
		for _, rawLine := range strings.Split(body, "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			kv := extractLineRegex.FindStringSubmatch(line)
			if kv == nil {
				continue
			}
			out = append(out, extractedEntry{Key: kv[1], Value: kv[2]})
		}
	}
	return out
}

// looksTextual is a lightweight UTF-8/ASCII sniff used when no extension is
// present. It rejects buffers containing NUL bytes or a high density of
// non-printable bytes; it is not meant to be perfect, just safe.
func looksTextual(b []byte) bool {
	const sample = 512
	n := len(b)
	if n > sample {
		n = sample
	}
	if n == 0 {
		return true
	}
	nonPrintable := 0
	for i := 0; i < n; i++ {
		c := b[i]
		if c == 0 {
			return false
		}
		if c < 0x09 || (c > 0x0D && c < 0x20) {
			nonPrintable++
		}
	}
	return nonPrintable*10 < n // <10% non-printable → treat as text
}
