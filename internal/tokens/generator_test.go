package tokens

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"strings"
	"testing"
)

// assertTokenShape checks the structural invariants of a generated token:
// the conduit_v1_ prefix followed by a non-empty base58 body.
func assertTokenShape(t *testing.T, token string) {
	t.Helper()
	if !strings.HasPrefix(token, TokenPrefix) {
		t.Fatalf("Token doesn't start with prefix %s: %s", TokenPrefix, token)
	}
	body := strings.TrimPrefix(token, TokenPrefix)
	if body == "" {
		t.Fatalf("Token has empty body: %q", token)
	}
	for _, c := range body {
		if !strings.ContainsRune(Base58Alphabet, c) {
			t.Fatalf("Token body contains non-base58 character %q: %s", c, token)
		}
	}
	// 18 bytes (entropy + checksum) encode to at most 25 base58 characters.
	if len(body) > 25 {
		t.Fatalf("Token body too long: %d chars (%s)", len(body), token)
	}
}

func TestGenerateToken(t *testing.T) {
	for i := 0; i < 50; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken() error = %v", err)
		}
		assertTokenShape(t, token)

		// Minimum length: leading zero bytes shorten the encoding only
		// with negligible probability for 144 random bits.
		if len(token) < len(TokenPrefix)+20 {
			t.Errorf("Token too short: %d chars (%s)", len(token), token)
		}
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
	sequential := make([]byte, TokenEntropyBytes)
	for i := range sequential {
		sequential[i] = byte(i)
	}

	tests := []struct {
		name        string
		entropy     []byte
		want        string // known vector: prefix + base58(entropy || sha256(entropy)[:2])
		errContains string
	}{
		{
			name:    "sequential entropy",
			entropy: sequential,
			want:    "conduit_v1_1YruNJgvoA2CUCpKWeF9JFv",
		},
		{
			name:    "all zeros entropy",
			entropy: make([]byte, TokenEntropyBytes),
			want:    "conduit_v1_11111111111111115Cz",
		},
		{
			name:    "all ones entropy",
			entropy: bytes.Repeat([]byte{0xFF}, TokenEntropyBytes),
			want:    "conduit_v1_BcrMA6SqZZvEpAezV9QmfHd81",
		},
		{
			name:        "too short entropy",
			entropy:     make([]byte, TokenEntropyBytes-1),
			errContains: "must be exactly",
		},
		{
			name:        "too long entropy",
			entropy:     make([]byte, TokenEntropyBytes+1),
			errContains: "must be exactly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := GenerateTokenFromEntropy(tt.entropy)
			if tt.errContains != "" {
				if err == nil {
					t.Fatalf("GenerateTokenFromEntropy() = %q, want error", token)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error to contain %q, got %q", tt.errContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateTokenFromEntropy() error = %v", err)
			}
			if token != tt.want {
				t.Errorf("GenerateTokenFromEntropy() = %q, want %q", token, tt.want)
			}
			assertTokenShape(t, token)

			// The body must be base58(entropy || 2-byte SHA256 checksum).
			sum := sha256.Sum256(tt.entropy)
			data := append(append([]byte{}, tt.entropy...), sum[:ChecksumBytes]...)
			if got := TokenPrefix + base58Encode(data); got != token {
				t.Errorf("token %q does not encode entropy+checksum (%q)", token, got)
			}
		})
	}
}

func TestGenerateTokenFromEntropyDeterministic(t *testing.T) {
	entropy := make([]byte, TokenEntropyBytes)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	a, err := GenerateTokenFromEntropy(entropy)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateTokenFromEntropy(entropy)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same entropy produced different tokens: %q vs %q", a, b)
	}

	// A single-bit change in entropy must change the token.
	entropy[0] ^= 0x01
	c, err := GenerateTokenFromEntropy(entropy)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Errorf("different entropy produced identical token %q", a)
	}
}

func TestGetTokenPrefix(t *testing.T) {
	tests := []struct {
		token string
		n     int
		want  string
	}{
		{"conduit_v1_abcdef", 12, "conduit_v1_a"},
		{"short", 12, "short"},
		{"exactly12chr", 12, "exactly12chr"},
		{"", 4, ""},
	}
	for _, tt := range tests {
		if got := GetTokenPrefix(tt.token, tt.n); got != tt.want {
			t.Errorf("GetTokenPrefix(%q, %d) = %q, want %q", tt.token, tt.n, got, tt.want)
		}
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

func BenchmarkBase58Encode(b *testing.B) {
	entropy := make([]byte, TokenEntropyBytes)
	rand.Read(entropy)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		base58Encode(entropy)
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
}
