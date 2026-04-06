package telegram

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"conduit/internal/protocol"
)

// handleVoiceMessage processes incoming Telegram voice messages by transcribing them to text.
func (a *Adapter) handleVoiceMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := strconv.FormatInt(update.Message.Chat.ID, 10)
	chatID := update.Message.Chat.ID

	// Check pairing status if pairing manager is enabled
	if a.pairingMgr != nil {
		isPaired, err := a.pairingMgr.HandlePairingForUser(ctx, b, userID, chatID)
		if err != nil {
			log.Printf("[Telegram] Error handling pairing for voice user %s: %v", userID, err)
			return
		}

		if !isPaired {
			log.Printf("[Telegram] User %s is not paired, voice message blocked", userID)
			return
		}
	}

	// Check if STT is configured
	if a.stt == nil {
		a.bot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Voice messages are not supported (speech-to-text not configured).",
		})
		return
	}

	// Send typing indicator while transcribing
	a.bot.SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID: chatID,
		Action: models.ChatActionTyping,
	})

	// Transcribe the voice message
	voice := update.Message.Voice
	transcription, err := a.transcribeVoice(ctx, voice)
	if err != nil {
		log.Printf("[Telegram] Failed to transcribe voice message: %v", err)
		a.bot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Sorry, I couldn't transcribe that voice message. Please try again or send text.",
		})
		return
	}

	// Build metadata
	metadata := map[string]string{
		"type":            "voice",
		"message_id":      strconv.Itoa(update.Message.ID),
		"chat_type":       string(update.Message.Chat.Type),
		"from_first_name": update.Message.From.FirstName,
		"from_last_name":  update.Message.From.LastName,
		"from_username":   update.Message.From.Username,
		"voice_duration":  strconv.Itoa(voice.Duration),
		"voice_mime_type": voice.MimeType,
		"voice_file_size": strconv.FormatInt(voice.FileSize, 10),
	}

	incomingMsg := &protocol.IncomingMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeIncomingMessage,
			ID:        a.generateMessageID(),
			Timestamp: time.Now(),
		},
		ChannelID:  a.id,
		SessionKey: fmt.Sprintf("telegram_%d", update.Message.Chat.ID),
		UserID:     strconv.FormatInt(update.Message.Chat.ID, 10),
		Text:       transcription,
		Metadata:   metadata,
	}

	// Send to incoming channel (non-blocking)
	select {
	case a.incoming <- incomingMsg:
		a.mutex.Lock()
		a.msgCount++
		a.mutex.Unlock()

		log.Printf("[Telegram] Received voice message from chat %d (duration=%ds, transcription=%d chars)",
			update.Message.Chat.ID, voice.Duration, len(transcription))
	default:
		log.Printf("[Telegram] Warning: incoming message channel is full, dropping voice message")
	}
}

// transcribeVoice downloads a voice message from Telegram and transcribes it using STT.
func (a *Adapter) transcribeVoice(ctx context.Context, voice *models.Voice) (string, error) {
	// Get file info from Telegram
	file, err := a.bot.GetFile(ctx, &bot.GetFileParams{
		FileID: voice.FileID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}

	// Get download URL
	downloadURL := a.bot.FileDownloadLink(file)

	// Download the audio file
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create download request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download voice file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status downloading voice file: %d", resp.StatusCode)
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read voice file: %w", err)
	}

	// Determine MIME type (default to audio/ogg for Telegram voice messages)
	mimeType := voice.MimeType
	if mimeType == "" {
		mimeType = "audio/ogg"
	}

	// Transcribe
	text, err := a.stt.Transcribe(ctx, audioData, mimeType)
	if err != nil {
		return "", fmt.Errorf("transcription failed: %w", err)
	}

	return strings.TrimSpace(text), nil
}
