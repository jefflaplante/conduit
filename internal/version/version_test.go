package version

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// saveVars captures current package variables and returns a restore function
func saveVars() func() {
	savedVersion := Version
	savedGitCommit := GitCommit
	savedGitTag := GitTag
	savedBuildDate := BuildDate
	savedGoVersion := GoVersion
	savedGitDirty := GitDirty

	return func() {
		Version = savedVersion
		GitCommit = savedGitCommit
		GitTag = savedGitTag
		BuildDate = savedBuildDate
		GoVersion = savedGoVersion
		GitDirty = savedGitDirty
	}
}

func TestInfo_DefaultValues(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "dev"
	GitCommit = "unknown"
	GitTag = ""
	GitDirty = ""

	result := Info()
	assert.Equal(t, "dev", result)
}

func TestInfo_WithGitTag(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = "v1.2.3"
	GitDirty = ""

	result := Info()
	assert.Equal(t, "v1.2.3", result)
}

func TestInfo_WithGitTagUnknown(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = "unknown"
	GitDirty = ""

	result := Info()
	assert.Equal(t, "1.0.0", result)
}

func TestInfo_WithGitDirty(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = ""
	GitDirty = "true"

	result := Info()
	assert.Equal(t, "1.0.0-dirty", result)
}

func TestInfo_GitTagWithDirty(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = "v1.2.3"
	GitDirty = "true"

	result := Info()
	assert.Equal(t, "v1.2.3-dirty", result)
}

func TestInfo_NoDuplicateDirtyMarker(t *testing.T) {
	restore := saveVars()
	defer restore()

	// If version already contains -dirty, don't add it again
	Version = "1.0.0-dirty"
	GitTag = ""
	GitDirty = "true"

	result := Info()
	assert.Equal(t, "1.0.0-dirty", result)
	assert.Equal(t, 1, strings.Count(result, "-dirty"))
}

func TestInfo_GitDirtyNotTrue(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = ""
	GitDirty = "false"

	result := Info()
	assert.Equal(t, "1.0.0", result)
}

func TestFull_DefaultValues(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "dev"
	GitCommit = "unknown"
	GitTag = ""
	GitDirty = ""

	result := Full()
	assert.Equal(t, "dev", result)
}

func TestFull_WithCommit(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitCommit = "abc1234567890"
	GitTag = ""
	GitDirty = ""

	result := Full()
	assert.Equal(t, "1.0.0 (abc1234)", result)
}

func TestFull_WithCommitAndTag(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitCommit = "abc1234567890"
	GitTag = "v2.0.0"
	GitDirty = ""

	result := Full()
	assert.Equal(t, "v2.0.0 (abc1234)", result)
}

func TestFull_CommitAlreadyInVersion(t *testing.T) {
	restore := saveVars()
	defer restore()

	// If version already contains the commit hash prefix, don't add it again
	Version = "1.0.0-abc1234"
	GitCommit = "abc1234567890"
	GitTag = ""
	GitDirty = ""

	result := Full()
	// Should not add commit again since it's already in the version string
	assert.Equal(t, "1.0.0-abc1234", result)
	assert.Equal(t, 1, strings.Count(result, "abc1234"))
}

func TestFull_EmptyCommit(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitCommit = ""
	GitTag = ""
	GitDirty = ""

	result := Full()
	assert.Equal(t, "1.0.0", result)
}

func TestFull_WithDirtyAndCommit(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitCommit = "abc1234567890"
	GitTag = ""
	GitDirty = "true"

	result := Full()
	assert.Equal(t, "1.0.0-dirty (abc1234)", result)
}

func TestGetBuildInfo_AllFields(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitCommit = "abc1234567890"
	GitTag = "v1.0.0"
	GitDirty = "true"
	BuildDate = "2024-01-15"
	GoVersion = "go1.21.0"

	info := GetBuildInfo()

	assert.Equal(t, "v1.0.0-dirty", info.Version)
	assert.Equal(t, "abc1234567890", info.GitCommit)
	assert.Equal(t, "v1.0.0", info.GitTag)
	assert.True(t, info.GitDirty)
	assert.Equal(t, "2024-01-15", info.BuildDate)
	assert.Equal(t, "go1.21.0", info.GoVersion)
}

func TestGetBuildInfo_GitDirtyFalse(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitCommit = "abc1234"
	GitTag = ""
	GitDirty = "false"
	BuildDate = "2024-01-15"
	GoVersion = "go1.21.0"

	info := GetBuildInfo()

	assert.False(t, info.GitDirty)
}

func TestGetBuildInfo_GitDirtyEmpty(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitCommit = "abc1234"
	GitTag = ""
	GitDirty = ""
	BuildDate = "2024-01-15"
	GoVersion = "go1.21.0"

	info := GetBuildInfo()

	assert.False(t, info.GitDirty)
}

func TestUserAgent(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = ""
	GitDirty = ""

	result := UserAgent()
	assert.Equal(t, "conduit/1.0.0", result)
}

func TestUserAgent_WithTag(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = "v2.0.0"
	GitDirty = ""

	result := UserAgent()
	assert.Equal(t, "conduit/v2.0.0", result)
}

func TestUserAgent_WithDirty(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "1.0.0"
	GitTag = ""
	GitDirty = "true"

	result := UserAgent()
	assert.Equal(t, "conduit/1.0.0-dirty", result)
}

func TestBuildInfo_StructFields(t *testing.T) {
	// Test that BuildInfo struct has the expected JSON tags
	info := BuildInfo{
		Version:   "1.0.0",
		GitCommit: "abc1234",
		GitTag:    "v1.0.0",
		GitDirty:  true,
		BuildDate: "2024-01-15",
		GoVersion: "go1.21.0",
	}

	require.Equal(t, "1.0.0", info.Version)
	require.Equal(t, "abc1234", info.GitCommit)
	require.Equal(t, "v1.0.0", info.GitTag)
	require.True(t, info.GitDirty)
	require.Equal(t, "2024-01-15", info.BuildDate)
	require.Equal(t, "go1.21.0", info.GoVersion)
}

func TestGoVersion_DefaultValue(t *testing.T) {
	restore := saveVars()
	defer restore()

	// Reset to default
	GoVersion = runtime.Version()

	// Should match runtime version
	assert.Equal(t, runtime.Version(), GoVersion)
}

func TestInfo_EmptyGitTag(t *testing.T) {
	restore := saveVars()
	defer restore()

	Version = "custom-version"
	GitTag = ""
	GitDirty = ""

	result := Info()
	assert.Equal(t, "custom-version", result)
}

func TestFull_ShortCommit(t *testing.T) {
	restore := saveVars()
	defer restore()

	// Test with commit exactly 7 chars
	Version = "1.0.0"
	GitCommit = "abc1234"
	GitTag = ""
	GitDirty = ""

	result := Full()
	assert.Equal(t, "1.0.0 (abc1234)", result)
}

func TestFull_LongCommit(t *testing.T) {
	restore := saveVars()
	defer restore()

	// Test with full 40-char commit hash
	Version = "1.0.0"
	GitCommit = "abc1234567890abcdef1234567890abcdef12345"
	GitTag = ""
	GitDirty = ""

	result := Full()
	assert.Equal(t, "1.0.0 (abc1234)", result)
}
