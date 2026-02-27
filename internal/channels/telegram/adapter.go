package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"conduit/internal/channels"
	"conduit/pkg/protocol"
)

// Patterns for sanitizing user-facing text
var (
	// Strip complete XML blocks: <claude_function_calls>...</claude_function_calls>
	claudeFunctionCallsRe  = regexp.MustCompile(`(?s)<\s*claude_function_calls\s*>.*?</\s*claude_function_calls\s*>`)
	claudeFunctionResultRe = regexp.MustCompile(`(?s)<\s*claude_function_result\s*>.*?</\s*claude_function_result\s*>`)
	antmlFunctionCallsRe   = regexp.MustCompile(`(?s)<\s*antml:function_calls\s*>.*?</\s*antml:function_calls\s*>`)
	antmlInvokeRe          = regexp.MustCompile(`(?s)<\s*antml:invoke[^>]*>.*?</\s*antml:invoke\s*>`)

	// Strip standalone XML-like tags that shouldn't be visible to users
	// Matches: <bash>, </bash>, <thinking>, <invoke name="...">, <parameter name="...">, etc.
	xmlTagRe = regexp.MustCompile(`<\s*/?(?:bash|thinking|final|tool_call|invoke|parameter|claude_function_calls|claude_function_result|antml:function_calls|antml:invoke|antml:parameter)[^>]*>`)

	// Strip [Tool Call: ...] and [Tool Result: ...] markers
	toolMarkerRe = regexp.MustCompile(`\[Tool (?:Call|Result)[^\]]*\]`)

	// Collapse multiple newlines into double newlines
	multiNewlineRe = regexp.MustCompile(`\n{3,}`)
)

// botAPI abstracts the Telegram bot methods used by the adapter, enabling testing with mocks.
type botAPI interface {
	Start(ctx context.Context)
	StartWebhook(ctx context.Context)
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
	SendPhoto(ctx context.Context, params *bot.SendPhotoParams) (*models.Message, error)
	EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error)
	DeleteMessage(ctx context.Context, params *bot.DeleteMessageParams) (bool, error)
	GetMe(ctx context.Context) (*models.User, error)
	AnswerCallbackQuery(ctx context.Context, params *bot.AnswerCallbackQueryParams) (bool, error)
	SendChatAction(ctx context.Context, params *bot.SendChatActionParams) (bool, error)
	SetMyCommands(ctx context.Context, params *bot.SetMyCommandsParams) (bool, error)
}

// Adapter implements the ChannelAdapter interface for Telegram
type Adapter struct {
	id         string
	name       string
	bot        botAPI
	config     TelegramConfig
	status     channels.StatusCode
	statusMsg  string
	incoming   chan *protocol.IncomingMessage
	ctx        context.Context
	cancel     context.CancelFunc
	mutex      sync.RWMutex
	startTime  time.Time
	msgCount   int64
	pairingMgr *PairingManager
}

// TelegramConfig contains Telegram-specific configuration
type TelegramConfig struct {
	BotToken    string `json:"bot_token"`
	WebhookMode bool   `json:"webhook_mode"`
	WebhookURL  string `json:"webhook_url"`
	Debug       bool   `json:"debug"`
}

// Factory creates Telegram channel adapters
type Factory struct {
	db *sql.DB
}

// NewFactory creates a new Telegram adapter factory
func NewFactory() *Factory {
	return &Factory{}
}

// NewFactoryWithDB creates a new Telegram adapter factory with database support
func NewFactoryWithDB(db *sql.DB) *Factory {
	return &Factory{
		db: db,
	}
}

// SupportsType returns whether this factory supports the given adapter type
func (f *Factory) SupportsType(adapterType string) bool {
	return adapterType == "telegram"
}

// CreateAdapter creates a new Telegram adapter instance
func (f *Factory) CreateAdapter(config channels.ChannelConfig) (channels.ChannelAdapter, error) {
	telegramConfig := TelegramConfig{}

	// Parse Telegram-specific config
	if token, ok := config.Config["bot_token"].(string); ok {
		telegramConfig.BotToken = token
	} else {
		return nil, fmt.Errorf("bot_token is required for Telegram adapter")
	}

	if webhook, ok := config.Config["webhook_mode"].(bool); ok {
		telegramConfig.WebhookMode = webhook
	}

	if webhookURL, ok := config.Config["webhook_url"].(string); ok {
		telegramConfig.WebhookURL = webhookURL
	}

	if debug, ok := config.Config["debug"].(bool); ok {
		telegramConfig.Debug = debug
	}

	adapter := &Adapter{
		id:       config.ID,
		name:     config.Name,
		config:   telegramConfig,
		status:   channels.StatusInitializing,
		incoming: make(chan *protocol.IncomingMessage, 100),
	}

	// Initialize pairing manager if database is available
	if f.db != nil {
		adapter.pairingMgr = NewPairingManager(f.db)
	} else {
		log.Printf("[Telegram] Warning: No database connection, pairing will be disabled for adapter %s", config.ID)
	}

	return adapter, nil
}

// GetSupportedTypes returns the adapter types this factory supports
func (f *Factory) GetSupportedTypes() []string {
	return []string{"telegram"}
}

// ID returns the adapter's unique identifier
func (a *Adapter) ID() string {
	return a.id
}

// Name returns the adapter's human-readable name
func (a *Adapter) Name() string {
	return a.name
}

// Type returns the adapter type
func (a *Adapter) Type() string {
	return "telegram"
}

// Start initializes and starts the Telegram bot
func (a *Adapter) Start(ctx context.Context) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	a.ctx, a.cancel = context.WithCancel(ctx)
	a.status = channels.StatusInitializing
	a.statusMsg = "Starting Telegram bot"
	a.startTime = time.Now()

	// Create bot options
	opts := []bot.Option{
		bot.WithDefaultHandler(a.handleUpdate),
	}

	if a.config.Debug {
		opts = append(opts, bot.WithDebug())
	}

	// Create bot instance
	telegramBot, err := bot.New(a.config.BotToken, opts...)
	if err != nil {
		a.status = channels.StatusError
		a.statusMsg = fmt.Sprintf("Failed to create bot: %v", err)
		return fmt.Errorf("failed to create Telegram bot: %w", err)
	}

	a.bot = telegramBot

	// Register slash commands with Telegram
	a.registerCommands(ctx)

	// Start bot in background
	go func() {
		defer func() {
			a.mutex.Lock()
			a.status = channels.StatusOffline
			a.statusMsg = "Bot stopped"
			a.mutex.Unlock()
		}()

		a.mutex.Lock()
		a.status = channels.StatusOnline
		a.statusMsg = "Bot is running"
		a.mutex.Unlock()

		log.Printf("[Telegram] Bot started: %s", a.Name())

		if a.config.WebhookMode {
			// Webhook mode (for production)
			log.Printf("[Telegram] Starting webhook mode")
			a.bot.StartWebhook(a.ctx)
		} else {
			// Polling mode (for development)
			log.Printf("[Telegram] Starting polling mode...")
			a.bot.Start(a.ctx)
			log.Printf("[Telegram] Polling mode started")
		}
	}()

	return nil
}

// Stop gracefully shuts down the adapter
func (a *Adapter) Stop() error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.cancel != nil {
		a.cancel()
	}

	a.status = channels.StatusOffline
	a.statusMsg = "Adapter stopped"

	// Close incoming message channel
	close(a.incoming)

	log.Printf("[Telegram] Adapter stopped: %s", a.Name())
	return nil
}

// sanitizeUserFacingText cleans up AI output before sending to users
// Strips internal markers, XML-like tags, and normalizes whitespace
func sanitizeUserFacingText(text string) string {
	if text == "" {
		return text
	}

	// First: Strip complete XML blocks with their content
	cleaned := claudeFunctionCallsRe.ReplaceAllString(text, "")
	cleaned = claudeFunctionResultRe.ReplaceAllString(cleaned, "")
	cleaned = antmlFunctionCallsRe.ReplaceAllString(cleaned, "")
	cleaned = antmlInvokeRe.ReplaceAllString(cleaned, "")

	// Second: Strip any remaining standalone XML-like tags
	cleaned = xmlTagRe.ReplaceAllString(cleaned, "")

	// Strip tool call markers like [Tool Call: ...]
	cleaned = toolMarkerRe.ReplaceAllString(cleaned, "")

	// Collapse excessive newlines
	cleaned = multiNewlineRe.ReplaceAllString(cleaned, "\n\n")

	// Trim leading/trailing whitespace
	cleaned = strings.TrimSpace(cleaned)

	return cleaned
}

// Regex patterns for Telegram markdown conversion
var (
	// Headers: # Header -> *Header* (bold with single asterisk)
	headerRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	// Standard **bold** -> *bold* (Telegram uses single asterisk)
	doubleBoldRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// Links [text](url) -> text (url) - Telegram markdown doesn't support links
	linkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	// Markdown tables: lines starting with |
	tableRe = regexp.MustCompile(`(?m)((?:^\|.+\|$\n?)+)`)
)

// convertToTelegramMarkdown converts standard markdown to Telegram's limited subset
// Telegram: *bold/italic* (single asterisk), `code`, ```code blocks```
func convertToTelegramMarkdown(text string) string {
	result := text

	// Wrap markdown tables in code blocks for monospace alignment
	result = tableRe.ReplaceAllStringFunc(result, func(table string) string {
		// Don't double-wrap if already in a code block
		if strings.Contains(table, "```") {
			return table
		}
		return "```\n" + strings.TrimSpace(table) + "\n```"
	})

	// Convert **bold** to *bold* (single asterisk for Telegram)
	result = doubleBoldRe.ReplaceAllString(result, "*$1*")

	// Convert headers to bold text (strip the # symbols)
	result = headerRe.ReplaceAllString(result, "*$1*")

	// Convert links to "text (url)" format since Telegram markdown doesn't do links
	result = linkRe.ReplaceAllString(result, "$1 ($2)")

	// Clean up excessive whitespace
	result = regexp.MustCompile(`\n{3,}`).ReplaceAllString(result, "\n\n")
	result = strings.TrimSpace(result)

	return result
}

// SendMessage sends an outgoing message through Telegram
func (a *Adapter) SendMessage(msg *protocol.OutgoingMessage) error {
	if a.bot == nil {
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

		_, err = a.bot.SendPhoto(a.ctx, photoParams)
		if err != nil {
			return fmt.Errorf("failed to send photo: %w", err)
		}

		log.Printf("[Telegram] Photo sent to chat %d (%s)", chatID, filepath.Base(imagePath))

		a.mutex.Lock()
		a.msgCount++
		a.mutex.Unlock()

		return nil
	}

	// Sanitize text before sending
	sanitizedText := sanitizeUserFacingText(msg.Text)

	// Convert standard markdown to Telegram's limited markdown subset
	telegramText := convertToTelegramMarkdown(sanitizedText)

	// Send message with legacy Markdown parse mode (more lenient than MarkdownV2)
	// Telegram Markdown supports: *bold*, _italic_, `code`, ```code blocks```
	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      telegramText,
		ParseMode: models.ParseModeMarkdownV1,
	}

	// Handle reply-to if specified in metadata
	if replyToStr, ok := msg.Metadata["reply_to_message_id"]; ok {
		if replyToInt, err := strconv.Atoi(replyToStr); err == nil {
			params.ReplyParameters = &models.ReplyParameters{
				MessageID: replyToInt,
			}
		}
	}

	// Handle parse mode override from metadata
	if parseMode, ok := msg.Metadata["parse_mode"]; ok {
		params.ParseMode = models.ParseMode(parseMode)
	}

	_, err = a.bot.SendMessage(a.ctx, params)
	if err != nil {
		// If HTML parsing fails, retry without formatting
		if strings.Contains(err.Error(), "can't parse entities") {
			log.Printf("[Telegram] HTML parsing failed, retrying as plain text: %v", err)
			params.ParseMode = ""
			params.Text = sanitizedText // Use original sanitized text without HTML conversion
			_, err = a.bot.SendMessage(a.ctx, params)
			if err != nil {
				return fmt.Errorf("failed to send message (plain text fallback): %w", err)
			}
			log.Printf("[Telegram] Message sent as plain text fallback (%d chars)", len(sanitizedText))
		} else {
			return fmt.Errorf("failed to send message: %w", err)
		}
	} else {
		log.Printf("[Telegram] Message sent to chat (%d chars, sanitized from %d)", len(sanitizedText), len(msg.Text))
	}

	a.mutex.Lock()
	a.msgCount++
	a.mutex.Unlock()

	return nil
}

// SendMessageWithID sends a message and returns the message ID (for later editing)
func (a *Adapter) SendMessageWithID(chatID int64, text string) (int, error) {
	if a.bot == nil {
		return 0, fmt.Errorf("bot not initialized")
	}

	sanitizedText := sanitizeUserFacingText(text)
	telegramText := convertToTelegramMarkdown(sanitizedText)

	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      telegramText,
		ParseMode: models.ParseModeMarkdownV1,
	}

	msg, err := a.bot.SendMessage(a.ctx, params)
	if err != nil {
		// Fallback to plain text
		params.ParseMode = ""
		params.Text = sanitizedText
		msg, err = a.bot.SendMessage(a.ctx, params)
		if err != nil {
			return 0, err
		}
	}

	return msg.ID, nil
}

// EditMessageText edits an existing message
func (a *Adapter) EditMessageText(chatID int64, messageID int, text string) error {
	if a.bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	sanitizedText := sanitizeUserFacingText(text)
	telegramText := convertToTelegramMarkdown(sanitizedText)

	params := &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      telegramText,
		ParseMode: models.ParseModeMarkdownV1,
	}

	_, err := a.bot.EditMessageText(a.ctx, params)
	if err != nil {
		// Fallback to plain text if markdown fails
		if strings.Contains(err.Error(), "can't parse entities") {
			params.ParseMode = ""
			params.Text = sanitizedText
			_, err = a.bot.EditMessageText(a.ctx, params)
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
	if a.bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	params := &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: messageID,
	}

	_, err := a.bot.DeleteMessage(a.ctx, params)
	if err != nil {
		// Ignore "message not found" errors
		if strings.Contains(err.Error(), "message to delete not found") {
			return nil
		}
		return err
	}

	return nil
}

// ReceiveMessages returns the channel for incoming messages
func (a *Adapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return a.incoming
}

// Status returns the current adapter status
func (a *Adapter) Status() channels.ChannelStatus {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	details := map[string]interface{}{
		"uptime_seconds": time.Since(a.startTime).Seconds(),
		"message_count":  a.msgCount,
	}

	if a.bot != nil {
		// Get bot info via GetMe method
		if me, err := a.bot.GetMe(context.Background()); err == nil {
			details["bot_id"] = me.ID
			details["bot_username"] = me.Username
		}
	}

	return channels.ChannelStatus{
		Status:    a.status,
		Message:   a.statusMsg,
		Details:   details,
		Timestamp: time.Now(),
	}
}

// IsHealthy returns whether the adapter is functioning properly
func (a *Adapter) IsHealthy() bool {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	return a.status == channels.StatusOnline && a.bot != nil
}

// handleUpdate processes incoming Telegram updates
func (a *Adapter) handleUpdate(ctx context.Context, b *bot.Bot, update *models.Update) {
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
				a.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					CallbackQueryID: update.CallbackQuery.ID,
				})
				return // Don't process the callback further
			}

			if !isPaired {
				log.Printf("[Telegram] User %s is not paired, callback query blocked", userID)
				// Answer the callback query to remove loading state
				a.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
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
		a.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
		})
	}

	// Handle photo messages
	if update.Message != nil && len(update.Message.Photo) > 0 {
		userID := strconv.FormatInt(update.Message.Chat.ID, 10)
		chatID := update.Message.Chat.ID

		// Check pairing status if pairing manager is enabled
		if a.pairingMgr != nil {
			isPaired, err := a.pairingMgr.HandlePairingForUser(ctx, b, userID, chatID)
			if err != nil {
				log.Printf("[Telegram] Error handling pairing for photo user %s: %v", userID, err)
				return // Don't process the photo further
			}

			if !isPaired {
				log.Printf("[Telegram] User %s is not paired, photo blocked", userID)
				return // User not paired, photo was handled by pairing system
			}
		}

		caption := update.Message.Caption
		if caption == "" {
			caption = "[Photo]"
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
			Text:       caption,
			Metadata: map[string]string{
				"type":            "photo",
				"message_id":      strconv.Itoa(update.Message.ID),
				"chat_type":       string(update.Message.Chat.Type),
				"from_first_name": update.Message.From.FirstName,
				"from_last_name":  update.Message.From.LastName,
				"from_username":   update.Message.From.Username,
				"photo_count":     strconv.Itoa(len(update.Message.Photo)),
			},
		}

		select {
		case a.incoming <- incomingMsg:
			a.mutex.Lock()
			a.msgCount++
			a.mutex.Unlock()
			log.Printf("[Telegram] Received photo from %s", update.Message.From.FirstName)
		default:
			log.Printf("[Telegram] Warning: incoming message channel is full, dropping photo")
		}
	}
}

// generateMessageID creates a unique message ID
func (a *Adapter) generateMessageID() string {
	return fmt.Sprintf("telegram_%s_%s", a.id, uuid.New().String()[:8])
}

// SendTypingIndicator sends a "typing" chat action to show the bot is thinking
func (a *Adapter) SendTypingIndicator(chatID string) error {
	if a.bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %s", chatID)
	}

	_, err = a.bot.SendChatAction(a.ctx, &bot.SendChatActionParams{
		ChatID: chatIDInt,
		Action: models.ChatActionTyping,
	})
	return err
}

// GetPairingManager returns the pairing manager for this adapter (if enabled)
func (a *Adapter) GetPairingManager() *PairingManager {
	return a.pairingMgr
}

// ApprovePairingCode approves a pairing code and sends notification to user
func (a *Adapter) ApprovePairingCode(code string) error {
	if a.pairingMgr == nil {
		return fmt.Errorf("pairing manager not initialized")
	}

	// Validate and get the pairing record before approval
	record, err := a.pairingMgr.ValidatePairingCode(code)
	if err != nil {
		return fmt.Errorf("invalid pairing code: %w", err)
	}

	// Approve the pairing
	if err := a.pairingMgr.ApprovePairing(code); err != nil {
		return fmt.Errorf("failed to approve pairing: %w", err)
	}

	// Send approval notification to user
	chatID, err := strconv.ParseInt(record.UserID, 10, 64)
	if err != nil {
		log.Printf("[Telegram] Warning: Failed to parse chat ID %s for approval notification: %v", record.UserID, err)
		// Pairing was approved successfully, just can't send notification
		return nil
	}

	if a.bot != nil {
		if concreteBot, ok := a.bot.(*bot.Bot); ok {
			if err := a.pairingMgr.SendApprovalNotification(a.ctx, concreteBot, chatID); err != nil {
				log.Printf("[Telegram] Warning: Failed to send approval notification: %v", err)
				// Pairing was approved successfully, just can't send notification
			}
		}
	}

	log.Printf("[Telegram] Successfully approved pairing for user %s", record.UserID)
	return nil
}

// GetPairingStats returns statistics about the pairing system
func (a *Adapter) GetPairingStats() (map[string]interface{}, error) {
	if a.pairingMgr == nil {
		return nil, fmt.Errorf("pairing manager not initialized")
	}

	return a.pairingMgr.GetPairingStats()
}

// CleanupExpiredPairingCodes removes expired pairing codes
func (a *Adapter) CleanupExpiredPairingCodes() error {
	if a.pairingMgr == nil {
		return fmt.Errorf("pairing manager not initialized")
	}

	return a.pairingMgr.CleanupExpiredCodes()
}

// IsPairingEnabled returns whether pairing is enabled for this adapter
func (a *Adapter) IsPairingEnabled() bool {
	return a.pairingMgr != nil
}

// registerCommands registers slash commands with Telegram's BotFather
func (a *Adapter) registerCommands(ctx context.Context) {
	commands := []models.BotCommand{
		{Command: "reset", Description: "Clear conversation history and start fresh"},
		{Command: "status", Description: "Show session status and model info"},
		{Command: "help", Description: "Show available commands"},
		{Command: "model", Description: "Switch or view current model"},
		{Command: "context", Description: "Show context window usage"},
		{Command: "stop", Description: "Stop the current operation"},
	}

	_, err := a.bot.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: commands,
	})
	if err != nil {
		log.Printf("[Telegram] Warning: Failed to register commands: %v", err)
	} else {
		log.Printf("[Telegram] Registered %d slash commands", len(commands))
	}
}
