package telegram

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"conduit/internal/protocol"
	"conduit/internal/redact"
)

// Supported image MIME types for vision analysis.
var supportedImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// handlePhotoMessage processes incoming Telegram photo messages by downloading
// the image and attaching it to the message for LLM vision analysis.
func (a *Adapter) handlePhotoMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := strconv.FormatInt(update.Message.Chat.ID, 10)
	chatID := update.Message.Chat.ID

	// Check pairing status if pairing manager is enabled
	if a.pairingMgr != nil {
		isPaired, err := a.pairingMgr.HandlePairingForUser(ctx, b, userID, chatID)
		if err != nil {
			log.Printf("[Telegram] Error handling pairing for photo user %s: %v", userID, err)
			return
		}

		if !isPaired {
			log.Printf("[Telegram] User %s is not paired, photo blocked", userID)
			return
		}
	}

	// Send typing indicator while downloading
	a.getBot().SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID: chatID,
		Action: models.ChatActionTyping,
	})

	// Select highest resolution photo (Telegram sorts ascending by size)
	photos := update.Message.Photo
	bestPhoto := photos[len(photos)-1]

	// Download the photo
	imageData, err := a.downloadPhoto(ctx, &bestPhoto)
	if err != nil {
		log.Printf("[Telegram] Failed to download photo: %v", err)
		a.getBot().SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Sorry, I couldn't process that photo. Please try again.",
		})
		return
	}

	// Detect MIME type from content
	mimeType := http.DetectContentType(imageData)
	if !supportedImageTypes[mimeType] {
		// Default to JPEG for unrecognized image types from Telegram
		mimeType = "image/jpeg"
	}

	// Build metadata
	metadata := map[string]string{
		"type":            "photo",
		"message_id":      strconv.Itoa(update.Message.ID),
		"chat_type":       string(update.Message.Chat.Type),
		"from_first_name": update.Message.From.FirstName,
		"from_last_name":  update.Message.From.LastName,
		"from_username":   update.Message.From.Username,
		"photo_count":     strconv.Itoa(len(photos)),
		"photo_width":     strconv.Itoa(bestPhoto.Width),
		"photo_height":    strconv.Itoa(bestPhoto.Height),
	}

	incomingMsg := &protocol.IncomingMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeIncomingMessage,
			ID:        a.generateMessageID(),
			Timestamp: time.Now(),
		},
		ChannelID:  a.id,
		SessionKey: fmt.Sprintf("telegram_%d", update.Message.Chat.ID),
		UserID:     userID,
		Text:       update.Message.Caption,
		Metadata:   metadata,
		Attachments: []protocol.Attachment{
			{
				Type:      "image",
				MediaType: mimeType,
				Data:      imageData,
			},
		},
	}

	// Send to incoming channel (non-blocking)
	select {
	case a.incoming <- incomingMsg:
		a.mutex.Lock()
		a.msgCount++
		a.mutex.Unlock()

		log.Printf("[Telegram] Received photo from chat %d (%dx%d, %s, %d bytes)",
			chatID, bestPhoto.Width, bestPhoto.Height, mimeType, len(imageData))
	default:
		log.Printf("[Telegram] Warning: incoming message channel is full, dropping photo")
	}
}

// downloadPhoto downloads a photo from Telegram's servers.
func (a *Adapter) downloadPhoto(ctx context.Context, photo *models.PhotoSize) ([]byte, error) {
	// Get file info from Telegram
	file, err := a.getBot().GetFile(ctx, &bot.GetFileParams{
		FileID: photo.FileID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", redact.Error(err))
	}

	// Get download URL
	downloadURL := a.getBot().FileDownloadLink(file)

	// Download the image file (URL embeds the bot token; errors are scrubbed).
	data, err := downloadTelegramFile(ctx, a.downloadClient(), downloadURL, "photo")
	if err != nil {
		return nil, err
	}

	// Validate size (20MB limit — Telegram's max file size)
	const maxPhotoSize = 20 * 1024 * 1024
	if len(data) > maxPhotoSize {
		return nil, fmt.Errorf("photo too large: %d bytes (max %d)", len(data), maxPhotoSize)
	}

	return data, nil
}
