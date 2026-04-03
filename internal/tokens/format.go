package tokens

import (
	"crypto/subtle"
	"fmt"
	"math/big"
)

const (
	// Base58Alphabet is the alphabet used for base58 encoding (Bitcoin style)
	// Excludes confusing characters: 0 (zero), O (capital o), I (capital i), l (lowercase L)
	Base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)

// base58Encode encodes bytes to base58 string using Go's big.Int
func base58Encode(input []byte) string {
	if len(input) == 0 {
		return ""
	}

	// Convert to big integer
	num := big.NewInt(0)
	num.SetBytes(input)

	// Convert to base58
	var result []byte
	base := big.NewInt(58)
	zero := big.NewInt(0)
	remainder := big.NewInt(0)

	for num.Cmp(zero) > 0 {
		num.DivMod(num, base, remainder)
		result = append([]byte{Base58Alphabet[remainder.Int64()]}, result...)
	}

	// Handle leading zeros (represented as '1' in base58)
	for _, b := range input {
		if b != 0 {
			break
		}
		result = append([]byte{Base58Alphabet[0]}, result...)
	}

	return string(result)
}

// base58Decode decodes a base58 string to bytes
func base58Decode(input string) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}

	// Build character map for validation
	charMap := make(map[byte]int)
	for i, c := range []byte(Base58Alphabet) {
		charMap[c] = i
	}

	// Convert from base58
	num := big.NewInt(0)
	base := big.NewInt(58)

	for _, c := range []byte(input) {
		val, ok := charMap[c]
		if !ok {
			return nil, fmt.Errorf("invalid base58 character: %c", c)
		}
		num.Mul(num, base)
		num.Add(num, big.NewInt(int64(val)))
	}

	// Convert to bytes
	result := num.Bytes()

	// Handle leading zeros (represented as '1' in base58)
	for _, c := range []byte(input) {
		if c != Base58Alphabet[0] {
			break
		}
		result = append([]byte{0}, result...)
	}

	return result, nil
}

// ValidateTokenTiming performs timing-safe token comparison to prevent timing attacks
// Returns true if token1 equals token2, using constant-time comparison
func ValidateTokenTiming(token1, token2 string) bool {
	// First check length to avoid short-circuit on different lengths
	if len(token1) != len(token2) {
		// Still do a dummy comparison to maintain constant time
		dummy := make([]byte, 32) // Use a fixed size for the dummy comparison
		subtle.ConstantTimeCompare(dummy, dummy)
		return false
	}

	// Perform constant-time comparison
	return subtle.ConstantTimeCompare([]byte(token1), []byte(token2)) == 1
}

// IsValidBase58 checks if a string contains only valid base58 characters
func IsValidBase58(s string) bool {
	charMap := make(map[byte]bool)
	for _, c := range []byte(Base58Alphabet) {
		charMap[c] = true
	}

	for _, c := range []byte(s) {
		if !charMap[c] {
			return false
		}
	}
	return true
}

// GetEntropyFromToken extracts and decodes the entropy bytes from a token
func GetEntropyFromToken(token string) ([]byte, error) {
	if !IsValidTokenFormat(token) {
		return nil, fmt.Errorf("invalid token format")
	}

	// Extract the base58 suffix
	suffix := token[len(TokenPrefix):]

	// Decode the base58
	tokenData, err := base58Decode(suffix)
	if err != nil {
		return nil, fmt.Errorf("failed to decode token: %w", err)
	}

	// Verify total length (entropy + checksum)
	expectedLen := TokenEntropyBytes + ChecksumBytes
	if len(tokenData) != expectedLen {
		return nil, fmt.Errorf("invalid token data length: expected %d bytes, got %d", expectedLen, len(tokenData))
	}

	// Extract just the entropy portion (without checksum)
	entropy := tokenData[:TokenEntropyBytes]

	return entropy, nil
}
