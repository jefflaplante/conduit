package telegram

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

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

	if b, ctx := a.session(); b != nil {
		if concreteBot, ok := b.(*bot.Bot); ok {
			if err := a.pairingMgr.SendApprovalNotification(ctx, concreteBot, chatID); err != nil {
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

// cleanupExpiredPairingCodesOnStart runs CleanupExpiredPairingCodes when
// pairing is enabled, logging (never returning) a failure.
func (a *Adapter) cleanupExpiredPairingCodesOnStart() {
	if a.pairingMgr == nil {
		return
	}
	if err := a.CleanupExpiredPairingCodes(); err != nil {
		log.Printf("[Telegram] Adapter %s: expired pairing code cleanup failed: %v", a.Name(), err)
	}
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
		{Command: "provider", Description: "Switch or view current AI provider"},
		{Command: "context", Description: "Show context window usage"},
		{Command: "stop", Description: "Stop the current operation"},
		{Command: "ring", Description: "Show debug ring buffer activity"},
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
