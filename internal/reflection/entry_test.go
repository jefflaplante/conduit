package reflection

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEntry(t *testing.T) {
	before := time.Now()
	entry := NewEntry("system", TypeToolOutcome, OutcomeSuccess)
	after := time.Now()

	require.NotEmpty(t, entry.ID, "ID should be set")
	assert.Len(t, entry.ID, 36, "ID should be a UUID string (36 chars with hyphens)")

	assert.False(t, entry.Timestamp.IsZero(), "Timestamp should be set")
	assert.False(t, entry.Timestamp.Before(before), "Timestamp should be >= test start")
	assert.False(t, entry.Timestamp.After(after), "Timestamp should be <= test end")

	assert.Equal(t, "system", entry.Source)
	assert.Equal(t, TypeToolOutcome, entry.Type)
	assert.Equal(t, OutcomeSuccess, entry.Outcome)

	// Defaults for unset fields
	assert.Empty(t, entry.SessionKey)
	assert.Empty(t, entry.Tool)
	assert.Equal(t, 0, entry.RetryCount)
	assert.Equal(t, time.Duration(0), entry.Duration)
	assert.Empty(t, entry.Insight)
	assert.Equal(t, 0, entry.Score)
	assert.Nil(t, entry.Tags)
	assert.Nil(t, entry.RelatedKeys)
}

func TestNewEntryUniqueIDs(t *testing.T) {
	e1 := NewEntry("model", TypeLearned, OutcomeSuccess)
	e2 := NewEntry("model", TypeLearned, OutcomeSuccess)
	assert.NotEqual(t, e1.ID, e2.ID, "Each entry should have a unique ID")
}

func TestShouldCapture_All(t *testing.T) {
	cfg := &ReflectionConfig{CaptureLevel: "all"}

	assert.True(t, cfg.ShouldCapture(OutcomeSuccess))
	assert.True(t, cfg.ShouldCapture(OutcomeFailure))
	assert.True(t, cfg.ShouldCapture(OutcomePartial))
	assert.True(t, cfg.ShouldCapture(OutcomeTimeout))
}

func TestShouldCapture_Failures(t *testing.T) {
	cfg := &ReflectionConfig{CaptureLevel: "failures"}

	assert.False(t, cfg.ShouldCapture(OutcomeSuccess))
	assert.True(t, cfg.ShouldCapture(OutcomeFailure))
	assert.False(t, cfg.ShouldCapture(OutcomePartial))
	assert.True(t, cfg.ShouldCapture(OutcomeTimeout))
}

func TestShouldCapture_Anomalies(t *testing.T) {
	cfg := &ReflectionConfig{CaptureLevel: "anomalies"}

	assert.False(t, cfg.ShouldCapture(OutcomeSuccess))
	assert.True(t, cfg.ShouldCapture(OutcomeFailure))
	assert.True(t, cfg.ShouldCapture(OutcomePartial))
	assert.True(t, cfg.ShouldCapture(OutcomeTimeout))
}

func TestShouldCapture_UnknownLevel(t *testing.T) {
	cfg := &ReflectionConfig{CaptureLevel: "bogus"}

	// Unknown levels fall back to capturing everything.
	assert.True(t, cfg.ShouldCapture(OutcomeSuccess))
	assert.True(t, cfg.ShouldCapture(OutcomeFailure))
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.True(t, cfg.Enabled)
	assert.Equal(t, "all", cfg.CaptureLevel)
	assert.Equal(t, 30, cfg.RetentionDays)
}
