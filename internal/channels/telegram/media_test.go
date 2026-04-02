package telegram

import (
	"context"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMediaLines(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedMedia []MediaLine
		expectedText  string
	}{
		{
			name:          "no media lines",
			input:         "Hello, this is a regular message.",
			expectedMedia: nil,
			expectedText:  "Hello, this is a regular message.",
		},
		{
			name:  "voice base64",
			input: "Here's your audio:\nMEDIA:voice:SGVsbG8gV29ybGQ=\nEnjoy!",
			expectedMedia: []MediaLine{
				{Type: MediaTypeVoice, Content: "SGVsbG8gV29ybGQ="},
			},
			expectedText: "Here's your audio:\n\nEnjoy!",
		},
		{
			name:  "audio URL",
			input: "Playing song:\nMEDIA:audio:https://example.com/song.mp3",
			expectedMedia: []MediaLine{
				{Type: MediaTypeAudio, Content: "https://example.com/song.mp3"},
			},
			expectedText: "Playing song:",
		},
		{
			name:  "tts with text",
			input: "MEDIA:tts:Hello, how are you today?",
			expectedMedia: []MediaLine{
				{Type: MediaTypeTTS, Content: "Hello, how are you today?"},
			},
			expectedText: "",
		},
		{
			name:  "media with caption",
			input: "MEDIA:voice:SGVsbG8=|Voice message for you",
			expectedMedia: []MediaLine{
				{Type: MediaTypeVoice, Content: "SGVsbG8=", Caption: "Voice message for you"},
			},
			expectedText: "",
		},
		{
			name:  "multiple media lines",
			input: "MEDIA:voice:YXVkaW8x\nSome text in between\nMEDIA:audio:https://example.com/audio.mp3\nMore text",
			expectedMedia: []MediaLine{
				{Type: MediaTypeVoice, Content: "YXVkaW8x"},
				{Type: MediaTypeAudio, Content: "https://example.com/audio.mp3"},
			},
			expectedText: "Some text in between\n\nMore text",
		},
		{
			name:  "simple MEDIA path format (TTS tool output)",
			input: "Here's the audio:\nMEDIA: /workspace/audio/tts_20240101_120000.ogg\nDone!",
			expectedMedia: []MediaLine{
				{Type: MediaTypePath, Content: "/workspace/audio/tts_20240101_120000.ogg"},
			},
			expectedText: "Here's the audio:\n\nDone!",
		},
		{
			name:  "mixed formats",
			input: "MEDIA:voice:YXVkaW8=\nMEDIA: /path/to/file.ogg\nText response",
			expectedMedia: []MediaLine{
				{Type: MediaTypeVoice, Content: "YXVkaW8="},
				{Type: MediaTypePath, Content: "/path/to/file.ogg"},
			},
			expectedText: "Text response",
		},
		{
			name:  "case insensitive type",
			input: "MEDIA:VOICE:SGVsbG8=",
			expectedMedia: []MediaLine{
				{Type: MediaTypeVoice, Content: "SGVsbG8="},
			},
			expectedText: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaLines, remainingText := ParseMediaLines(tt.input)

			assert.Equal(t, len(tt.expectedMedia), len(mediaLines), "media line count mismatch")
			for i, expected := range tt.expectedMedia {
				if i < len(mediaLines) {
					assert.Equal(t, expected.Type, mediaLines[i].Type, "media type mismatch at index %d", i)
					assert.Equal(t, expected.Content, mediaLines[i].Content, "media content mismatch at index %d", i)
					assert.Equal(t, expected.Caption, mediaLines[i].Caption, "media caption mismatch at index %d", i)
				}
			}
			assert.Equal(t, tt.expectedText, remainingText, "remaining text mismatch")
		})
	}
}

// mockBotForMedia implements botAPI for testing media sending
type mockBotForMedia struct {
	sentVoice    []*bot.SendVoiceParams
	sentAudio    []*bot.SendAudioParams
	sentMessages []*bot.SendMessageParams
	voiceError   error
	audioError   error
}

func (m *mockBotForMedia) Start(ctx context.Context)        {}
func (m *mockBotForMedia) StartWebhook(ctx context.Context) {}
func (m *mockBotForMedia) SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	m.sentMessages = append(m.sentMessages, params)
	return &models.Message{ID: 1}, nil
}
func (m *mockBotForMedia) SendPhoto(ctx context.Context, params *bot.SendPhotoParams) (*models.Message, error) {
	return &models.Message{ID: 1}, nil
}
func (m *mockBotForMedia) SendVoice(ctx context.Context, params *bot.SendVoiceParams) (*models.Message, error) {
	m.sentVoice = append(m.sentVoice, params)
	if m.voiceError != nil {
		return nil, m.voiceError
	}
	return &models.Message{ID: 1}, nil
}
func (m *mockBotForMedia) SendAudio(ctx context.Context, params *bot.SendAudioParams) (*models.Message, error) {
	m.sentAudio = append(m.sentAudio, params)
	if m.audioError != nil {
		return nil, m.audioError
	}
	return &models.Message{ID: 1}, nil
}
func (m *mockBotForMedia) EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error) {
	return &models.Message{ID: 1}, nil
}
func (m *mockBotForMedia) DeleteMessage(ctx context.Context, params *bot.DeleteMessageParams) (bool, error) {
	return true, nil
}
func (m *mockBotForMedia) GetMe(ctx context.Context) (*models.User, error) {
	return &models.User{ID: 123, Username: "test_bot"}, nil
}
func (m *mockBotForMedia) AnswerCallbackQuery(ctx context.Context, params *bot.AnswerCallbackQueryParams) (bool, error) {
	return true, nil
}
func (m *mockBotForMedia) SendChatAction(ctx context.Context, params *bot.SendChatActionParams) (bool, error) {
	return true, nil
}
func (m *mockBotForMedia) SetMyCommands(ctx context.Context, params *bot.SetMyCommandsParams) (bool, error) {
	return true, nil
}

func TestMediaSender_SendVoiceBase64(t *testing.T) {
	mockBot := &mockBotForMedia{}
	sender := NewMediaSender(mockBot, context.Background())

	// Test sending base64 encoded audio
	media := MediaLine{
		Type:    MediaTypeVoice,
		Content: "SGVsbG8gV29ybGQ=", // "Hello World" in base64
		Caption: "Test voice",
	}

	errMsg, err := sender.SendMedia(123456, media)
	require.NoError(t, err)
	assert.Empty(t, errMsg)
	assert.Len(t, mockBot.sentVoice, 1)
	assert.Equal(t, int64(123456), mockBot.sentVoice[0].ChatID)
	assert.Equal(t, "Test voice", mockBot.sentVoice[0].Caption)
}

func TestMediaSender_SendAudioURL(t *testing.T) {
	mockBot := &mockBotForMedia{}
	sender := NewMediaSender(mockBot, context.Background())

	media := MediaLine{
		Type:    MediaTypeAudio,
		Content: "https://example.com/audio.mp3",
	}

	errMsg, err := sender.SendMedia(123456, media)
	require.NoError(t, err)
	assert.Empty(t, errMsg)
	assert.Len(t, mockBot.sentAudio, 1)
}

func TestMediaSender_TTSWithoutProvider(t *testing.T) {
	mockBot := &mockBotForMedia{}
	sender := NewMediaSender(mockBot, context.Background())

	media := MediaLine{
		Type:    MediaTypeTTS,
		Content: "Hello, this is a test",
	}

	errMsg, err := sender.SendMedia(123456, media)
	require.Error(t, err)
	assert.Contains(t, errMsg, "TTS not available")
}

func TestMediaSender_ProcessAndSendMedia(t *testing.T) {
	mockBot := &mockBotForMedia{}
	sender := NewMediaSender(mockBot, context.Background())

	input := "Here's your voice message:\nMEDIA:voice:SGVsbG8=\nHope you enjoy it!"

	cleanedText, errors := sender.ProcessAndSendMedia(123456, input)

	assert.Empty(t, errors)
	assert.Equal(t, "Here's your voice message:\n\nHope you enjoy it!", cleanedText)
	assert.Len(t, mockBot.sentVoice, 1)
}

func TestMediaSender_InvalidBase64(t *testing.T) {
	mockBot := &mockBotForMedia{}
	sender := NewMediaSender(mockBot, context.Background())

	media := MediaLine{
		Type:    MediaTypeVoice,
		Content: "not-valid-base64!!!",
	}

	errMsg, err := sender.SendMedia(123456, media)
	require.Error(t, err)
	assert.Contains(t, errMsg, "invalid audio data")
}

func TestParseMediaLines_CollapseExcessiveNewlines(t *testing.T) {
	input := "Before\n\n\nMEDIA:voice:SGVsbG8=\n\n\n\nAfter"
	_, text := ParseMediaLines(input)

	// Should collapse excessive newlines to double newlines
	assert.NotContains(t, text, "\n\n\n")
	assert.Equal(t, "Before\n\nAfter", text)
}

func TestMediaSender_UnsupportedType(t *testing.T) {
	mockBot := &mockBotForMedia{}
	sender := NewMediaSender(mockBot, context.Background())

	media := MediaLine{
		Type:    MediaType("unknown"),
		Content: "some content",
	}

	errMsg, err := sender.SendMedia(123456, media)
	require.Error(t, err)
	assert.Contains(t, errMsg, "Unsupported media type")
}

func TestParseMediaLines_URLWithQueryParams(t *testing.T) {
	input := "MEDIA:audio:https://example.com/audio.mp3?token=abc123&format=high"
	mediaLines, text := ParseMediaLines(input)

	require.Len(t, mediaLines, 1)
	assert.Equal(t, MediaTypeAudio, mediaLines[0].Type)
	assert.Equal(t, "https://example.com/audio.mp3?token=abc123&format=high", mediaLines[0].Content)
	assert.Empty(t, text)
}

func TestParseMediaLines_CaptionWithSpecialChars(t *testing.T) {
	input := "MEDIA:voice:SGVsbG8=|Hello! How's it going?"
	mediaLines, _ := ParseMediaLines(input)

	require.Len(t, mediaLines, 1)
	assert.Equal(t, "Hello! How's it going?", mediaLines[0].Caption)
}
