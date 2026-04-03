package mqtt

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrors(t *testing.T) {
	// Verify error variables are non-nil
	assert.NotNil(t, ErrPublishNotAllowed)
	assert.NotNil(t, ErrNotConnected)

	// Verify error messages
	assert.Equal(t, "mqtt: publishing is not allowed (publish_allowed is false)", ErrPublishNotAllowed.Error())
	assert.Equal(t, "mqtt: client not connected", ErrNotConnected.Error())
}

func TestErrors_Is(t *testing.T) {
	// Verify errors can be checked with errors.Is
	err := ErrPublishNotAllowed
	assert.True(t, errors.Is(err, ErrPublishNotAllowed))
	assert.False(t, errors.Is(err, ErrNotConnected))

	err = ErrNotConnected
	assert.True(t, errors.Is(err, ErrNotConnected))
	assert.False(t, errors.Is(err, ErrPublishNotAllowed))
}

func TestErrors_AreDistinct(t *testing.T) {
	// Verify the two errors are distinct
	assert.NotEqual(t, ErrPublishNotAllowed, ErrNotConnected)
}
