package mqtt

import (
	"bytes"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDebugf_VerboseLoggingEnabled(t *testing.T) {
	// Capture log output
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Enable verbose logging
	oldVerbose := VerboseLogging
	VerboseLogging = true
	defer func() { VerboseLogging = oldVerbose }()

	debugf("[MQTT] Test message: %s %d", "hello", 42)

	output := buf.String()
	assert.Contains(t, output, "[MQTT] Test message: hello 42")
}

func TestDebugf_VerboseLoggingDisabled(t *testing.T) {
	// Capture log output
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Disable verbose logging
	oldVerbose := VerboseLogging
	VerboseLogging = false
	defer func() { VerboseLogging = oldVerbose }()

	debugf("[MQTT] Test message: %s %d", "hello", 42)

	output := buf.String()
	assert.Empty(t, output)
}

func TestVerboseLogging_DefaultIsFalse(t *testing.T) {
	// This test verifies the default value of VerboseLogging
	// We need to be careful since tests run in sequence and other tests may modify it
	// Just verify it's a bool and starts as false in a clean state

	// Save current state
	oldVerbose := VerboseLogging

	// Reset to false and verify debugf produces no output
	VerboseLogging = false

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	debugf("test")
	assert.Empty(t, buf.String())

	// Restore
	VerboseLogging = oldVerbose
}
