package mqtt

import "errors"

var (
	ErrPublishNotAllowed = errors.New("mqtt: publishing is not allowed (publish_allowed is false)")
	ErrNotConnected      = errors.New("mqtt: client not connected")
)
