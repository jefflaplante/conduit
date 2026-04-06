package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// WhisperTranscriber implements Transcriber using OpenAI Whisper API.
type WhisperTranscriber struct {
	apiKey     string
	model      string
	httpClient *http.Client
	baseURL    string
}

// NewWhisperTranscriber creates a new WhisperTranscriber with the given API key and model.
// If model is empty, it defaults to "whisper-1".
func NewWhisperTranscriber(apiKey, model string) *WhisperTranscriber {
	if model == "" {
		model = "whisper-1"
	}
	return &WhisperTranscriber{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: "https://api.openai.com",
	}
}

// SetBaseURL overrides the API base URL (useful for testing).
func (w *WhisperTranscriber) SetBaseURL(url string) {
	w.baseURL = url
}

// whisperResponse represents the JSON response from the Whisper API.
type whisperResponse struct {
	Text string `json:"text"`
}

// Transcribe sends audio bytes to the OpenAI Whisper API and returns the transcribed text.
func (w *WhisperTranscriber) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error) {
	filename := filenameForMIME(mimeType)

	// Build multipart form body.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("stt: create form file: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("stt: write audio data: %w", err)
	}

	if err := writer.WriteField("model", w.model); err != nil {
		return "", fmt.Errorf("stt: write model field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("stt: close multipart writer: %w", err)
	}

	// Build request.
	url := w.baseURL + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("stt: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Execute request.
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("stt: read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", fmt.Errorf("stt: whisper API returned status %d: %s", resp.StatusCode, snippet)
	}

	var result whisperResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("stt: parse response JSON: %w", err)
	}

	if result.Text == "" {
		return "", fmt.Errorf("stt: whisper API returned empty transcription")
	}

	return result.Text, nil
}

// filenameForMIME returns an appropriate filename for the given MIME type.
func filenameForMIME(mimeType string) string {
	switch mimeType {
	case "audio/mpeg":
		return "voice.mp3"
	case "audio/wav":
		return "voice.wav"
	case "audio/ogg":
		return "voice.ogg"
	default:
		return "voice.ogg"
	}
}
