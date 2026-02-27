package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// TokenStorage manages authentication tokens in the database
type TokenStorage struct {
	db *sql.DB
}

// AuthToken represents an authentication token
type AuthToken struct {
	TokenID     string            `json:"token_id"`
	ClientName  string            `json:"client_name"`
	HashedToken string            `json:"-"` // Never expose in JSON
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   *time.Time        `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time        `json:"last_used_at,omitempty"`
	IsActive    bool              `json:"is_active"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// TokenInfo represents public token information (no sensitive data)
type TokenInfo struct {
	TokenID    string            `json:"token_id"`
	ClientName string            `json:"client_name"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  *time.Time        `json:"expires_at,omitempty"`
	LastUsedAt *time.Time        `json:"last_used_at,omitempty"`
	IsActive   bool              `json:"is_active"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// CreateTokenRequest contains parameters for creating a new token
type CreateTokenRequest struct {
	ClientName string            `json:"client_name"`
	ExpiresAt  *time.Time        `json:"expires_at,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// CreateTokenResponse contains the newly created token (including the raw token)
type CreateTokenResponse struct {
	Token     string    `json:"token"`      // Raw token (only returned once)
	TokenInfo TokenInfo `json:"token_info"` // Public token information
}

// NewTokenStorage creates a new token storage instance
func NewTokenStorage(db *sql.DB) *TokenStorage {
	return &TokenStorage{db: db}
}

// CreateToken generates and stores a new authentication token
func (ts *TokenStorage) CreateToken(req CreateTokenRequest) (*CreateTokenResponse, error) {
	// Validate input
	if strings.TrimSpace(req.ClientName) == "" {
		return nil, fmt.Errorf("client_name is required")
	}

	// Generate token
	tokenBytes := make([]byte, 32) // 256 bits
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random token: %w", err)
	}

	// Create token string with prefix for easy identification
	rawToken := "conduit_" + hex.EncodeToString(tokenBytes)

	// Hash token for storage
	hasher := sha256.New()
	hasher.Write([]byte(rawToken))
	hashedToken := hex.EncodeToString(hasher.Sum(nil))

	// Generate token ID
	tokenID := uuid.New().String()

	// Prepare metadata
	metadata := req.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Insert into database
	_, err = ts.db.Exec(`
		INSERT INTO auth_tokens 
		(token_id, client_name, hashed_token, created_at, expires_at, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		tokenID,
		strings.TrimSpace(req.ClientName),
		hashedToken,
		time.Now(),
		req.ExpiresAt,
		true,
		string(metadataJSON),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to store token: %w", err)
	}

	// Retrieve the created token for response
	tokenInfo, err := ts.GetTokenInfo(tokenID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve created token: %w", err)
	}

	return &CreateTokenResponse{
		Token:     rawToken,
		TokenInfo: *tokenInfo,
	}, nil
}

// CreateTokenWithCustomFormat creates a token with a custom provided token string
func (ts *TokenStorage) CreateTokenWithCustomFormat(req CreateTokenRequest, rawToken string) (*CreateTokenResponse, error) {
	// Validate input
	if strings.TrimSpace(req.ClientName) == "" {
		return nil, fmt.Errorf("client_name is required")
	}

	if rawToken == "" {
		return nil, fmt.Errorf("token is required")
	}

	// Hash token for storage
	hasher := sha256.New()
	hasher.Write([]byte(rawToken))
	hashedToken := hex.EncodeToString(hasher.Sum(nil))

	// Generate token ID
	tokenID := uuid.New().String()

	// Prepare metadata
	metadata := req.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Insert into database
	_, err = ts.db.Exec(`
		INSERT INTO auth_tokens 
		(token_id, client_name, hashed_token, created_at, expires_at, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		tokenID,
		strings.TrimSpace(req.ClientName),
		hashedToken,
		time.Now(),
		req.ExpiresAt,
		true,
		string(metadataJSON),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to store token: %w", err)
	}

	// Retrieve the created token for response
	tokenInfo, err := ts.GetTokenInfo(tokenID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve created token: %w", err)
	}

	return &CreateTokenResponse{
		Token:     rawToken,
		TokenInfo: *tokenInfo,
	}, nil
}

// ValidateToken checks if a token is valid and updates last_used_at
func (ts *TokenStorage) ValidateToken(rawToken string) (*TokenInfo, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("token is required")
	}

	// Hash the provided token
	hasher := sha256.New()
	hasher.Write([]byte(rawToken))
	hashedToken := hex.EncodeToString(hasher.Sum(nil))

	// Query for the token
	var token AuthToken
	var metadataJSON string

	row := ts.db.QueryRow(`
		SELECT token_id, client_name, hashed_token, created_at, expires_at, last_used_at, is_active, metadata
		FROM auth_tokens 
		WHERE hashed_token = ? AND is_active = 1
	`, hashedToken)

	err := row.Scan(
		&token.TokenID,
		&token.ClientName,
		&token.HashedToken,
		&token.CreatedAt,
		&token.ExpiresAt,
		&token.LastUsedAt,
		&token.IsActive,
		&metadataJSON,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invalid token")
		}
		return nil, fmt.Errorf("failed to validate token: %w", err)
	}

	// Check if token is expired
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, fmt.Errorf("token has expired")
	}

	// Update last_used_at
	if err := ts.updateLastUsed(token.TokenID); err != nil {
		// Log error but don't fail the validation
		// In production, you might want to use a proper logger here
	}

	// Parse metadata
	if err := json.Unmarshal([]byte(metadataJSON), &token.Metadata); err != nil {
		token.Metadata = make(map[string]string)
	}

	// Return token info (no sensitive data)
	now := time.Now()
	return &TokenInfo{
		TokenID:    token.TokenID,
		ClientName: token.ClientName,
		CreatedAt:  token.CreatedAt,
		ExpiresAt:  token.ExpiresAt,
		LastUsedAt: &now,
		IsActive:   token.IsActive,
		Metadata:   token.Metadata,
	}, nil
}

// GetTokenInfo retrieves public information about a token by ID
func (ts *TokenStorage) GetTokenInfo(tokenID string) (*TokenInfo, error) {
	var token AuthToken
	var metadataJSON string

	row := ts.db.QueryRow(`
		SELECT token_id, client_name, created_at, expires_at, last_used_at, is_active, metadata
		FROM auth_tokens 
		WHERE token_id = ?
	`, tokenID)

	err := row.Scan(
		&token.TokenID,
		&token.ClientName,
		&token.CreatedAt,
		&token.ExpiresAt,
		&token.LastUsedAt,
		&token.IsActive,
		&metadataJSON,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("token not found: %s", tokenID)
		}
		return nil, fmt.Errorf("failed to get token info: %w", err)
	}

	// Parse metadata
	if err := json.Unmarshal([]byte(metadataJSON), &token.Metadata); err != nil {
		token.Metadata = make(map[string]string)
	}

	return &TokenInfo{
		TokenID:    token.TokenID,
		ClientName: token.ClientName,
		CreatedAt:  token.CreatedAt,
		ExpiresAt:  token.ExpiresAt,
		LastUsedAt: token.LastUsedAt,
		IsActive:   token.IsActive,
		Metadata:   token.Metadata,
	}, nil
}

// ListTokens returns all tokens for a client (public info only)
func (ts *TokenStorage) ListTokens(clientName string, includeInactive bool) ([]TokenInfo, error) {
	var query string
	var args []interface{}

	if clientName != "" {
		if includeInactive {
			query = `
				SELECT token_id, client_name, created_at, expires_at, last_used_at, is_active, metadata
				FROM auth_tokens 
				WHERE client_name = ?
				ORDER BY created_at DESC
			`
			args = []interface{}{clientName}
		} else {
			query = `
				SELECT token_id, client_name, created_at, expires_at, last_used_at, is_active, metadata
				FROM auth_tokens 
				WHERE client_name = ? AND is_active = 1
				ORDER BY created_at DESC
			`
			args = []interface{}{clientName}
		}
	} else {
		if includeInactive {
			query = `
				SELECT token_id, client_name, created_at, expires_at, last_used_at, is_active, metadata
				FROM auth_tokens 
				ORDER BY created_at DESC
			`
		} else {
			query = `
				SELECT token_id, client_name, created_at, expires_at, last_used_at, is_active, metadata
				FROM auth_tokens 
				WHERE is_active = 1
				ORDER BY created_at DESC
			`
		}
	}

	rows, err := ts.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query tokens: %w", err)
	}
	defer rows.Close()

	var tokens []TokenInfo
	for rows.Next() {
		var token AuthToken
		var metadataJSON string

		err := rows.Scan(
			&token.TokenID,
			&token.ClientName,
			&token.CreatedAt,
			&token.ExpiresAt,
			&token.LastUsedAt,
			&token.IsActive,
			&metadataJSON,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan token: %w", err)
		}

		// Parse metadata
		if err := json.Unmarshal([]byte(metadataJSON), &token.Metadata); err != nil {
			token.Metadata = make(map[string]string)
		}

		tokens = append(tokens, TokenInfo{
			TokenID:    token.TokenID,
			ClientName: token.ClientName,
			CreatedAt:  token.CreatedAt,
			ExpiresAt:  token.ExpiresAt,
			LastUsedAt: token.LastUsedAt,
			IsActive:   token.IsActive,
			Metadata:   token.Metadata,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return tokens, nil
}

// RevokeToken deactivates a token (sets is_active = false)
func (ts *TokenStorage) RevokeToken(tokenID string) error {
	result, err := ts.db.Exec(`
		UPDATE auth_tokens 
		SET is_active = 0 
		WHERE token_id = ?
	`, tokenID)

	if err != nil {
		return fmt.Errorf("failed to revoke token: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("token not found: %s", tokenID)
	}

	return nil
}

// DeleteToken permanently removes a token from the database
func (ts *TokenStorage) DeleteToken(tokenID string) error {
	result, err := ts.db.Exec(`
		DELETE FROM auth_tokens 
		WHERE token_id = ?
	`, tokenID)

	if err != nil {
		return fmt.Errorf("failed to delete token: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("token not found: %s", tokenID)
	}

	return nil
}

// UpdateTokenMetadata updates the metadata for a token
func (ts *TokenStorage) UpdateTokenMetadata(tokenID string, metadata map[string]string) error {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	result, err := ts.db.Exec(`
		UPDATE auth_tokens 
		SET metadata = ? 
		WHERE token_id = ?
	`, string(metadataJSON), tokenID)

	if err != nil {
		return fmt.Errorf("failed to update token metadata: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("token not found: %s", tokenID)
	}

	return nil
}

// CleanupExpiredTokens removes expired tokens from the database
func (ts *TokenStorage) CleanupExpiredTokens() (int64, error) {
	result, err := ts.db.Exec(`
		DELETE FROM auth_tokens 
		WHERE expires_at IS NOT NULL AND expires_at < ?
	`, time.Now())

	if err != nil {
		return 0, fmt.Errorf("failed to cleanup expired tokens: %w", err)
	}

	return result.RowsAffected()
}

// updateLastUsed updates the last_used_at timestamp for a token
func (ts *TokenStorage) updateLastUsed(tokenID string) error {
	_, err := ts.db.Exec(`
		UPDATE auth_tokens 
		SET last_used_at = ? 
		WHERE token_id = ?
	`, time.Now(), tokenID)

	return err
}
