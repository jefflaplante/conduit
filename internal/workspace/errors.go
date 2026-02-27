package workspace

import "errors"

// Common workspace context errors
var (
	// Security related errors
	ErrMissingSessionType = errors.New("security context missing session type")
	ErrInvalidSessionType = errors.New("invalid session type - must be 'main', 'shared', or 'isolated'")
	ErrAccessDenied       = errors.New("access denied to workspace file")
	ErrInvalidFilePath    = errors.New("invalid file path - potential security risk")

	// File operation errors
	ErrFileNotFound  = errors.New("workspace context file not found")
	ErrFileReadError = errors.New("failed to read workspace context file")
	ErrFileTooBig    = errors.New("workspace context file exceeds size limit")
	ErrMalformedFile = errors.New("workspace context file is malformed")

	// Cache errors
	ErrCacheFull      = errors.New("file cache is full")
	ErrCacheCorrupted = errors.New("file cache data is corrupted")

	// Context loading errors
	ErrWorkspaceDirNotFound = errors.New("workspace directory not found")
	ErrNoContextFiles       = errors.New("no context files found in workspace")
	ErrContextLoadTimeout   = errors.New("context loading timed out")
)
