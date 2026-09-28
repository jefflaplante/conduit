package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"conduit/internal/approval"
	"conduit/internal/protocol"
)

// SendMessage sends an outgoing message through Telegram
func (a *Adapter) SendMessage(msg *protocol.OutgoingMessage) error {
	b, ctx := a.session()
	if b == nil {
		return fmt.Errorf("bot not initialized")
	}

	// Convert user ID to chat ID (int64)
	chatID, err := strconv.ParseInt(msg.UserID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %s", msg.UserID)
	}

	// Handle photo sending when image_path is set in metadata
	if imagePath, ok := msg.Metadata["image_path"]; ok && imagePath != "" {
		fileData, err := os.ReadFile(imagePath)
		if err != nil {
			return fmt.Errorf("failed to read image file %s: %w", imagePath, err)
		}

		photoParams := &bot.SendPhotoParams{
			ChatID:    chatID,
			Photo:     &models.InputFileUpload{Filename: filepath.Base(imagePath), Data: bytes.NewReader(fileData)},
			Caption:   sanitizeUserFacingText(msg.Text),
			ParseMode: models.ParseModeMarkdownV1,
		}

		// Handle reply-to if specified in metadata
		if replyToStr, ok := msg.Metadata["reply_to_message_id"]; ok {
			if replyToInt, err := strconv.Atoi(replyToStr); err == nil {
				photoParams.ReplyParameters = &models.ReplyParameters{
					MessageID: replyToInt,
				}
			}
		}

		// Handle parse mode override from metadata
		if parseMode, ok := msg.Metadata["parse_mode"]; ok {
			photoParams.ParseMode = models.ParseMode(parseMode)
		}

		_, err = b.SendPhoto(ctx, photoParams)
		if err != nil {
			return fmt.Errorf("failed to send photo: %w", err)
		}

		log.Printf("[Telegram] Photo sent to chat %d (%s)", chatID, filepath.Base(imagePath))

		a.mutex.Lock()
		a.msgCount++
		a.mutex.Unlock()

		return nil
	}

	// conduit-31jg.43: approval notices are gateway-generated and must be
	// shown verbatim (the sanitizer strips <addr> tags, which could hide a
	// recipient from the human approving the action).
	if msg.Metadata[approval.MetaNotice] != "" {
		return a.sendApprovalNotice(chatID, msg)
	}

	// Process MEDIA protocol lines in the response
	mediaSender := NewMediaSender(b, ctx)
	textAfterMedia, mediaErrors := mediaSender.ProcessAndSendMedia(chatID, msg.Text)

	// If there were media errors, append them to the text
	for _, errMsg := range mediaErrors {
		if textAfterMedia != "" {
			textAfterMedia += "\n"
		}
		textAfterMedia += errMsg
	}

	// If no text remains after media processing, we're done
	if textAfterMedia == "" {
		log.Printf("[Telegram] Message was media-only, no text to send")

		a.mutex.Lock()
		a.msgCount++
		a.mutex.Unlock()

		return nil
	}

	// Sanitize text before sending
	sanitizedText := sanitizeUserFacingText(textAfterMedia)

	// If sanitized text is empty, skip sending
	if sanitizedText == "" {
		log.Printf("[Telegram] Message text empty after sanitization, skipping")
		return nil
	}

	chunks := splitMessage(sanitizedText, TelegramMessageLimit)

	parseModeOverride, hasParseModeOverride := msg.Metadata["parse_mode"]

	replyToID := 0
	if replyToStr, ok := msg.Metadata["reply_to_message_id"]; ok {
		if replyToInt, err := strconv.Atoi(replyToStr); err == nil {
			replyToID = replyToInt
		}
	}

	for i, chunk := range chunks {
		telegramText := convertToTelegramMarkdown(chunk)

		params := &bot.SendMessageParams{
			ChatID:    chatID,
			Text:      telegramText,
			ParseMode: models.ParseModeMarkdownV1,
		}

		// Only apply reply-to on the first chunk
		if i == 0 && replyToID != 0 {
			params.ReplyParameters = &models.ReplyParameters{MessageID: replyToID}
		}

		if hasParseModeOverride {
			params.ParseMode = models.ParseMode(parseModeOverride)
		}

		_, err = b.SendMessage(ctx, params)
		if err != nil {
			if strings.Contains(err.Error(), "can't parse entities") || strings.Contains(err.Error(), "message is too long") {
				log.Printf("[Telegram] chunk %d/%d markdown failed, retrying plain text: %v", i+1, len(chunks), err)
				params.ParseMode = ""
				params.Text = chunk
				_, err = b.SendMessage(ctx, params)
				if err != nil {
					return fmt.Errorf("failed to send chunk %d/%d (plain text fallback): %w", i+1, len(chunks), err)
				}
			} else {
				return fmt.Errorf("failed to send chunk %d/%d: %w", i+1, len(chunks), err)
			}
		}
	}

	if len(chunks) > 1 {
		log.Printf("[Telegram] Message sent to chat in %d parts (%d chars, sanitized from %d)", len(chunks), len(sanitizedText), len(msg.Text))
	} else {
		log.Printf("[Telegram] Message sent to chat (%d chars, sanitized from %d)", len(sanitizedText), len(msg.Text))
	}

	a.mutex.Lock()
	a.msgCount++
	a.mutex.Unlock()

	return nil
}

// sendApprovalNotice sends an approval prompt/result as plain text with no
// sanitizing or markdown conversion. Approve/Deny inline buttons go on the
// last chunk; pressing one arrives as a callback query whose Data is the
// reply text ("YES <code>"), which is exactly what a typed reply would be
// (conduit-31jg.43).
func (a *Adapter) sendApprovalNotice(chatID int64, msg *protocol.OutgoingMessage) error {
	b, ctx := a.session()
	if b == nil {
		return fmt.Errorf("bot not initialized")
	}
	var markup models.ReplyMarkup
	if raw := msg.Metadata[approval.MetaChoices]; raw != "" {
		var choices []approval.Choice
		if err := json.Unmarshal([]byte(raw), &choices); err == nil && len(choices) > 0 {
			row := make([]models.InlineKeyboardButton, 0, len(choices))
			for _, c := range choices {
				if c.Label == "" || c.Reply == "" || len(c.Reply) > 64 { // Telegram callback_data limit
					continue
				}
				row = append(row, models.InlineKeyboardButton{Text: c.Label, CallbackData: c.Reply})
			}
			if len(row) > 0 {
				markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
			}
		}
	}
	chunks := splitMessage(msg.Text, TelegramMessageLimit)
	for i, chunk := range chunks {
		params := &bot.SendMessageParams{ChatID: chatID, Text: chunk}
		if i == len(chunks)-1 && markup != nil {
			params.ReplyMarkup = markup
		}
		if _, err := b.SendMessage(ctx, params); err != nil {
			return fmt.Errorf("failed to send approval notice chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	a.mutex.Lock()
	a.msgCount++
	a.mutex.Unlock()
	return nil
}

// SendMessageWithID sends a message and returns the message ID (for later editing)
func (a *Adapter) SendMessageWithID(chatID int64, text string) (int, error) {
	b, ctx := a.session()
	if b == nil {
		return 0, fmt.Errorf("bot not initialized")
	}

	sanitizedText := sanitizeUserFacingText(text)
	telegramText := convertToTelegramMarkdown(sanitizedText)

	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      telegramText,
		ParseMode: models.ParseModeMarkdownV1,
	}

	msg, err := b.SendMessage(ctx, params)
	if err != nil {
		// Fallback to plain text
		params.ParseMode = ""
		params.Text = sanitizedText
		msg, err = b.SendMessage(ctx, params)
		if err != nil {
			return 0, err
		}
	}

	return msg.ID, nil
}

// EditMessageText edits an existing message.
// When text exceeds TelegramMessageLimit the edit is refused so the caller can
// fall back to sending a fresh (splittable) message via SendMessage.
func (a *Adapter) EditMessageText(chatID int64, messageID int, text string) error {
	b, ctx := a.session()
	if b == nil {
		return fmt.Errorf("bot not initialized")
	}

	if len(text) > TelegramMessageLimit {
		return fmt.Errorf("text %d chars exceeds Telegram limit %d; caller should fall back to SendMessage", len(text), TelegramMessageLimit)
	}

	sanitizedText := sanitizeUserFacingText(text)
	telegramText := convertToTelegramMarkdown(sanitizedText)

	params := &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      telegramText,
		ParseMode: models.ParseModeMarkdownV1,
	}

	_, err := b.EditMessageText(ctx, params)
	if err != nil {
		// Fallback to plain text if markdown fails
		if strings.Contains(err.Error(), "can't parse entities") {
			params.ParseMode = ""
			params.Text = sanitizedText
			_, err = b.EditMessageText(ctx, params)
		}
		if err != nil {
			// Ignore "message not modified" errors
			if strings.Contains(err.Error(), "message is not modified") {
				return nil
			}
			return err
		}
	}

	return nil
}

// DeleteMessage deletes a message (used for silent response cleanup)
func (a *Adapter) DeleteMessage(chatID int64, messageID int) error {
	b, ctx := a.session()
	if b == nil {
		return fmt.Errorf("bot not initialized")
	}

	params := &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: messageID,
	}

	_, err := b.DeleteMessage(ctx, params)
	if err != nil {
		// Ignore "message not found" errors
		if strings.Contains(err.Error(), "message to delete not found") {
			return nil
		}
		return err
	}

	return nil
}

// SendTypingIndicator sends a "typing" chat action to show the bot is thinking
func (a *Adapter) SendTypingIndicator(chatID string) error {
	b, ctx := a.session()
	if b == nil {
		return fmt.Errorf("bot not initialized")
	}

	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %s", chatID)
	}

	_, err = b.SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID: chatIDInt,
		Action: models.ChatActionTyping,
	})
	return err
}
