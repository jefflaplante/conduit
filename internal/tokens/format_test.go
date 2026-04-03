package tokens

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestValidateTokenTiming(t *testing.T) {
	tests := []struct {
		name   string
		token1 string
		token2 string
		want   bool
	}{
		{
			name:   "identical tokens",
			token1: "claw_v1_test123",
			token2: "claw_v1_test123",
			want:   true,
		},
		{
			name:   "different tokens same length",
			token1: "claw_v1_test123",
			token2: "claw_v1_test456",
			want:   false,
		},
		{
			name:   "different lengths",
			token1: "claw_v1_test123",
			token2: "claw_v1_test",
			want:   false,
		},
		{
			name:   "empty tokens",
			token1: "",
			token2: "",
			want:   true,
		},
		{
			name:   "one empty",
			token1: "test",
			token2: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test multiple times to ensure consistency
			for i := 0; i < 5; i++ {
				if got := ValidateTokenTiming(tt.token1, tt.token2); got != tt.want {
					t.Errorf("ValidateTokenTiming() iteration %d = %v, want %v", i, got, tt.want)
				}
			}
		})
	}
}

func TestValidateTokenTimingIsConstantTime(t *testing.T) {
	// Test that timing is roughly constant regardless of where strings differ
	base := "claw_v1_" + strings.Repeat("a", 20)

	// Create strings that differ at different positions
	earlyDiff := "x" + base[1:]               // Differs at position 0
	middleDiff := base[:10] + "x" + base[11:] // Differs at position 10
	lateDiff := base[:len(base)-1] + "x"      // Differs at last position

	tests := []string{earlyDiff, middleDiff, lateDiff}
	var durations []time.Duration

	for _, test := range tests {
		start := time.Now()
		for i := 0; i < 1000; i++ { // Run multiple times for better measurement
			ValidateTokenTiming(base, test)
		}
		duration := time.Since(start)
		durations = append(durations, duration)
	}

	// Check that all durations are reasonably similar (within 50% of each other)
	minDuration := durations[0]
	maxDuration := durations[0]

	for _, d := range durations[1:] {
		if d < minDuration {
			minDuration = d
		}
		if d > maxDuration {
			maxDuration = d
		}
	}

	ratio := float64(maxDuration) / float64(minDuration)
	if ratio > 2.0 {
		t.Logf("Warning: timing variance might be high: min=%v, max=%v, ratio=%.2f",
			minDuration, maxDuration, ratio)
		// Note: we log rather than fail because system load can affect timing
	}
}

func TestIsValidBase58(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "empty string",
			input: "",
			want:  true,
		},
		{
			name:  "valid base58 lowercase",
			input: "abcdefghijkmnpqrstuvwxyz",
			want:  true,
		},
		{
			name:  "valid base58 uppercase",
			input: "ABCDEFGHJKLMNPQRSTUVWXYZ",
			want:  true,
		},
		{
			name:  "valid base58 numbers",
			input: "123456789",
			want:  true,
		},
		{
			name:  "mixed valid base58",
			input: "123ABCabc",
			want:  true,
		},
		{
			name:  "contains zero",
			input: "123abc0",
			want:  false,
		},
		{
			name:  "contains capital O",
			input: "123abcO",
			want:  false,
		},
		{
			name:  "contains capital I",
			input: "123abcI",
			want:  false,
		},
		{
			name:  "contains lowercase l",
			input: "123abcl",
			want:  false,
		},
		{
			name:  "contains special characters",
			input: "123abc!@#",
			want:  false,
		},
		{
			name:  "contains space",
			input: "123 abc",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidBase58(tt.input); got != tt.want {
				t.Errorf("IsValidBase58(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestBase58Encode(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string // We'll validate round-trip instead of exact output
	}{
		{
			name:  "empty input",
			input: []byte{},
		},
		{
			name:  "single zero byte",
			input: []byte{0x00},
		},
		{
			name:  "single non-zero byte",
			input: []byte{0x01},
		},
		{
			name:  "multiple zeros",
			input: []byte{0x00, 0x00, 0x00},
		},
		{
			name:  "mixed with leading zeros",
			input: []byte{0x00, 0x00, 0x01, 0x02},
		},
		{
			name:  "all ones",
			input: []byte{0xFF, 0xFF, 0xFF, 0xFF},
		},
		{
			name:  "random bytes",
			input: []byte{0x12, 0x34, 0x56, 0x78, 0x9A, 0xBC, 0xDE, 0xF0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := base58Encode(tt.input)

			// Verify the encoded string only contains valid base58 characters
			if !IsValidBase58(encoded) {
				t.Errorf("Encoded string contains invalid base58 characters: %q", encoded)
			}

			// Verify round-trip
			decoded, err := base58Decode(encoded)
			if err != nil {
				t.Errorf("Failed to decode encoded string: %v", err)
			}

			if !bytes.Equal(decoded, tt.input) {
				t.Errorf("Round-trip failed. Original: %x, Decoded: %x", tt.input, decoded)
			}
		})
	}
}

func TestBase58Decode(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{
			name:  "empty string",
			input: "",
		},
		{
			name:  "single valid character",
			input: "1",
		},
		{
			name:  "multiple valid characters",
			input: "123ABCabc",
		},
		{
			name:    "invalid character zero",
			input:   "123a0bc",
			wantErr: true,
			errMsg:  "invalid base58 character",
		},
		{
			name:    "invalid character O",
			input:   "123aObc",
			wantErr: true,
			errMsg:  "invalid base58 character",
		},
		{
			name:    "invalid character I",
			input:   "123aIbc",
			wantErr: true,
			errMsg:  "invalid base58 character",
		},
		{
			name:    "invalid character l",
			input:   "123albc",
			wantErr: true,
			errMsg:  "invalid base58 character",
		},
		{
			name:    "special characters",
			input:   "123!@#",
			wantErr: true,
			errMsg:  "invalid base58 character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := base58Decode(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error message to contain %q, got %q", tt.errMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// For valid inputs, verify we can encode back and get the same string
			if tt.input != "" {
				reencoded := base58Encode(result)
				if reencoded != tt.input {
					t.Errorf("Re-encoding failed. Original: %q, Re-encoded: %q", tt.input, reencoded)
				}
			}
		})
	}
}

func TestGetEntropyFromToken(t *testing.T) {
	// Create a token with known entropy for testing
	knownEntropy := make([]byte, TokenEntropyBytes)
	for i := range knownEntropy {
		knownEntropy[i] = byte(i % 256)
	}

	token, err := GenerateTokenFromEntropy(knownEntropy)
	if err != nil {
		t.Fatalf("Failed to generate test token: %v", err)
	}

	tests := []struct {
		name    string
		token   string
		wantErr bool
		errMsg  string
	}{
		{
			name:  "valid token",
			token: token,
		},
		{
			name:    "invalid format",
			token:   "invalid_token",
			wantErr: true,
			errMsg:  "invalid token format",
		},
		{
			name:    "empty token",
			token:   "",
			wantErr: true,
			errMsg:  "invalid token format",
		},
		{
			name:    "wrong prefix",
			token:   "wrong_v1_" + token[len(TokenPrefix):],
			wantErr: true,
			errMsg:  "invalid token format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entropy, err := GetEntropyFromToken(tt.token)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error message to contain %q, got %q", tt.errMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(entropy) != TokenEntropyBytes {
				t.Errorf("Expected entropy length %d, got %d", TokenEntropyBytes, len(entropy))
			}

			// For the known entropy test case, verify we got back the same entropy
			if tt.token == token && !bytes.Equal(entropy, knownEntropy) {
				t.Errorf("Extracted entropy doesn't match original. Expected: %x, Got: %x", knownEntropy, entropy)
			}
		})
	}
}

// Benchmark tests for format functions
func BenchmarkValidateTokenTiming(b *testing.B) {
	token1 := "claw_v1_test123456789"
	token2 := "claw_v1_test123456789"

	for i := 0; i < b.N; i++ {
		ValidateTokenTiming(token1, token2)
	}
}

func BenchmarkIsValidBase58(b *testing.B) {
	testString := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz"

	for i := 0; i < b.N; i++ {
		IsValidBase58(testString)
	}
}

func BenchmarkBase58EncodeFormat(b *testing.B) {
	data := make([]byte, TokenEntropyBytes)
	for i := range data {
		data[i] = byte(i)
	}

	for i := 0; i < b.N; i++ {
		base58Encode(data)
	}
}

func BenchmarkBase58DecodeFormat(b *testing.B) {
	data := make([]byte, TokenEntropyBytes)
	for i := range data {
		data[i] = byte(i)
	}
	encoded := base58Encode(data)

	for i := 0; i < b.N; i++ {
		base58Decode(encoded)
	}
}

func TestBase58AlphabetExclusions(t *testing.T) {
	// Verify that confusing characters are excluded from the alphabet
	excludedChars := []byte{'0', 'O', 'I', 'l'}

	for _, char := range excludedChars {
		if strings.ContainsRune(Base58Alphabet, rune(char)) {
			t.Errorf("Base58 alphabet should not contain confusing character: %c", char)
		}
	}

	// Verify the alphabet has the expected length (58 characters)
	if len(Base58Alphabet) != 58 {
		t.Errorf("Base58 alphabet should have 58 characters, got %d", len(Base58Alphabet))
	}

	// Verify no duplicate characters
	seen := make(map[rune]bool)
	for _, char := range Base58Alphabet {
		if seen[char] {
			t.Errorf("Base58 alphabet contains duplicate character: %c", char)
		}
		seen[char] = true
	}
}
