package tokens

import "math/big"

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
