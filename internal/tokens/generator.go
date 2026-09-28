package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

const (
	// TokenPrefix is the prefix for all new Conduit tokens
	TokenPrefix = "conduit_v1_"

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
