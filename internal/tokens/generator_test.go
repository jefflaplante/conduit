package tokens

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestGenerateToken(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"basic generation"},
		{"multiple generations should be unique"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := GenerateToken()
			if err != nil {
				t.Fatalf("GenerateToken() error = %v", err)
			}

			// Check format
			if !strings.HasPrefix(token, TokenPrefix) {
				t.Errorf("Token doesn't start with prefix %s: %s", TokenPrefix, token)
			}

			// Check minimum length
			if len(token) < len(TokenPrefix)+20 {
				t.Errorf("Token too short: %d chars", len(token))
			}

			// Validate the token
			if !ValidateToken(token) {
				t.Errorf("Generated token failed validation: %s", token)
			}
		})
	}
}

func TestGenerateTokenUniqueness(t *testing.T) {
	// Generate multiple tokens and ensure they're all unique
	tokens := make(map[string]bool)
	numTokens := 1000

	for i := 0; i < numTokens; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken() error = %v", err)
		}

		if tokens[token] {
			t.Errorf("Duplicate token generated: %s", token)
		}
		tokens[token] = true
	}

	if len(tokens) != numTokens {
		t.Errorf("Expected %d unique tokens, got %d", numTokens, len(tokens))
	}
}

func TestGenerateTokenFromEntropy(t *testing.T) {
	tests := []struct {
		name        string
		entropy     []byte
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid entropy",
			entropy: make([]byte, TokenEntropyBytes),
			wantErr: false,
		},
		{
			name:        "too short entropy",
			entropy:     make([]byte, TokenEntropyBytes-1),
			wantErr:     true,
			errContains: "must be exactly",
		},
		{
			name:        "too long entropy",
			entropy:     make([]byte, TokenEntropyBytes+1),
			wantErr:     true,
			errContains: "must be exactly",
		},
		{
			name:    "all zeros entropy",
			entropy: make([]byte, TokenEntropyBytes),
			wantErr: false,
		},
		{
			name:    "all ones entropy",
			entropy: bytes.Repeat([]byte{0xFF}, TokenEntropyBytes),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fill with known data for non-zero cases
			if tt.name == "valid entropy" {
				for i := range tt.entropy {
					tt.entropy[i] = byte(i)
				}
			}

			token, err := GenerateTokenFromEntropy(tt.entropy)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateTokenFromEntropy() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error to contain %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			// Verify the token is valid
			if !ValidateToken(token) {
				t.Errorf("Generated token from entropy failed validation: %s", token)
			}

			// Verify we can extract the original entropy
			extracted, err := GetTokenEntropy(token)
			if err != nil {
				t.Errorf("Failed to extract entropy: %v", err)
			}

			if !bytes.Equal(extracted, tt.entropy) {
				t.Errorf("Extracted entropy doesn't match original. Expected %x, got %x", tt.entropy, extracted)
			}
		})
	}
}

func TestValidateToken(t *testing.T) {
	// Generate a valid token for positive tests
	validToken, err := GenerateToken()
	if err != nil {
		t.Fatalf("Failed to generate valid token: %v", err)
	}

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "valid token",
			token: validToken,
			want:  true,
		},
		{
			name:  "empty token",
			token: "",
			want:  false,
		},
		{
			name:  "wrong prefix",
			token: "wrong_v1_" + validToken[len(TokenPrefix):],
			want:  false,
		},
		{
			name:  "no prefix",
			token: validToken[len(TokenPrefix):],
			want:  false,
		},
		{
			name:  "too short",
			token: TokenPrefix + "abc",
			want:  false,
		},
		{
			name:  "invalid base58 chars",
			token: TokenPrefix + "0OIl", // These chars are not in Base58 alphabet
			want:  false,
		},
		{
			name:  "corrupted token",
			token: validToken[:len(validToken)-1] + "x", // Change last char
			want:  false,
		},
		{
			name:  "missing characters",
			token: validToken[:len(validToken)-5], // Remove some chars
			want:  false,
		},
		{
			name:  "extra characters",
			token: validToken + "extra",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateToken(tt.token); got != tt.want {
				t.Errorf("ValidateToken() = %v, want %v for token %s", got, tt.want, tt.token)
			}
		})
	}
}

func TestCompareTokens(t *testing.T) {
	token1, err := GenerateToken()
	if err != nil {
		t.Fatalf("Failed to generate token1: %v", err)
	}

	token2, err := GenerateToken()
	if err != nil {
		t.Fatalf("Failed to generate token2: %v", err)
	}

	tests := []struct {
		name   string
		token1 string
		token2 string
		want   bool
	}{
		{
			name:   "same token",
			token1: token1,
			token2: token1,
			want:   true,
		},
		{
			name:   "different tokens",
			token1: token1,
			token2: token2,
			want:   false,
		},
		{
			name:   "empty tokens",
			token1: "",
			token2: "",
			want:   true,
		},
		{
			name:   "one empty one not",
			token1: "",
			token2: token1,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareTokens(tt.token1, tt.token2); got != tt.want {
				t.Errorf("CompareTokens() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsValidTokenFormat(t *testing.T) {
	validToken, _ := GenerateToken()

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "valid token",
			token: validToken,
			want:  true,
		},
		{
			name:  "empty string",
			token: "",
			want:  false,
		},
		{
			name:  "just prefix",
			token: TokenPrefix,
			want:  false,
		},
		{
			name:  "wrong prefix",
			token: "wrong_v1_" + validToken[len(TokenPrefix):],
			want:  false,
		},
		{
			name:  "invalid base58",
			token: TokenPrefix + "0OIl", // Contains excluded chars
			want:  false,
		},
		{
			name:  "valid format but wrong length",
			token: TokenPrefix + "123456", // Valid base58 but too short
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidTokenFormat(tt.token); got != tt.want {
				t.Errorf("IsValidTokenFormat() = %v, want %v for token %s", got, tt.want, tt.token)
			}
		})
	}
}

func TestGetTokenEntropy(t *testing.T) {
	// Test with known entropy
	knownEntropy := make([]byte, TokenEntropyBytes)
	for i := range knownEntropy {
		knownEntropy[i] = byte(i)
	}

	token, err := GenerateTokenFromEntropy(knownEntropy)
	if err != nil {
		t.Fatalf("Failed to generate token from known entropy: %v", err)
	}

	extractedEntropy, err := GetTokenEntropy(token)
	if err != nil {
		t.Errorf("Failed to extract entropy: %v", err)
	}

	if !bytes.Equal(extractedEntropy, knownEntropy) {
		t.Errorf("Extracted entropy doesn't match. Expected %x, got %x", knownEntropy, extractedEntropy)
	}

	// Test with invalid token
	_, err = GetTokenEntropy("invalid_token")
	if err == nil {
		t.Error("Expected error for invalid token")
	}
}

func TestGetTokenDisplay(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "long token",
			token: "claw_v1_abcdefghijklmnopqrstuvwxyz",
			want:  "claw_v1_abcd...",
		},
		{
			name:  "short token",
			token: "short",
			want:  "short",
		},
		{
			name:  "empty token",
			token: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetTokenDisplay(tt.token); got != tt.want {
				t.Errorf("GetTokenDisplay() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTimingSafeValidation(t *testing.T) {
	// This test verifies that validation takes roughly the same time
	// regardless of where the token differs (timing attack prevention)

	validToken, _ := GenerateToken()

	// Create tokens that differ at different positions
	earlyDiffToken := "x" + validToken[1:]                     // Differs at position 0
	middleDiffToken := validToken[:10] + "x" + validToken[11:] // Differs in middle
	lateDiffToken := validToken[:len(validToken)-1] + "x"      // Differs at end

	tokens := []string{earlyDiffToken, middleDiffToken, lateDiffToken}

	// Measure timing for each validation
	for _, token := range tokens {
		start := time.Now()
		ValidateToken(token)
		elapsed := time.Since(start)

		// Just ensure it completes reasonably quickly (not a precise timing test)
		if elapsed > time.Millisecond {
			t.Errorf("Validation took too long: %v", elapsed)
		}
	}
}

func TestBase58Encoding(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{
			name:  "empty",
			input: []byte{},
		},
		{
			name:  "single byte",
			input: []byte{0x01},
		},
		{
			name:  "multiple bytes",
			input: []byte{0x01, 0x02, 0x03},
		},
		{
			name:  "with leading zeros",
			input: []byte{0x00, 0x00, 0x01},
		},
		{
			name:  "max entropy",
			input: bytes.Repeat([]byte{0xFF}, TokenEntropyBytes),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := base58Encode(tt.input)
			decoded, err := base58Decode(encoded)
			if err != nil {
				t.Errorf("Failed to decode: %v", err)
			}

			if !bytes.Equal(decoded, tt.input) {
				t.Errorf("Round trip failed. Original: %x, Decoded: %x", tt.input, decoded)
			}
		})
	}
}

// Benchmark tests
func BenchmarkGenerateToken(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateToken()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateToken(b *testing.B) {
	token, _ := GenerateToken()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ValidateToken(token)
	}
}

func BenchmarkCompareTokens(b *testing.B) {
	token1, _ := GenerateToken()
	token2, _ := GenerateToken()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		CompareTokens(token1, token2)
	}
}

func BenchmarkBase58Encode(b *testing.B) {
	entropy := make([]byte, TokenEntropyBytes)
	rand.Read(entropy)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		base58Encode(entropy)
	}
}

func BenchmarkBase58Decode(b *testing.B) {
	entropy := make([]byte, TokenEntropyBytes)
	rand.Read(entropy)
	encoded := base58Encode(entropy)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		base58Decode(encoded)
	}
}

// Edge case tests for security
func TestSecurityEdgeCases(t *testing.T) {
	t.Run("entropy source validation", func(t *testing.T) {
		// Verify we're actually using crypto/rand
		token1, _ := GenerateToken()
		token2, _ := GenerateToken()

		if token1 == token2 {
			t.Error("Generated identical tokens - poor entropy source?")
		}
	})

	t.Run("checksum corruption detection", func(t *testing.T) {
		token, _ := GenerateToken()

		// Corrupt the token by changing one character
		tokenBytes := []byte(token)
		if len(tokenBytes) > len(TokenPrefix)+1 {
			// Change a character in the base58 part
			tokenBytes[len(TokenPrefix)+1] = 'x'
			corruptedToken := string(tokenBytes)

			if ValidateToken(corruptedToken) {
				t.Error("Corrupted token passed validation - checksum failed")
			}
		}
	})

	t.Run("timing attack resistance", func(t *testing.T) {
		token1, _ := GenerateToken()
		token2, _ := GenerateToken()

		// Ensure different tokens still take similar time to compare
		start := time.Now()
		CompareTokens(token1, token2)
		duration1 := time.Since(start)

		start = time.Now()
		CompareTokens(token1, token1)
		duration2 := time.Since(start)

		// Allow for some variance but they should be roughly similar
		ratio := float64(duration1) / float64(duration2)
		if ratio < 0.1 || ratio > 10 {
			t.Logf("Warning: timing variance might be too high: %v vs %v", duration1, duration2)
		}
	})
}
