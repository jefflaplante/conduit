package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"conduit/internal/protocol"
	"conduit/internal/stt"
)

// mockSTT implements stt.Transcriber for testing.
type mockSTT struct {
	text string
	err  error
}

func (m *mockSTT) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error) {
	return m.text, m.err
}

// voiceMockBot extends mockBot with configurable GetFile/FileDownloadLink behavior.
type voiceMockBot struct {
	mockBot
	getFileResp      *models.File
	getFileErr       error
	fileDownloadLink string
}

func (m *voiceMockBot) GetFile(ctx context.Context, params *bot.GetFileParams) (*models.File, error) {
	if m.getFileErr != nil {
		return nil, m.getFileErr
	}
	if m.getFileResp != nil {
		return m.getFileResp, nil
	}
	return &models.File{FileID: params.FileID, FilePath: "voice/file_0.oga"}, nil
}

func (m *voiceMockBot) FileDownloadLink(f *models.File) string {
	return m.fileDownloadLink
}

func newTestVoiceAdapter(b botAPI, transcriber stt.Transcriber, bufSize int) (*Adapter, chan *protocol.IncomingMessage) {
	incoming := make(chan *protocol.IncomingMessage, bufSize)
	ctx, cancel := context.WithCancel(context.Background())
	a := &Adapter{
		id:       "test",
		bot:      b,
		stt:      transcriber,
		incoming: incoming,
		ctx:      ctx,
		cancel:   cancel,
	}
	return a, incoming
}

func makeVoiceUpdate(chatID int64, fileID string, duration int, mimeType string) *models.Update {
	return &models.Update{
		Message: &models.Message{
			ID: 100,
			Chat: models.Chat{
				ID:   chatID,
				Type: models.ChatTypePrivate,
			},
			From: &models.User{
				ID:        chatID,
				FirstName: "Test",
				LastName:  "User",
				Username:  "testuser",
			},
			Voice: &models.Voice{
				FileID:   fileID,
				Duration: duration,
				MimeType: mimeType,
				FileSize: 4096,
			},
		},
	}
}

func TestHandleVoiceMessage_Success(t *testing.T) {
	// Set up an httptest server to serve fake audio bytes
	audioData := []byte("fake-ogg-audio-data")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		w.Write(audioData)
	}))
	defer srv.Close()

	mb := &voiceMockBot{
		getFileResp:      &models.File{FileID: "voice123", FilePath: "voice/file_0.oga"},
		fileDownloadLink: srv.URL + "/file/voice_0.oga",
	}
	transcriber := &mockSTT{text: "hello world"}

	adapter, incoming := newTestVoiceAdapter(mb, transcriber, 10)
	defer adapter.cancel()

	update := makeVoiceUpdate(12345, "voice123", 5, "audio/ogg")
	adapter.handleVoiceMessage(context.Background(), nil, update)

	// Should receive a message on the incoming channel
	select {
	case msg := <-incoming:
		assert.Equal(t, "hello world", msg.Text)
		assert.Equal(t, "test", msg.ChannelID)
		assert.Equal(t, "telegram_12345", msg.SessionKey)
		assert.Equal(t, "12345", msg.UserID)
		assert.Equal(t, "voice", msg.Metadata["type"])
		assert.Equal(t, "5", msg.Metadata["voice_duration"])
		assert.Equal(t, "audio/ogg", msg.Metadata["voice_mime_type"])
		assert.Equal(t, "4096", msg.Metadata["voice_file_size"])
		assert.Equal(t, "100", msg.Metadata["message_id"])
		assert.Equal(t, "Test", msg.Metadata["from_first_name"])
		assert.Equal(t, "testuser", msg.Metadata["from_username"])
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for incoming message")
	}

	// msgCount should have been incremented
	adapter.mutex.RLock()
	assert.Equal(t, int64(1), adapter.msgCount)
	adapter.mutex.RUnlock()

	// No error messages should have been sent
	assert.Empty(t, mb.sendMessageCalls)
}

func TestHandleVoiceMessage_NoSTT(t *testing.T) {
	mb := &voiceMockBot{}
	// stt is nil
	adapter, incoming := newTestVoiceAdapter(mb, nil, 10)
	defer adapter.cancel()

	update := makeVoiceUpdate(12345, "voice123", 3, "audio/ogg")
	adapter.handleVoiceMessage(context.Background(), nil, update)

	// Should have sent an error message to the user
	require.Len(t, mb.sendMessageCalls, 1)
	assert.Contains(t, mb.sendMessageCalls[0].Text, "not supported")
	assert.Equal(t, int64(12345), mb.sendMessageCalls[0].ChatID)

	// No message should appear on the incoming channel
	select {
	case msg := <-incoming:
		t.Fatalf("expected no incoming message, got: %+v", msg)
	default:
		// expected
	}
}

func TestHandleVoiceMessage_TranscriptionFails(t *testing.T) {
	// Audio download succeeds but transcription fails
	audioData := []byte("fake-audio")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(audioData)
	}))
	defer srv.Close()

	mb := &voiceMockBot{
		getFileResp:      &models.File{FileID: "voice456", FilePath: "voice/file_1.oga"},
		fileDownloadLink: srv.URL + "/file/voice_1.oga",
	}
	transcriber := &mockSTT{err: fmt.Errorf("whisper API unavailable")}

	adapter, incoming := newTestVoiceAdapter(mb, transcriber, 10)
	defer adapter.cancel()

	update := makeVoiceUpdate(12345, "voice456", 10, "audio/ogg")
	adapter.handleVoiceMessage(context.Background(), nil, update)

	// Should have sent an error message to the user
	require.Len(t, mb.sendMessageCalls, 1)
	assert.Contains(t, mb.sendMessageCalls[0].Text, "couldn't transcribe")
	assert.Equal(t, int64(12345), mb.sendMessageCalls[0].ChatID)

	// No message on incoming channel
	select {
	case msg := <-incoming:
		t.Fatalf("expected no incoming message, got: %+v", msg)
	default:
		// expected
	}
}

func TestHandleVoiceMessage_DownloadFails(t *testing.T) {
	mb := &voiceMockBot{
		getFileErr: fmt.Errorf("file not found on Telegram servers"),
	}
	transcriber := &mockSTT{text: "should not reach"}

	adapter, incoming := newTestVoiceAdapter(mb, transcriber, 10)
	defer adapter.cancel()

	update := makeVoiceUpdate(12345, "badfile", 2, "audio/ogg")
	adapter.handleVoiceMessage(context.Background(), nil, update)

	// Should have sent an error message to the user
	require.Len(t, mb.sendMessageCalls, 1)
	assert.Contains(t, mb.sendMessageCalls[0].Text, "couldn't transcribe")
	assert.Equal(t, int64(12345), mb.sendMessageCalls[0].ChatID)

	// No message on incoming channel
	select {
	case msg := <-incoming:
		t.Fatalf("expected no incoming message, got: %+v", msg)
	default:
		// expected
	}
}

func TestHandleVoiceMessage_ChannelFull(t *testing.T) {
	// Buffer size 0 means the channel is always full (unbuffered, no receiver)
	audioData := []byte("fake-audio")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(audioData)
	}))
	defer srv.Close()

	mb := &voiceMockBot{
		getFileResp:      &models.File{FileID: "voice789", FilePath: "voice/file_2.oga"},
		fileDownloadLink: srv.URL + "/file/voice_2.oga",
	}
	transcriber := &mockSTT{text: "hello world"}

	adapter, _ := newTestVoiceAdapter(mb, transcriber, 0)
	defer adapter.cancel()

	update := makeVoiceUpdate(12345, "voice789", 5, "audio/ogg")

	// Should not panic
	assert.NotPanics(t, func() {
		adapter.handleVoiceMessage(context.Background(), nil, update)
	})

	// No error message should be sent to user (this is a dropped message, not an error)
	assert.Empty(t, mb.sendMessageCalls)

	// msgCount should NOT have been incremented (message was dropped)
	adapter.mutex.RLock()
	assert.Equal(t, int64(0), adapter.msgCount)
	adapter.mutex.RUnlock()
}
