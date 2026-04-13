package vecgo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIgnoreRules_CommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vectorignore")
	require.NoError(t, os.WriteFile(path, []byte("# this is a comment\n\n  \n# another comment\n"), 0644))

	rules := loadIgnoreFile(path)
	assert.Empty(t, rules.patterns)
	assert.False(t, rules.isIgnored("anything.md"))
}

func TestIgnoreRules_FilenameMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vectorignore")
	require.NoError(t, os.WriteFile(path, []byte("scratch.md\n*.tmp.md\n"), 0644))

	rules := loadIgnoreFile(path)

	// Exact filename match at root
	assert.True(t, rules.isIgnored("scratch.md"))
	// Filename match in subdirectory
	assert.True(t, rules.isIgnored("memory/scratch.md"))
	// Glob match
	assert.True(t, rules.isIgnored("notes.tmp.md"))
	assert.True(t, rules.isIgnored("deep/dir/stuff.tmp.md"))
	// Non-matching
	assert.False(t, rules.isIgnored("important.md"))
	assert.False(t, rules.isIgnored("scratch.txt"))
}

func TestIgnoreRules_PathMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vectorignore")
	require.NoError(t, os.WriteFile(path, []byte("drafts/*.md\n"), 0644))

	rules := loadIgnoreFile(path)

	assert.True(t, rules.isIgnored("drafts/wip.md"))
	assert.True(t, rules.isIgnored("drafts/ideas.md"))
	// Does not match deeper nesting (filepath.Match doesn't cross /)
	assert.False(t, rules.isIgnored("drafts/sub/deep.md"))
	// Does not match root-level files
	assert.False(t, rules.isIgnored("wip.md"))
}

func TestIgnoreRules_DirectoryMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vectorignore")
	require.NoError(t, os.WriteFile(path, []byte("archive/\n"), 0644))

	rules := loadIgnoreFile(path)

	assert.True(t, rules.isIgnored("archive/old.md"))
	assert.True(t, rules.isIgnored("archive/deep/nested.md"))
	// The directory name itself matches
	assert.True(t, rules.isIgnored("archive"))
	// Non-matching
	assert.False(t, rules.isIgnored("not-archive/file.md"))
	assert.False(t, rules.isIgnored("archivefile.md"))
}

func TestIgnoreRules_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vectorignore")
	require.NoError(t, os.WriteFile(path, []byte(""), 0644))

	rules := loadIgnoreFile(path)
	assert.False(t, rules.isIgnored("anything.md"))
}

func TestIgnoreRules_MissingFile(t *testing.T) {
	rules := loadIgnoreFile("/nonexistent/.vectorignore")
	assert.False(t, rules.isIgnored("anything.md"))
}

func TestIgnoreRules_NilRules(t *testing.T) {
	var rules *ignoreRules
	assert.False(t, rules.isIgnored("anything.md"))
}

func TestIgnoreRules_MixedPatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vectorignore")
	content := `# Scratch files
scratch.md
wip-*.md

# Drafts directory
drafts/

# Temp files with path
temp/*.md
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	rules := loadIgnoreFile(path)

	assert.True(t, rules.isIgnored("scratch.md"))
	assert.True(t, rules.isIgnored("wip-ideas.md"))
	assert.True(t, rules.isIgnored("drafts/anything.md"))
	assert.True(t, rules.isIgnored("temp/notes.md"))
	assert.False(t, rules.isIgnored("important.md"))
	assert.False(t, rules.isIgnored("final/notes.md"))
}
