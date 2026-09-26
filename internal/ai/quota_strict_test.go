package ai

import (
	"errors"
	"fmt"
	"testing"
)

// conduit-31jg.18(c): IsQuotaError substring-matched "400" (so "4000"
// matched) and loose words like "limit"/"exceeded" (so Anthropic's
// context-overflow 400 "input length and max_tokens exceed context limit"
// was treated as quota and handed to the fallback model).
func TestIsQuotaError_StatusAndTypeBased(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// true: quota / billing exhaustion
		{"claude max out of extra usage", errors.New(`API error: 400 - {"type":"error","error":{"type":"invalid_request_error","message":"You're out of extra usage"}}`), true},
		{"anthropic credit balance", errors.New(`API error: 400 - {"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`), true},
		{"openai insufficient_quota 429", errors.New(`API error: 429 - {"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`), true},
		{"z.ai insufficient balance 429", errors.New(`API error: 429 - {"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`), true},
		{"billing_error type", errors.New(`API error: 402 - {"type":"error","error":{"type":"billing_error","message":"payment required"}}`), true},
		{"wrapped by router", fmt.Errorf("AI provider error: %w", errors.New(`API error: 400 - {"error":{"message":"quota exceeded"}}`)), true},
		{"legacy bare 400 prefix", errors.New("400 - out of extra usage"), true},
		{"400 usage limit", errors.New("API error: 400 - usage limit exceeded"), true},

		// false: context overflow is NOT quota
		{"anthropic context overflow", errors.New(`API error: 400 - {"type":"error","error":{"type":"invalid_request_error","message":"input length and ` + "`max_tokens`" + ` exceed context limit: 190000 + 16000 > 200000"}}`), false},
		{"prompt too long", errors.New(`API error: 400 - {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`), false},
		// false: "400" inside a number is not a status code
		{"4000 in 500 body", errors.New("API error: 500 - internal error: limit exceeded for max_tokens 4000"), false},
		{"4000 in 529 overloaded body", errors.New(`API error: 529 - {"type":"error","error":{"type":"overloaded_error","message":"Overloaded (limit 4000)"}}`), false},
		// false: plain rate limit / overload is retryable, not quota
		{"anthropic 429 rate limit", errors.New(`API error: 429 - {"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`), false},
		{"mid-stream overloaded", errors.New("anthropic stream error: overloaded_error: Overloaded"), false},
		// false: auth propagates
		{"401 with quota words", errors.New("API error: 401 - quota limit reached"), false},
		{"403", errors.New("API error: 403 - forbidden"), false},
		{"no status", errors.New("quota exceeded"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsQuotaError(tt.err); got != tt.want {
				t.Errorf("IsQuotaError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestProviderStatusCode(t *testing.T) {
	cases := map[string]int{
		"API error: 529 - overloaded":         529,
		"AI provider error: API error: 400 -": 400,
		"400 quota exceeded for model x":      400,
		"status code 429":                     429,
		"max_tokens 4000 exceeded":            0,
		"something else":                      0,
	}
	for msg, want := range cases {
		if got := providerStatusCode(errors.New(msg)); got != want {
			t.Errorf("providerStatusCode(%q) = %d, want %d", msg, got, want)
		}
	}
}
