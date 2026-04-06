package stt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscribe_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(whisperResponse{Text: "hello world"})
	}))
	defer server.Close()

	tr := NewWhisperTranscriber("test-key", "whisper-1")
	tr.SetBaseURL(server.URL)

	text, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "hello world", text)
}

func TestTranscribe_EmptyModel_DefaultsToWhisper1(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseMultipartForm(10 << 20)
		require.NoError(t, err)
		receivedModel = r.MultipartForm.Value["model"][0]

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(whisperResponse{Text: "ok"})
	}))
	defer server.Close()

	tr := NewWhisperTranscriber("test-key", "")
	tr.SetBaseURL(server.URL)

	_, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "whisper-1", receivedModel)
}

func TestTranscribe_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	tr := NewWhisperTranscriber("test-key", "whisper-1")
	tr.SetBaseURL(server.URL)

	_, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestTranscribe_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	tr := NewWhisperTranscriber("test-key", "whisper-1")
	tr.SetBaseURL(server.URL)

	_, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestTranscribe_EmptyTranscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(whisperResponse{Text: ""})
	}))
	defer server.Close()

	tr := NewWhisperTranscriber("test-key", "whisper-1")
	tr.SetBaseURL(server.URL)

	_, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty transcription")
}

func TestTranscribe_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	}))
	defer server.Close()

	tr := NewWhisperTranscriber("test-key", "whisper-1")
	tr.SetBaseURL(server.URL)

	_, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response JSON")
}

func TestTranscribe_MimeTypeMapping(t *testing.T) {
	tests := []struct {
		mimeType     string
		wantFilename string
	}{
		{"audio/ogg", "voice.ogg"},
		{"audio/mpeg", "voice.mp3"},
		{"audio/wav", "voice.wav"},
		{"audio/unknown", "voice.ogg"}, // default
	}

	for _, tc := range tests {
		t.Run(tc.mimeType, func(t *testing.T) {
			var receivedFilename string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err := r.ParseMultipartForm(10 << 20)
				require.NoError(t, err)
				fileHeaders := r.MultipartForm.File["file"]
				require.Len(t, fileHeaders, 1)
				receivedFilename = fileHeaders[0].Filename

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(whisperResponse{Text: "ok"})
			}))
			defer server.Close()

			tr := NewWhisperTranscriber("test-key", "whisper-1")
			tr.SetBaseURL(server.URL)

			_, err := tr.Transcribe(context.Background(), []byte("fake audio"), tc.mimeType)
			require.NoError(t, err)
			assert.Equal(t, tc.wantFilename, receivedFilename)
		})
	}
}

func TestTranscribe_AuthorizationHeader(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(whisperResponse{Text: "ok"})
	}))
	defer server.Close()

	tr := NewWhisperTranscriber("sk-test-secret-key", "whisper-1")
	tr.SetBaseURL(server.URL)

	_, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-test-secret-key", receivedAuth)
}
