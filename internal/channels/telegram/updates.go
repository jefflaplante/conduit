package telegram

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"conduit/internal/protocol"
)

// isDuplicateUpdate returns true if update.ID was seen recently. Telegram's
// polling API can redeliver the same update on retries; without this check we
// process the message twice. Entries older than 60s are swept periodically.
func (a *Adapter) isDuplicateUpdate(id int64) bool {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.seenUpdates == nil {
		a.seenUpdates = make(map[int64]time.Time)
	}

	if _, ok := a.seenUpdates[id]; ok {
		return true
	}
	a.seenUpdates[id] = time.Now()

	a.seenCalls++
	if a.seenCalls%1000 == 0 {
		cutoff := time.Now().Add(-60 * time.Second)
		for k, t := range a.seenUpdates {
			if t.Before(cutoff) {
				delete(a.seenUpdates, k)
			}
		}
	}
	return false
}

// handleUpdate processes incoming Telegram updates
func (a *Adapter) handleUpdate(ctx context.Context, b *bot.Bot, update *models.Update) {
	if a.isDuplicateUpdate(update.ID) {
		log.Printf("[Telegram] Duplicate update %d detected, skipping", update.ID)
		return
	}

	log.Printf("[Telegram] handleUpdate called with update type: message=%v, callback=%v",
		update.Message != nil, update.CallbackQuery != nil)

	// Handle text messages
	if update.Message != nil && update.Message.Text != "" {
		userID := strconv.FormatInt(update.Message.Chat.ID, 10)
		chatID := update.Message.Chat.ID

		// Check pairing status if pairing manager is enabled
		if a.pairingMgr != nil {
			isPaired, err := a.pairingMgr.HandlePairingForUser(ctx, b, userID, chatID)
			if err != nil {
				log.Printf("[Telegram] Error handling pairing for user %s: %v", userID, err)
				return // Don't process the message further
			}

			if !isPaired {
				log.Printf("[Telegram] User %s is not paired, message blocked", userID)
				return // User not paired, message was handled by pairing system
			}
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
			Text:       update.Message.Text,
			Metadata: map[string]string{
				"message_id":      strconv.Itoa(update.Message.ID),
				"chat_type":       string(update.Message.Chat.Type),
				"from_first_name": update.Message.From.FirstName,
				"from_last_name":  update.Message.From.LastName,
				"from_username":   update.Message.From.Username,
			},
		}

		// Send to incoming channel (non-blocking)
		select {
		case a.incoming <- incomingMsg:
			a.mutex.Lock()
			a.msgCount++
			a.mutex.Unlock()

			// Privacy-safe logging - no message content or user names
			log.Printf("[Telegram] Received message from chat %d (%d chars)",
				update.Message.Chat.ID, len(update.Message.Text))
		default:
			log.Printf("[Telegram] Warning: incoming message channel is full, dropping message")
		}
	}

	// Handle callback queries
	if update.CallbackQuery != nil {
		userID := strconv.FormatInt(update.CallbackQuery.From.ID, 10)
		chatID := update.CallbackQuery.From.ID

		// Check pairing status if pairing manager is enabled
		if a.pairingMgr != nil {
			isPaired, err := a.pairingMgr.HandlePairingForUser(ctx, b, userID, chatID)
			if err != nil {
				log.Printf("[Telegram] Error handling pairing for callback query user %s: %v", userID, err)
				// Answer the callback query to remove loading state
				a.getBot().AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					CallbackQueryID: update.CallbackQuery.ID,
				})
				return // Don't process the callback further
			}

			if !isPaired {
				log.Printf("[Telegram] User %s is not paired, callback query blocked", userID)
				// Answer the callback query to remove loading state
				a.getBot().AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					CallbackQueryID: update.CallbackQuery.ID,
				})
				return // User not paired, callback was handled by pairing system
			}
		}

		incomingMsg := &protocol.IncomingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeIncomingMessage,
				ID:        a.generateMessageID(),
				Timestamp: time.Now(),
			},
			ChannelID:  a.id,
			SessionKey: fmt.Sprintf("telegram_%d", update.CallbackQuery.From.ID),
			UserID:     strconv.FormatInt(update.CallbackQuery.From.ID, 10),
			Text:       update.CallbackQuery.Data,
			Metadata: map[string]string{
				"type":            "callback_query",
				"callback_id":     update.CallbackQuery.ID,
				"from_first_name": update.CallbackQuery.From.FirstName,
				"from_last_name":  update.CallbackQuery.From.LastName,
				"from_username":   update.CallbackQuery.From.Username,
			},
		}

		select {
		case a.incoming <- incomingMsg:
			a.mutex.Lock()
			a.msgCount++
			a.mutex.Unlock()
			log.Printf("[Telegram] Received callback query from %s: %s",
				update.CallbackQuery.From.FirstName, update.CallbackQuery.Data)
		default:
			log.Printf("[Telegram] Warning: incoming message channel is full, dropping callback query")
		}

		// Answer the callback query to remove loading state
		a.getBot().AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
		})
	}

	// Handle photo messages
	if update.Message != nil && len(update.Message.Photo) > 0 {
		a.handlePhotoMessage(ctx, b, update)
	}

	// Handle voice messages
	if update.Message != nil && update.Message.Voice != nil {
		a.handleVoiceMessage(ctx, b, update)
	}
}
