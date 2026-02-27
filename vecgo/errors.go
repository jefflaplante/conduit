package vecgo

import (
	"errors"
	"fmt"
)

// Common errors
var (
	ErrNotTrained     = errors.New("vecgo: embedder not trained")
	ErrEmptyQuery     = errors.New("vecgo: empty query")
	ErrEmptyCorpus    = errors.New("vecgo: empty corpus")
	ErrDimMismatch    = errors.New("vecgo: vector dimension mismatch")
	ErrNotFound       = errors.New("vecgo: vector not found")
	ErrONNXDisabled   = errors.New("vecgo: ONNX support not compiled (use -tags onnx)")
	ErrStorageCorrupt = errors.New("vecgo: storage data corrupted")
)

// Error wraps errors with operation context.
type Error struct {
	Op  string
	Err error
}

func (e *Error) Error() string {
	return fmt.Sprintf("vecgo.%s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// WrapError wraps an error with operation context.
func WrapError(op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Op: op, Err: err}
}
