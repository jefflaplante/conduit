package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	// HashVersionPlainSHA256 is the original plain SHA256 hash (v1)
	HashVersionPlainSHA256 = 1
	// HashVersionHMACSHA256 is the HMAC-SHA256 hash with server secret (v2)
	HashVersionHMACSHA256 = 2
)

// RevocationCallback is called when a token is revoked, with the token ID.
type RevocationCallback func(tokenID string)

// TokenStorage manages authentication tokens in the database
type TokenStorage struct {
	db       *sql.DB
	secret   []byte // HMAC secret key for token hashing
	onRevoke RevocationCallback
	revokeMu sync.Mutex

	// conduit-31jg.4: last_used_at writes are throttled to once per
	// lastUsedInterval per token so every authenticated request does not
	// turn into a SQLite write.
	lastUsedMu      sync.Mutex
	lastUsedWritten map[string]time.Time
}

// lastUsedInterval is the minimum time between last_used_at writes for a
// single token (conduit-31jg.4).
const lastUsedInterval = time.Minute

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

// ErrNoTokenSecret is returned when a token store is constructed without an
// HMAC secret. conduit-31jg.48: there is no ephemeral-key fallback any more —
// such a key silently invalidated every token on restart (and a CLI using one
// minted tokens the server could never validate).
var ErrNoTokenSecret = errors.New("token storage: no HMAC token secret configured (set auth.token_secret, " + TokenSecretEnvVar + ", or let ResolveTokenStore persist one)")

// OpenTokenStorage creates a token storage instance, returning
// ErrNoTokenSecret when secret is empty. Production callers obtain the
// secret from ResolveTokenStore.
func OpenTokenStorage(db *sql.DB, secret string) (*TokenStorage, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, ErrNoTokenSecret
	}
	return NewTokenStorage(db, secret), nil
}

// NewTokenStorage creates a new token storage instance. The secret parameter
// is the HMAC key for token hashing (hex-decoded when it is valid hex).
//
// conduit-31jg.48: an empty secret no longer generates an ephemeral random
// key. The returned storage fails closed instead: CreateToken and
// ValidateToken return ErrNoTokenSecret. Prefer OpenTokenStorage, which
// reports the problem at construction time.
func NewTokenStorage(db *sql.DB, secret string) *TokenStorage {
	if strings.TrimSpace(secret) == "" {
		log.Printf("ERROR: %v", ErrNoTokenSecret)
		return &TokenStorage{db: db}
	}
	var key []byte
	// Try hex-decoding first; fall back to raw string
	if decoded, err := hex.DecodeString(secret); err == nil {
		key = decoded
	} else {
		key = []byte(secret)
	}
	return &TokenStorage{db: db, secret: key}
}

// OnRevoke registers a callback that is invoked after a token is successfully
// revoked. The callback receives the token ID that was revoked. Only one
// callback can be registered; subsequent calls replace the previous one.
func (ts *TokenStorage) OnRevoke(cb RevocationCallback) {
	ts.revokeMu.Lock()
	defer ts.revokeMu.Unlock()
	ts.onRevoke = cb
}

// CreateToken generates and stores a new authentication token
func (ts *TokenStorage) CreateToken(req CreateTokenRequest) (*CreateTokenResponse, error) {
	if len(ts.secret) == 0 {
		return nil, ErrNoTokenSecret // conduit-31jg.48: fail closed
	}
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

	// Hash token for storage using HMAC-SHA256
	hashedToken := ts.hashTokenHMAC(rawToken)

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

	// Insert into database with hash_version = 2 (HMAC-SHA256)
	_, err = ts.db.Exec(`
		INSERT INTO auth_tokens
		(token_id, client_name, hashed_token, hash_version, created_at, expires_at, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		tokenID,
		strings.TrimSpace(req.ClientName),
		hashedToken,
		HashVersionHMACSHA256,
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
	if len(ts.secret) == 0 {
		return nil, ErrNoTokenSecret // conduit-31jg.48: fail closed
	}

	// Hash token for storage using HMAC-SHA256
	hashedToken := ts.hashTokenHMAC(rawToken)

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

	// Insert into database with hash_version = 2 (HMAC-SHA256)
	_, err = ts.db.Exec(`
		INSERT INTO auth_tokens
		(token_id, client_name, hashed_token, hash_version, created_at, expires_at, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		tokenID,
		strings.TrimSpace(req.ClientName),
		hashedToken,
		HashVersionHMACSHA256,
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

// ValidateToken checks if a token is valid and updates last_used_at.
// It first tries HMAC-SHA256 (v2), then falls back to plain SHA256 (v1) for
// backwards compatibility with tokens created before the HMAC migration.
// When a v1 token is validated successfully, it is re-hashed as v2.
func (ts *TokenStorage) ValidateToken(rawToken string) (*TokenInfo, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("token is required")
	}
	if len(ts.secret) == 0 {
		return nil, ErrNoTokenSecret // conduit-31jg.48: fail closed
	}

	// Try HMAC-SHA256 hash first (v2)
	hmacHash := ts.hashTokenHMAC(rawToken)

	var token AuthToken
	var metadataJSON string
	var hashVersion int

	row := ts.db.QueryRow(`
		SELECT token_id, client_name, hashed_token, hash_version, created_at, expires_at, last_used_at, is_active, metadata
		FROM auth_tokens
		WHERE hashed_token = ? AND is_active = 1
	`, hmacHash)

	err := row.Scan(
		&token.TokenID,
		&token.ClientName,
		&token.HashedToken,
		&hashVersion,
		&token.CreatedAt,
		&token.ExpiresAt,
		&token.LastUsedAt,
		&token.IsActive,
		&metadataJSON,
	)

	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to validate token: %w", err)
	}

	// If v2 lookup failed, fall back to plain SHA256 (v1)
	needsRehash := false
	if err == sql.ErrNoRows {
		plainHash := hashTokenPlain(rawToken)
		row = ts.db.QueryRow(`
			SELECT token_id, client_name, hashed_token, hash_version, created_at, expires_at, last_used_at, is_active, metadata
			FROM auth_tokens
			WHERE hashed_token = ? AND is_active = 1
		`, plainHash)

		err = row.Scan(
			&token.TokenID,
			&token.ClientName,
			&token.HashedToken,
			&hashVersion,
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
		needsRehash = true
	}

	// Check if token is expired
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, fmt.Errorf("token has expired")
	}

	// Update last_used_at (throttled, conduit-31jg.4). Errors are ignored:
	// bookkeeping must not fail validation.
	if ts.shouldRecordLastUsed(token.TokenID, time.Now()) {
		_ = ts.updateLastUsed(token.TokenID)
	}

	// Re-hash v1 token as v2 for future lookups
	if needsRehash {
		ts.rehashToken(token.TokenID, hmacHash)
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

	// Notify revocation listener (best-effort)
	ts.revokeMu.Lock()
	cb := ts.onRevoke
	ts.revokeMu.Unlock()
	if cb != nil {
		cb(tokenID)
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

// rehashToken upgrades a v1 (plain SHA256) token to v2 (HMAC-SHA256).
// This is best-effort; failures are logged but do not affect validation.
func (ts *TokenStorage) rehashToken(tokenID, newHash string) {
	_, err := ts.db.Exec(`
		UPDATE auth_tokens
		SET hashed_token = ?, hash_version = ?
		WHERE token_id = ?
	`, newHash, HashVersionHMACSHA256, tokenID)
	if err != nil {
		log.Printf("[Auth] Failed to re-hash token %s from v1 to v2: %v", tokenID, err)
	} else {
		log.Printf("[Auth] Upgraded token %s from plain SHA256 (v1) to HMAC-SHA256 (v2)", tokenID)
	}
}

// hashTokenHMAC computes HMAC-SHA256 of the raw token using the server secret
func (ts *TokenStorage) hashTokenHMAC(rawToken string) string {
	mac := hmac.New(sha256.New, ts.secret)
	mac.Write([]byte(rawToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// hashTokenPlain computes a plain SHA256 hash of the raw token (legacy v1)
func hashTokenPlain(rawToken string) string {
	h := sha256.New()
	h.Write([]byte(rawToken))
	return hex.EncodeToString(h.Sum(nil))
}

// shouldRecordLastUsed reports whether last_used_at should be written for
// tokenID now, and if so records the write time. conduit-31jg.4.
func (ts *TokenStorage) shouldRecordLastUsed(tokenID string, now time.Time) bool {
	ts.lastUsedMu.Lock()
	defer ts.lastUsedMu.Unlock()
	if ts.lastUsedWritten == nil {
		ts.lastUsedWritten = make(map[string]time.Time)
	}
	if last, ok := ts.lastUsedWritten[tokenID]; ok && now.Sub(last) < lastUsedInterval {
		return false
	}
	ts.lastUsedWritten[tokenID] = now
	return true
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
