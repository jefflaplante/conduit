package telegram

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// parseTimestamp flexibly parses timestamps from SQLite (tries RFC3339 first, then simple format)
func parseTimestamp(timeStr string) (time.Time, error) {
	// Try RFC3339 format first (used by time.Now() when stored by Go)
	if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
		return t, nil
	}

	// Fallback to simple format
	if t, err := time.Parse("2006-01-02 15:04:05", timeStr); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unable to parse timestamp: %s", timeStr)
}

// PairingStorage manages Telegram pairing codes in the database
type PairingStorage struct {
	db *sql.DB
}

// PairingInfo represents a Telegram pairing entry
type PairingInfo struct {
	Code      string            `json:"code"`
	UserID    string            `json:"user_id"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	IsActive  bool              `json:"is_active"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// NewPairingStorage creates a new pairing storage instance
func NewPairingStorage(db *sql.DB) *PairingStorage {
	return &PairingStorage{db: db}
}

// ListPendingPairings returns all active (non-expired) pairing codes
func (ps *PairingStorage) ListPendingPairings() ([]PairingInfo, error) {
	query := `
		SELECT code, user_id, created_at, expires_at, is_active, metadata
		FROM telegram_pairings
		WHERE is_active = 1 AND expires_at > datetime('now')
		ORDER BY created_at DESC
	`

	rows, err := ps.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending pairings: %w", err)
	}
	defer rows.Close()

	var pairings []PairingInfo
	for rows.Next() {
		var p PairingInfo
		var metadataJSON string
		var createdAtStr, expiresAtStr string

		err := rows.Scan(&p.Code, &p.UserID, &createdAtStr, &expiresAtStr, &p.IsActive, &metadataJSON)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pairing row: %w", err)
		}

		// Parse timestamps
		if p.CreatedAt, err = parseTimestamp(createdAtStr); err != nil {
			return nil, fmt.Errorf("failed to parse created_at: %w", err)
		}
		if p.ExpiresAt, err = parseTimestamp(expiresAtStr); err != nil {
			return nil, fmt.Errorf("failed to parse expires_at: %w", err)
		}

		// Parse metadata
		if metadataJSON != "" {
			if err := json.Unmarshal([]byte(metadataJSON), &p.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		} else {
			p.Metadata = make(map[string]string)
		}

		pairings = append(pairings, p)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pairing rows: %w", err)
	}

	return pairings, nil
}

// GetPairingByCode retrieves a pairing by its code
func (ps *PairingStorage) GetPairingByCode(code string) (*PairingInfo, error) {
	query := `
		SELECT code, user_id, created_at, expires_at, is_active, metadata
		FROM telegram_pairings
		WHERE code = ?
	`

	var p PairingInfo
	var metadataJSON string
	var createdAtStr, expiresAtStr string

	err := ps.db.QueryRow(query, code).Scan(
		&p.Code, &p.UserID, &createdAtStr, &expiresAtStr, &p.IsActive, &metadataJSON,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("pairing code not found: %s", code)
		}
		return nil, fmt.Errorf("failed to query pairing: %w", err)
	}

	// Parse timestamps
	if p.CreatedAt, err = parseTimestamp(createdAtStr); err != nil {
		return nil, fmt.Errorf("failed to parse created_at: %w", err)
	}
	if p.ExpiresAt, err = parseTimestamp(expiresAtStr); err != nil {
		return nil, fmt.Errorf("failed to parse expires_at: %w", err)
	}

	// Parse metadata
	if metadataJSON != "" {
		if err := json.Unmarshal([]byte(metadataJSON), &p.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	} else {
		p.Metadata = make(map[string]string)
	}

	return &p, nil
}

// ApprovePairing marks a pairing as inactive (consumed/approved)
func (ps *PairingStorage) ApprovePairing(code string) error {
	// First check if pairing exists and is active
	pairing, err := ps.GetPairingByCode(code)
	if err != nil {
		return err
	}

	if !pairing.IsActive {
		return fmt.Errorf("pairing code is already used or inactive: %s", code)
	}

	if time.Now().After(pairing.ExpiresAt) {
		return fmt.Errorf("pairing code has expired: %s", code)
	}

	// Mark as inactive
	query := `
		UPDATE telegram_pairings
		SET is_active = 0
		WHERE code = ?
	`

	result, err := ps.db.Exec(query, code)
	if err != nil {
		return fmt.Errorf("failed to approve pairing: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no pairing found to approve: %s", code)
	}

	return nil
}

// CreatePairing creates a new pairing entry (utility function for future use)
func (ps *PairingStorage) CreatePairing(userID string, expirationDuration time.Duration) (*PairingInfo, error) {
	code := uuid.New().String()
	now := time.Now()
	expiresAt := now.Add(expirationDuration)

	metadata := make(map[string]string)
	metadataJSON, _ := json.Marshal(metadata)

	query := `
		INSERT INTO telegram_pairings (code, user_id, created_at, expires_at, is_active, metadata)
		VALUES (?, ?, ?, ?, 1, ?)
	`

	_, err := ps.db.Exec(query, code, userID, now.Format("2006-01-02 15:04:05"),
		expiresAt.Format("2006-01-02 15:04:05"), string(metadataJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create pairing: %w", err)
	}

	return &PairingInfo{
		Code:      code,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: expiresAt,
		IsActive:  true,
		Metadata:  metadata,
	}, nil
}

// CleanupExpiredPairings removes expired pairing codes (utility function for future use)
func (ps *PairingStorage) CleanupExpiredPairings() (int64, error) {
	query := `
		DELETE FROM telegram_pairings
		WHERE expires_at <= datetime('now')
	`

	result, err := ps.db.Exec(query)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup expired pairings: %w", err)
	}

	rowsDeleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return rowsDeleted, nil
}

// findPairingByCodePrefix finds a pairing by matching a code prefix (for partial code matching)
func (ps *PairingStorage) findPairingByCodePrefix(prefix string) ([]PairingInfo, error) {
	// Only search active, non-expired pairings
	query := `
		SELECT code, user_id, created_at, expires_at, is_active, metadata
		FROM telegram_pairings
		WHERE code LIKE ? AND is_active = 1 AND expires_at > datetime('now')
		ORDER BY created_at DESC
	`

	rows, err := ps.db.Query(query, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to query pairings by prefix: %w", err)
	}
	defer rows.Close()

	var pairings []PairingInfo
	for rows.Next() {
		var p PairingInfo
		var metadataJSON string
		var createdAtStr, expiresAtStr string

		err := rows.Scan(&p.Code, &p.UserID, &createdAtStr, &expiresAtStr, &p.IsActive, &metadataJSON)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pairing row: %w", err)
		}

		// Parse timestamps
		if p.CreatedAt, err = parseTimestamp(createdAtStr); err != nil {
			return nil, fmt.Errorf("failed to parse created_at: %w", err)
		}
		if p.ExpiresAt, err = parseTimestamp(expiresAtStr); err != nil {
			return nil, fmt.Errorf("failed to parse expires_at: %w", err)
		}

		// Parse metadata
		if metadataJSON != "" {
			if err := json.Unmarshal([]byte(metadataJSON), &p.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		} else {
			p.Metadata = make(map[string]string)
		}

		pairings = append(pairings, p)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pairing rows: %w", err)
	}

	return pairings, nil
}
