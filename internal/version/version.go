package version

import (
	"fmt"
	"runtime"
	"strings"
)

// Build-time variables injected via ldflags
var (
	// Version is the semantic version, injected at build time
	Version = "dev"

	// GitCommit is the git commit hash, injected at build time
	GitCommit = "unknown"

	// GitTag is the git tag, injected at build time
	GitTag = ""

	// BuildDate is the build date, injected at build time
	BuildDate = "unknown"

	// GoVersion is the Go version used to build
	GoVersion = runtime.Version()

	// GitDirty indicates if the working tree was dirty during build
	GitDirty = ""
)

// Info returns version information
func Info() string {
	var version string

	// Use git tag if available, otherwise use Version
	if GitTag != "" && GitTag != "unknown" {
		version = GitTag
	} else {
		version = Version
	}

	// Add dirty marker if present (only once)
	if GitDirty == "true" && !strings.Contains(version, "-dirty") {
		version += "-dirty"
	}

	return version
}

// Full returns full version information
func Full() string {
	info := Info()

	// Add commit if available and different from version
	if GitCommit != "" && GitCommit != "unknown" && !strings.Contains(info, GitCommit[:7]) {
		info += fmt.Sprintf(" (%s)", GitCommit[:7])
	}

	return info
}

// BuildInfo returns detailed build information
type BuildInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	GitTag    string `json:"git_tag"`
	GitDirty  bool   `json:"git_dirty"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

// GetBuildInfo returns structured build information
func GetBuildInfo() BuildInfo {
	return BuildInfo{
		Version:   Info(),
		GitCommit: GitCommit,
		GitTag:    GitTag,
		GitDirty:  GitDirty == "true",
		BuildDate: BuildDate,
		GoVersion: GoVersion,
	}
}

// UserAgent returns a user agent string for HTTP clients
func UserAgent() string {
	return fmt.Sprintf("conduit/%s", Info())
}
