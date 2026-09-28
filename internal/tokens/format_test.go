package tokens

import (
	"strings"
	"testing"
)

func TestBase58Encode(t *testing.T) {
	// Known vectors (Bitcoin base58 alphabet); leading zero bytes map to '1'.
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{name: "empty input", input: []byte{}, expected: ""},
		{name: "single zero byte", input: []byte{0x00}, expected: "1"},
		{name: "two zero bytes", input: []byte{0x00, 0x00}, expected: "11"},
		{name: "single non-zero byte", input: []byte{0x01}, expected: "2"},
		{name: "all ones", input: []byte{0xFF, 0xFF, 0xFF, 0xFF}, expected: "7YXq9G"},
		{name: "leading zeros", input: []byte{0x00, 0x00, 0x28, 0x7f, 0xb4, 0xcd}, expected: "11233QC4"},
		{name: "more leading zeros", input: []byte{0x00, 0x00, 0x00, 0x00, 0x28, 0x7f, 0xb4, 0xcd}, expected: "1111233QC4"},
		{name: "hello world", input: []byte("Hello World!"), expected: "2NEpo7TZRRrLZSi2U"},
		{
			name:     "quick brown fox",
			input:    []byte("The quick brown fox jumps over the lazy dog"),
			expected: "7DdiPPYtxLjCD3wA1po2rvZHTDYjkZYiEtazrfiwJcwnKCizhGFhBGHeRdx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := base58Encode(tt.input); got != tt.expected {
				t.Errorf("base58Encode(%x) = %q, want %q", tt.input, got, tt.expected)
			}
		})
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
