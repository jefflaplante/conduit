package telegram

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"conduit/pkg/protocol"
)

// mockBot implements botAPI for testing
type mockBot struct {
	sendMessageCalls []*bot.SendMessageParams
	sendPhotoCalls   []*bot.SendPhotoParams
	sendMessageResp  *models.Message
	sendPhotoResp    *models.Message
	sendMessageErr   error
	sendPhotoErr     error
}

func (m *mockBot) Start(ctx context.Context)        {}
func (m *mockBot) StartWebhook(ctx context.Context) {}

func (m *mockBot) SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	m.sendMessageCalls = append(m.sendMessageCalls, params)
	resp := m.sendMessageResp
	if resp == nil {
		resp = &models.Message{ID: 1}
	}
	return resp, m.sendMessageErr
}

func (m *mockBot) SendPhoto(ctx context.Context, params *bot.SendPhotoParams) (*models.Message, error) {
	m.sendPhotoCalls = append(m.sendPhotoCalls, params)
	resp := m.sendPhotoResp
	if resp == nil {
		resp = &models.Message{ID: 1}
	}
	return resp, m.sendPhotoErr
}

func (m *mockBot) EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error) {
	return &models.Message{ID: 1}, nil
}

func (m *mockBot) DeleteMessage(ctx context.Context, params *bot.DeleteMessageParams) (bool, error) {
	return true, nil
}

func (m *mockBot) GetMe(ctx context.Context) (*models.User, error) {
	return &models.User{ID: 1, Username: "testbot"}, nil
}

func (m *mockBot) AnswerCallbackQuery(ctx context.Context, params *bot.AnswerCallbackQueryParams) (bool, error) {
	return true, nil
}

func (m *mockBot) SendChatAction(ctx context.Context, params *bot.SendChatActionParams) (bool, error) {
	return true, nil
}

func (m *mockBot) SetMyCommands(ctx context.Context, params *bot.SetMyCommandsParams) (bool, error) {
	return true, nil
}

func newTestAdapter(mb *mockBot) *Adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &Adapter{
		id:     "test",
		name:   "test-adapter",
		bot:    mb,
		ctx:    ctx,
		cancel: cancel,
	}
}

func TestSendMessage_TextOnly(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID:   "12345",
		Text:     "hello world",
		Metadata: map[string]string{},
	})

	require.NoError(t, err)
	assert.Len(t, mb.sendPhotoCalls, 0, "should not call SendPhoto for text messages")
	assert.Len(t, mb.sendMessageCalls, 1, "should call SendMessage once")
	assert.Equal(t, int64(12345), mb.sendMessageCalls[0].ChatID)
}

func TestSendMessage_NilMetadata(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "12345",
		Text:   "hello",
	})

	require.NoError(t, err)
	assert.Len(t, mb.sendPhotoCalls, 0)
	assert.Len(t, mb.sendMessageCalls, 1)
}

func TestSendMessage_Photo(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	// Create a temp image file
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	imgData := []byte("fake-png-data")
	require.NoError(t, os.WriteFile(imgPath, imgData, 0644))

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "12345",
		Text:   "Check this out",
		Metadata: map[string]string{
			"image_path": imgPath,
		},
	})

	require.NoError(t, err)
	assert.Len(t, mb.sendMessageCalls, 0, "should not call SendMessage for photo")
	assert.Len(t, mb.sendPhotoCalls, 1, "should call SendPhoto once")

	call := mb.sendPhotoCalls[0]
	assert.Equal(t, int64(12345), call.ChatID)
	assert.Equal(t, "Check this out", call.Caption)
	assert.Equal(t, models.ParseModeMarkdownV1, call.ParseMode)

	// Verify the uploaded file data
	upload, ok := call.Photo.(*models.InputFileUpload)
	require.True(t, ok, "Photo should be an InputFileUpload")
	assert.Equal(t, "test.png", upload.Filename)
	data, err := io.ReadAll(upload.Data)
	require.NoError(t, err)
	assert.Equal(t, imgData, data)
}

func TestSendMessage_PhotoNoCaption(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "photo.jpg")
	require.NoError(t, os.WriteFile(imgPath, []byte("jpg-data"), 0644))

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "12345",
		Text:   "",
		Metadata: map[string]string{
			"image_path": imgPath,
		},
	})

	require.NoError(t, err)
	assert.Len(t, mb.sendPhotoCalls, 1)
	assert.Equal(t, "", mb.sendPhotoCalls[0].Caption)
}

func TestSendMessage_PhotoWithReplyTo(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "reply.png")
	require.NoError(t, os.WriteFile(imgPath, []byte("data"), 0644))

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "12345",
		Text:   "replying with image",
		Metadata: map[string]string{
			"image_path":          imgPath,
			"reply_to_message_id": "42",
		},
	})

	require.NoError(t, err)
	require.Len(t, mb.sendPhotoCalls, 1)
	require.NotNil(t, mb.sendPhotoCalls[0].ReplyParameters)
	assert.Equal(t, 42, mb.sendPhotoCalls[0].ReplyParameters.MessageID)
}

func TestSendMessage_PhotoWithParseMode(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "styled.png")
	require.NoError(t, os.WriteFile(imgPath, []byte("data"), 0644))

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "12345",
		Text:   "<b>bold caption</b>",
		Metadata: map[string]string{
			"image_path": imgPath,
			"parse_mode": "HTML",
		},
	})

	require.NoError(t, err)
	require.Len(t, mb.sendPhotoCalls, 1)
	assert.Equal(t, models.ParseMode("HTML"), mb.sendPhotoCalls[0].ParseMode)
}

func TestSendMessage_PhotoMissingFile(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "12345",
		Text:   "caption",
		Metadata: map[string]string{
			"image_path": "/tmp/does-not-exist-12345.png",
		},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read image file")
	assert.Len(t, mb.sendPhotoCalls, 0, "should not call SendPhoto when file is missing")
}

func TestSendMessage_PhotoSendError(t *testing.T) {
	mb := &mockBot{
		sendPhotoErr: fmt.Errorf("telegram API error"),
	}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "err.png")
	require.NoError(t, os.WriteFile(imgPath, []byte("data"), 0644))

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "12345",
		Text:   "caption",
		Metadata: map[string]string{
			"image_path": imgPath,
		},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send photo")
}

func TestSendMessage_InvalidChatID(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()

	err := adapter.SendMessage(&protocol.OutgoingMessage{
		UserID: "not-a-number",
		Text:   "hello",
		Metadata: map[string]string{
			"image_path": "/tmp/img.png",
		},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid chat ID")
}
