package stt

import "context"

// Transcriber converts audio data to text.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error)
}
