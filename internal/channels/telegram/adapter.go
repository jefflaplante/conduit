package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"conduit/internal/channels"
	"conduit/internal/protocol"
	"conduit/internal/redact"
	"conduit/internal/stt"
)

// botAPI abstracts the Telegram bot methods used by the adapter, enabling testing with mocks.
type botAPI interface {
	Start(ctx context.Context)
	StartWebhook(ctx context.Context)
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
	SendPhoto(ctx context.Context, params *bot.SendPhotoParams) (*models.Message, error)
	SendVoice(ctx context.Context, params *bot.SendVoiceParams) (*models.Message, error)
	SendAudio(ctx context.Context, params *bot.SendAudioParams) (*models.Message, error)
	EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error)
	DeleteMessage(ctx context.Context, params *bot.DeleteMessageParams) (bool, error)
	GetMe(ctx context.Context) (*models.User, error)
	AnswerCallbackQuery(ctx context.Context, params *bot.AnswerCallbackQueryParams) (bool, error)
	SendChatAction(ctx context.Context, params *bot.SendChatActionParams) (bool, error)
	SetMyCommands(ctx context.Context, params *bot.SetMyCommandsParams) (bool, error)
	GetFile(ctx context.Context, params *bot.GetFileParams) (*models.File, error)
	FileDownloadLink(f *models.File) string
}

// Adapter implements the ChannelAdapter interface for Telegram
type Adapter struct {
	id          string
	name        string
	bot         botAPI
	config      TelegramConfig
	status      channels.StatusCode
	statusMsg   string
	incoming    chan *protocol.IncomingMessage
	ctx         context.Context
	cancel      context.CancelFunc
	mutex       sync.RWMutex
	startTime   time.Time
	msgCount    int64
	pairingMgr  *PairingManager
	stt         stt.Transcriber
	seenUpdates map[int64]time.Time
	seenCalls   int64

	// runDone is closed when the bot run goroutine started by Start exits
	// (after bot.Start/StartWebhook return, i.e. no more handleUpdate calls
	// from the poller). Stop waits on it. conduit-31jg.26.
	runDone chan struct{}

	// fileClient overrides the HTTP client used for photo/voice downloads
	// (tests); nil uses a redacting client with fileDownloadTimeout.
	fileClient bot.HttpClient

	// extraBotOptions are appended to the bot options in Start (tests:
	// fake server URL, skip getMe).
	extraBotOptions []bot.Option
}

// stopWaitTimeout bounds how long Stop waits for the poller goroutine.
// A handler stuck in a long download/transcription must not wedge shutdown.
var stopWaitTimeout = 5 * time.Second

// TelegramConfig contains Telegram-specific configuration
type TelegramConfig struct {
	BotToken    string `json:"bot_token"`
	WebhookMode bool   `json:"webhook_mode"`
	WebhookURL  string `json:"webhook_url"`
	Debug       bool   `json:"debug"`
}

// Factory creates Telegram channel adapters
type Factory struct {
	db  *sql.DB
	stt stt.Transcriber
}

// NewFactory creates a new Telegram adapter factory
func NewFactory() *Factory {
	return &Factory{}
}

// NewFactoryWithDB creates a new Telegram adapter factory with database support
func NewFactoryWithDB(db *sql.DB, transcriber stt.Transcriber) *Factory {
	return &Factory{
		db:  db,
		stt: transcriber,
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
		// conduit-31jg.83: scrub the literal token from all log output and
		// redacted errors, whatever shape it appears in.
		redact.RegisterSecret(token)
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
		id:          config.ID,
		name:        config.Name,
		config:      telegramConfig,
		status:      channels.StatusInitializing,
		incoming:    make(chan *protocol.IncomingMessage, 100),
		stt:         f.stt,
		seenUpdates: make(map[int64]time.Time),
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

	runCtx, cancel := context.WithCancel(ctx)
	a.ctx, a.cancel = runCtx, cancel
	a.status = channels.StatusInitializing
	a.statusMsg = "Starting Telegram bot"
	a.startTime = time.Now()

	// Create bot options. conduit-31jg.83: botLoggingOptions replaces the
	// library's raw log.Printf error/debug handlers and scrubs the token out
	// of request errors; WithDefaultHandler also replaces its log.Printf
	// update dump.
	opts := append([]bot.Option{
		bot.WithDefaultHandler(a.handleUpdate),
	}, botLoggingOptions()...)

	if a.config.Debug {
		opts = append(opts, bot.WithDebug())
	}
	opts = append(opts, a.extraBotOptions...)

	// Create bot instance
	telegramBot, err := bot.New(a.config.BotToken, opts...)
	if err != nil {
		err = redact.Error(err)
		a.status = channels.StatusError
		a.statusMsg = fmt.Sprintf("Failed to create bot: %v", err)
		return fmt.Errorf("failed to create Telegram bot: %w", err)
	}

	a.bot = telegramBot

	// Register slash commands with Telegram
	a.registerCommands(ctx)

	// Drop pairing codes that expired while the adapter was down
	// (conduit-3kgo). Hygiene only: lookups already ignore expired codes,
	// so a failure is logged and never blocks the start.
	a.cleanupExpiredPairingCodesOnStart()

	// Start bot in background. The goroutine uses the locals captured here,
	// never a.bot/a.ctx: a concurrent Stop+Start (Manager.RestartAdapter)
	// rewrites those fields under a.mutex (conduit-31jg.73).
	webhookMode := a.config.WebhookMode
	runDone := make(chan struct{})
	a.runDone = runDone
	go func() {
		defer close(runDone)
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

		if webhookMode {
			// Webhook mode (for production)
			log.Printf("[Telegram] Starting webhook mode")
			telegramBot.StartWebhook(runCtx)
		} else {
			// Polling mode (for development)
			log.Printf("[Telegram] Starting polling mode...")
			telegramBot.Start(runCtx)
			log.Printf("[Telegram] Polling mode started")
		}
	}()

	return nil
}

// session returns the current bot client and run context under the adapter
// lock. Start (via Manager.RestartAdapter) rewrites both fields, so senders
// must never read a.bot / a.ctx directly (conduit-31jg.73). The context is
// context.Background() before the first Start.
func (a *Adapter) session() (botAPI, context.Context) {
	a.mutex.RLock()
	defer a.mutex.RUnlock()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.bot, ctx
}

// getBot returns the current bot client under the adapter lock.
func (a *Adapter) getBot() botAPI {
	b, _ := a.session()
	return b
}

// Stop gracefully shuts down the adapter.
//
// conduit-31jg.26: Stop no longer closes a.incoming. handleUpdate, photo.go
// and voice.go send on it from the poller goroutine; closing it while they
// were mid-flight panicked ("send on closed channel" — select's default does
// not protect against a closed channel), and a second Stop panicked on the
// double close. Following the conduit-3rhx pattern we cancel ctx, wait
// (bounded) for the poller to return, and leave the channel open; readers
// exit on their own ctx. Stop is idempotent and Start may be called again
// afterwards (Manager.RestartAdapter).
func (a *Adapter) Stop() error {
	a.mutex.Lock()
	cancel := a.cancel
	runDone := a.runDone
	a.cancel = nil
	a.runDone = nil
	a.status = channels.StatusOffline
	a.statusMsg = "Adapter stopped"
	a.mutex.Unlock()

	if cancel != nil {
		cancel()
	}

	// Wait outside the lock: the run goroutine's defer takes a.mutex.
	if runDone != nil {
		select {
		case <-runDone:
		case <-time.After(stopWaitTimeout):
			log.Printf("[Telegram] Adapter %s: poller did not exit within %v", a.Name(), stopWaitTimeout)
		}
	}

	log.Printf("[Telegram] Adapter stopped: %s", a.Name())
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

// generateMessageID creates a unique message ID
func (a *Adapter) generateMessageID() string {
	return fmt.Sprintf("telegram_%s_%s", a.id, uuid.New().String()[:8])
}
