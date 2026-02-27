package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

const (
	// TokenPrefix is the prefix for all new Conduit tokens
	TokenPrefix = "conduit_v1_"

	// LegacyTokenPrefix is the prefix for legacy claw tokens (accepted during transition)
	LegacyTokenPrefix = "claw_v1_"

	// TokenEntropyBytes is the number of random bytes for 128-bit entropy
	TokenEntropyBytes = 16 // 128 bits / 8 bits per byte

	// ChecksumBytes is the number of checksum bytes to include
	ChecksumBytes = 2 // 16-bit checksum for corruption detection
)

// GenerateToken creates a new token with the format: conduit_v1_ + base58(entropy + checksum)
// The token includes 128-bit entropy plus a 16-bit checksum for corruption detection.
func GenerateToken() (string, error) {
	// Generate 16 random bytes (128 bits of entropy)
	entropy := make([]byte, TokenEntropyBytes)
	if _, err := rand.Read(entropy); err != nil {
		return "", fmt.Errorf("failed to generate random entropy: %w", err)
	}

	return GenerateTokenFromEntropy(entropy)
}

// GenerateTokenFromEntropy creates a token from provided entropy (useful for testing)
func GenerateTokenFromEntropy(entropy []byte) (string, error) {
	if len(entropy) != TokenEntropyBytes {
		return "", fmt.Errorf("entropy must be exactly %d bytes", TokenEntropyBytes)
	}

	// Generate checksum from entropy
	checksum := generateChecksum(entropy)

	// Combine entropy + checksum
	tokenData := make([]byte, 0, TokenEntropyBytes+ChecksumBytes)
	tokenData = append(tokenData, entropy...)
	tokenData = append(tokenData, checksum...)

	// Encode to base58
	encoded := base58Encode(tokenData)

	// Return with prefix
	return TokenPrefix + encoded, nil
}

// ValidateToken performs secure, timing-safe token validation
// Accepts both new (conduit_v1_) and legacy (claw_v1_) token prefixes
func ValidateToken(token string) bool {
	// Check basic format first
	if !IsValidTokenFormat(token) {
		return false
	}

	// Extract token data (entropy + checksum) - determine which prefix was used
	prefix := getTokenPrefix(token)
	suffix := token[len(prefix):]
	tokenData, err := base58Decode(suffix)
	if err != nil {
		return false
	}

	// Verify total length (entropy + checksum)
	expectedLen := TokenEntropyBytes + ChecksumBytes
	if len(tokenData) != expectedLen {
		return false
	}

	// Split entropy and checksum
	entropy := tokenData[:TokenEntropyBytes]
	providedChecksum := tokenData[TokenEntropyBytes:]

	// Calculate expected checksum
	expectedChecksum := generateChecksum(entropy)

	// Timing-safe checksum comparison
	return ValidateTokenTiming(string(providedChecksum), string(expectedChecksum))
}

// CompareTokens performs timing-safe comparison of two tokens
func CompareTokens(token1, token2 string) bool {
	return ValidateTokenTiming(token1, token2)
}

// IsValidTokenFormat checks if a token has the correct format and structure
// Accepts both new (conduit_v1_) and legacy (claw_v1_) token prefixes
func IsValidTokenFormat(token string) bool {
	// Check which prefix the token uses
	prefix := getTokenPrefix(token)
	if prefix == "" {
		return false
	}

	// Check minimum length (prefix + some encoded data)
	if len(token) < len(prefix)+1 {
		return false
	}

	// Check if the remaining part is valid base58
	suffix := token[len(prefix):]
	if !IsValidBase58(suffix) {
		return false
	}

	// Try to decode and verify length
	decoded, err := base58Decode(suffix)
	if err != nil {
		return false
	}

	// Check expected length (entropy + checksum)
	expectedLen := TokenEntropyBytes + ChecksumBytes
	return len(decoded) == expectedLen
}

// getTokenPrefix returns the prefix used by the token, or empty string if invalid
func getTokenPrefix(token string) string {
	if len(token) >= len(TokenPrefix) && token[:len(TokenPrefix)] == TokenPrefix {
		return TokenPrefix
	}
	if len(token) >= len(LegacyTokenPrefix) && token[:len(LegacyTokenPrefix)] == LegacyTokenPrefix {
		return LegacyTokenPrefix
	}
	return ""
}

// GetTokenEntropy extracts the entropy portion from a valid token (for testing/debugging)
func GetTokenEntropy(token string) ([]byte, error) {
	if !IsValidTokenFormat(token) {
		return nil, fmt.Errorf("invalid token format")
	}

	prefix := getTokenPrefix(token)
	suffix := token[len(prefix):]
	tokenData, err := base58Decode(suffix)
	if err != nil {
		return nil, fmt.Errorf("failed to decode token: %w", err)
	}

	if len(tokenData) != TokenEntropyBytes+ChecksumBytes {
		return nil, fmt.Errorf("invalid token data length")
	}

	// Verify checksum before returning entropy
	entropy := tokenData[:TokenEntropyBytes]
	providedChecksum := tokenData[TokenEntropyBytes:]
	expectedChecksum := generateChecksum(entropy)

	if !ValidateTokenTiming(string(providedChecksum), string(expectedChecksum)) {
		return nil, fmt.Errorf("token checksum validation failed")
	}

	return entropy, nil
}

// GetTokenDisplay returns a shortened version of the token for display (first 12 chars)
func GetTokenDisplay(token string) string {
	if len(token) < 12 {
		return token
	}
	return token[:12] + "..."
}

// GetTokenPrefix returns the first n characters of a token for display
// If the token is shorter than n, returns the whole token
func GetTokenPrefix(token string, n int) string {
	if len(token) <= n {
		return token
	}
	return token[:n]
}

// generateChecksum creates a 16-bit checksum from entropy using SHA256
func generateChecksum(entropy []byte) []byte {
	hash := sha256.Sum256(entropy)
	// Use first 2 bytes of SHA256 hash as checksum
	return hash[:ChecksumBytes]
}
