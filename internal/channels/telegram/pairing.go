package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// Pairing constants
const (
	// Pairing code expiration time
	PairingCodeExpiration = 1 * time.Hour

	// Pairing messages
	PairingCodeMessage = `🔐 <b>Conduit Pairing Required</b>

To use this bot, please provide this pairing code to your Conduit admin:

<code>%s</code>

⏰ This code expires in 1 hour.
📞 If you don't have access to Conduit, contact your administrator.`

	PairingCodeExpiredMessage = `⏰ <b>Pairing Code Expired</b>

Your pairing code has expired. A new pairing code has been generated:

<code>%s</code>

⏰ This code expires in 1 hour.`

	UnpairedUserMessage = `🔒 <b>Access Denied</b>

You are not paired with this Conduit instance. Please wait while a new pairing code is generated...`

	ApprovalNotificationMessage = `✅ <b>Pairing Approved</b>

Welcome to Conduit! You can now send messages and interact with the assistant.

🎉 Your account has been successfully paired.`

	PairingErrorMessage = `❌ <b>Pairing Error</b>

There was an error during the pairing process. Please try again later or contact your administrator.`
)

// PairingRecord represents a pairing code record in the database
type PairingRecord struct {
	Code      string    `json:"code"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	IsActive  bool      `json:"is_active"`
}

// PairingManager handles all pairing operations
type PairingManager struct {
	db *sql.DB
}

// NewPairingManager creates a new pairing manager
func NewPairingManager(db *sql.DB) *PairingManager {
	return &PairingManager{
		db: db,
	}
}

// GeneratePairingCode generates a new UUID-based pairing code for the given user
func (p *PairingManager) GeneratePairingCode(userID string) (string, error) {
	// Deactivate any existing active codes for this user
	if err := p.deactivateUserCodes(userID); err != nil {
		log.Printf("[Pairing] Warning: Failed to deactivate existing codes for user %s: %v", userID, err)
	}

	// Generate new UUID-based code
	code := uuid.New().String()
	now := time.Now()
	expiresAt := now.Add(PairingCodeExpiration)

	// Insert new pairing record
	_, err := p.db.Exec(`
		INSERT INTO telegram_pairings (code, user_id, created_at, expires_at, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, ?)
	`, code, userID, now, expiresAt, true, "{}")

	if err != nil {
		return "", fmt.Errorf("failed to create pairing code: %w", err)
	}

	log.Printf("[Pairing] Generated new pairing code for user %s (expires: %s)", userID, expiresAt.Format(time.RFC3339))
	return code, nil
}

// ValidatePairingCode validates a pairing code and returns the associated record if valid
func (p *PairingManager) ValidatePairingCode(code string) (*PairingRecord, error) {
	var record PairingRecord

	row := p.db.QueryRow(`
		SELECT code, user_id, created_at, expires_at, is_active
		FROM telegram_pairings
		WHERE code = ? AND is_active = 1
	`, code)

	err := row.Scan(
		&record.Code,
		&record.UserID,
		&record.CreatedAt,
		&record.ExpiresAt,
		&record.IsActive,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("pairing code not found or inactive")
		}
		return nil, fmt.Errorf("failed to validate pairing code: %w", err)
	}

	// Check if code has expired
	if time.Now().After(record.ExpiresAt) {
		return nil, fmt.Errorf("pairing code has expired")
	}

	return &record, nil
}

// ApprovePairing approves a pairing by marking the code as used and deactivating it
func (p *PairingManager) ApprovePairing(code string) error {
	// First validate the code exists and is active
	_, err := p.ValidatePairingCode(code)
	if err != nil {
		return fmt.Errorf("cannot approve pairing: %w", err)
	}

	// Deactivate the pairing code (mark as used)
	result, err := p.db.Exec(`
		UPDATE telegram_pairings 
		SET is_active = 0 
		WHERE code = ? AND is_active = 1
	`, code)

	if err != nil {
		return fmt.Errorf("failed to approve pairing: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check affected rows: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("pairing code not found or already used")
	}

	log.Printf("[Pairing] Approved pairing for code %s", code[:8]+"...")
	return nil
}

// IsUserPaired checks if a user ID has any approved (used) pairing codes
func (p *PairingManager) IsUserPaired(userID string) (bool, error) {
	var count int

	err := p.db.QueryRow(`
		SELECT COUNT(*)
		FROM telegram_pairings
		WHERE user_id = ? AND is_active = 0
	`, userID).Scan(&count)

	if err != nil {
		return false, fmt.Errorf("failed to check user pairing status: %w", err)
	}

	return count > 0, nil
}

// GetActivePairingCode gets the current active pairing code for a user (if any)
func (p *PairingManager) GetActivePairingCode(userID string) (*PairingRecord, error) {
	var record PairingRecord

	row := p.db.QueryRow(`
		SELECT code, user_id, created_at, expires_at, is_active
		FROM telegram_pairings
		WHERE user_id = ? AND is_active = 1 AND expires_at > ?
		ORDER BY created_at DESC
		LIMIT 1
	`, userID, time.Now())

	err := row.Scan(
		&record.Code,
		&record.UserID,
		&record.CreatedAt,
		&record.ExpiresAt,
		&record.IsActive,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no active pairing code found")
		}
		return nil, fmt.Errorf("failed to get active pairing code: %w", err)
	}

	return &record, nil
}

// SendPairingCode sends a pairing code message to the user
func (p *PairingManager) SendPairingCode(ctx context.Context, telegramBot *bot.Bot, chatID int64, code string) error {
	message := fmt.Sprintf(PairingCodeMessage, code)

	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      message,
		ParseMode: models.ParseModeHTML,
	}

	_, err := telegramBot.SendMessage(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to send pairing code: %w", err)
	}

	log.Printf("[Pairing] Sent pairing code to chat %d", chatID)
	return nil
}

// SendApprovalNotification sends a notification that pairing was approved
func (p *PairingManager) SendApprovalNotification(ctx context.Context, telegramBot *bot.Bot, chatID int64) error {
	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      ApprovalNotificationMessage,
		ParseMode: models.ParseModeHTML,
	}

	_, err := telegramBot.SendMessage(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to send approval notification: %w", err)
	}

	log.Printf("[Pairing] Sent approval notification to chat %d", chatID)
	return nil
}

// SendUnpairedMessage sends a message indicating the user is not paired
func (p *PairingManager) SendUnpairedMessage(ctx context.Context, telegramBot *bot.Bot, chatID int64) error {
	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      UnpairedUserMessage,
		ParseMode: models.ParseModeHTML,
	}

	_, err := telegramBot.SendMessage(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to send unpaired message: %w", err)
	}

	return nil
}

// SendPairingError sends an error message for pairing issues
func (p *PairingManager) SendPairingError(ctx context.Context, telegramBot *bot.Bot, chatID int64) error {
	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      PairingErrorMessage,
		ParseMode: models.ParseModeHTML,
	}

	_, err := telegramBot.SendMessage(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to send pairing error: %w", err)
	}

	return nil
}

// CleanupExpiredCodes removes expired pairing codes from the database
func (p *PairingManager) CleanupExpiredCodes() error {
	result, err := p.db.Exec(`
		DELETE FROM telegram_pairings 
		WHERE expires_at < ? AND is_active = 1
	`, time.Now())

	if err != nil {
		return fmt.Errorf("failed to cleanup expired codes: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check affected rows: %w", err)
	}

	if rowsAffected > 0 {
		log.Printf("[Pairing] Cleaned up %d expired pairing codes", rowsAffected)
	}

	return nil
}

// HandlePairingForUser handles the complete pairing flow for a user
// Returns whether the user is paired and ready to proceed with their message
func (p *PairingManager) HandlePairingForUser(ctx context.Context, telegramBot *bot.Bot, userID string, chatID int64) (bool, error) {
	// First check if user is already paired
	isPaired, err := p.IsUserPaired(userID)
	if err != nil {
		log.Printf("[Pairing] Error checking pairing status for user %s: %v", userID, err)
		p.SendPairingError(ctx, telegramBot, chatID)
		return false, err
	}

	if isPaired {
		return true, nil // User is already paired, allow message processing
	}

	// User is not paired, check if they have an active pairing code
	activeCode, err := p.GetActivePairingCode(userID)
	if err != nil {
		// No active code, generate a new one
		code, err := p.GeneratePairingCode(userID)
		if err != nil {
			log.Printf("[Pairing] Error generating pairing code for user %s: %v", userID, err)
			p.SendPairingError(ctx, telegramBot, chatID)
			return false, err
		}

		// Send the new pairing code
		if err := p.SendPairingCode(ctx, telegramBot, chatID, code); err != nil {
			log.Printf("[Pairing] Error sending pairing code to user %s: %v", userID, err)
			p.SendPairingError(ctx, telegramBot, chatID)
			return false, err
		}

		return false, nil
	}

	// Check if existing code has expired
	if time.Now().After(activeCode.ExpiresAt) {
		// Code expired, generate a new one
		code, err := p.GeneratePairingCode(userID)
		if err != nil {
			log.Printf("[Pairing] Error generating new pairing code for user %s: %v", userID, err)
			p.SendPairingError(ctx, telegramBot, chatID)
			return false, err
		}

		// Send expired message with new code
		message := fmt.Sprintf(PairingCodeExpiredMessage, code)
		params := &bot.SendMessageParams{
			ChatID:    chatID,
			Text:      message,
			ParseMode: models.ParseModeHTML,
		}

		if _, err := telegramBot.SendMessage(ctx, params); err != nil {
			log.Printf("[Pairing] Error sending expired pairing message to user %s: %v", userID, err)
			p.SendPairingError(ctx, telegramBot, chatID)
			return false, err
		}

		return false, nil
	}

	// User has an active, non-expired code, send it again
	log.Printf("[Pairing] Resending active pairing code to user %s", userID)
	if err := p.SendPairingCode(ctx, telegramBot, chatID, activeCode.Code); err != nil {
		log.Printf("[Pairing] Error resending pairing code to user %s: %v", userID, err)
		p.SendPairingError(ctx, telegramBot, chatID)
		return false, err
	}

	return false, nil
}

// deactivateUserCodes deactivates all active pairing codes for a user
func (p *PairingManager) deactivateUserCodes(userID string) error {
	_, err := p.db.Exec(`
		UPDATE telegram_pairings 
		SET is_active = 0 
		WHERE user_id = ? AND is_active = 1
	`, userID)

	return err
}

// GetPairingStats returns statistics about pairing codes
func (p *PairingManager) GetPairingStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Count active codes
	var activeCodes int
	err := p.db.QueryRow(`
		SELECT COUNT(*) FROM telegram_pairings 
		WHERE is_active = 1 AND expires_at > ?
	`, time.Now()).Scan(&activeCodes)
	if err != nil {
		return nil, fmt.Errorf("failed to count active codes: %w", err)
	}
	stats["active_codes"] = activeCodes

	// Count expired codes
	var expiredCodes int
	err = p.db.QueryRow(`
		SELECT COUNT(*) FROM telegram_pairings 
		WHERE is_active = 1 AND expires_at <= ?
	`, time.Now()).Scan(&expiredCodes)
	if err != nil {
		return nil, fmt.Errorf("failed to count expired codes: %w", err)
	}
	stats["expired_codes"] = expiredCodes

	// Count approved pairings
	var approvedPairings int
	err = p.db.QueryRow(`
		SELECT COUNT(*) FROM telegram_pairings 
		WHERE is_active = 0
	`, time.Now()).Scan(&approvedPairings)
	if err != nil {
		return nil, fmt.Errorf("failed to count approved pairings: %w", err)
	}
	stats["approved_pairings"] = approvedPairings

	// Count unique paired users
	var pairedUsers int
	err = p.db.QueryRow(`
		SELECT COUNT(DISTINCT user_id) FROM telegram_pairings 
		WHERE is_active = 0
	`).Scan(&pairedUsers)
	if err != nil {
		return nil, fmt.Errorf("failed to count paired users: %w", err)
	}
	stats["paired_users"] = pairedUsers

	return stats, nil
}
