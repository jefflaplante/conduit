package types

import (
	"context"
	"time"
)

// SelfTestStatus represents the functional status of a tool.
type SelfTestStatus string

const (
	// SelfTestStatusOK indicates the tool is fully functional.
	SelfTestStatusOK SelfTestStatus = "ok"

	// SelfTestStatusDegraded indicates partial functionality (some features work).
	SelfTestStatusDegraded SelfTestStatus = "degraded"

	// SelfTestStatusFailed indicates the tool is not functional.
	SelfTestStatusFailed SelfTestStatus = "failed"
)

// SelfTestOptions configures how the self-test runs.
type SelfTestOptions struct {
	// Verbose requests additional detail in the result.
	Verbose bool

	// IncludeExamples requests that the result include usage examples if the test passes.
	IncludeExamples bool

	// CheckDependencies requests explicit dependency verification.
	CheckDependencies bool
}

// DefaultSelfTestOptions returns sensible defaults.
func DefaultSelfTestOptions() *SelfTestOptions {
	return &SelfTestOptions{
		Verbose:           false,
		IncludeExamples:   true,
		CheckDependencies: true,
	}
}

// DependencyStatus describes the health of a single dependency.
type DependencyStatus struct {
	// Name identifies the dependency (e.g., "ChannelSender", "MQTTService").
	Name string `json:"name"`

	// Required indicates if this dependency is essential for basic functionality.
	Required bool `json:"required"`

	// Available indicates if the dependency is currently accessible.
	Available bool `json:"available"`

	// Status provides more detail (e.g., "connected", "offline", "rate_limited").
	Status string `json:"status,omitempty"`

	// Message provides additional context about the dependency state.
	Message string `json:"message,omitempty"`
}

// SelfTestResult contains the outcome of a tool's self-test.
type SelfTestResult struct {
	// Status indicates whether the tool is functional.
	Status SelfTestStatus `json:"status"`

	// Message provides a human-readable summary of the test result.
	Message string `json:"message"`

	// Dependencies lists the status of required services/resources.
	Dependencies []DependencyStatus `json:"dependencies,omitempty"`

	// Capabilities lists what the tool can currently do.
	Capabilities []string `json:"capabilities,omitempty"`

	// UnavailableCapabilities lists features that are currently non-functional.
	UnavailableCapabilities []string `json:"unavailable_capabilities,omitempty"`

	// Examples provides usage examples when the tool is functional.
	Examples []ToolExample `json:"examples,omitempty"`

	// Suggestions provides actionable hints when the tool has issues.
	Suggestions []string `json:"suggestions,omitempty"`

	// TestDuration records how long the self-test took.
	TestDuration time.Duration `json:"test_duration"`

	// TestedAt records when the test was performed.
	TestedAt time.Time `json:"tested_at"`

	// Details contains additional diagnostic information (verbose mode).
	Details map[string]interface{} `json:"details,omitempty"`
}

// IsOK returns true if the tool is fully functional.
func (r *SelfTestResult) IsOK() bool {
	return r.Status == SelfTestStatusOK
}

// IsFunctional returns true if the tool has at least partial functionality.
func (r *SelfTestResult) IsFunctional() bool {
	return r.Status == SelfTestStatusOK || r.Status == SelfTestStatusDegraded
}

// SelfTester is an optional interface for tools that can verify their own functionality.
// AI models may call this to verify a tool works before relying on it for critical tasks.
type SelfTester interface {
	// SelfTest performs a functional check of the tool and returns a diagnostic result.
	// The test should be lightweight (ideally < 1 second) and non-destructive.
	SelfTest(ctx context.Context, opts *SelfTestOptions) *SelfTestResult
}
