package ai

import (
	"fmt"
	"testing"
)

// TestIsQuotaOrAuthError tests the error classification for quota and auth errors
func TestIsQuotaOrAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "400 with quota text",
			err:  fmt.Errorf("API error: 400 - %s", `{"error":{"message":"out of extra usage"}}`),
			want: true,
		},
		{
			name: "401 unauthorized — NOT quota (auth propagates per NoFallbackOnAuthError contract)",
			err:  fmt.Errorf("API error: 401 - unauthorized"),
			want: false,
		},
		{
			name: "403 forbidden — NOT quota (auth propagates)",
			err:  fmt.Errorf("API error: 403 - forbidden"),
			want: false,
		},
		{
			name: "400 without quota text",
			err:  fmt.Errorf("API error: 400 - bad request"),
			want: false,
		},
		{
			name: "402 payment required",
			err:  fmt.Errorf("API error: 402 - payment required"),
			want: false,
		},
		{
			name: "429 rate limit",
			err:  fmt.Errorf("API error: 429 - rate limit exceeded"),
			want: false,
		},
		{
			name: "500 server error",
			err:  fmt.Errorf("API error: 500 - internal server error"),
			want: false,
		},
		{
			name: "timeout error",
			err:  fmt.Errorf("context deadline exceeded"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "generic error",
			err:  fmt.Errorf("something went wrong"),
			want: false,
		},
		{
			name: "400 with extra usage in message",
			err:  fmt.Errorf("API error: 400 - %s", `{"error":{"message":"Quota exceeded: out of extra usage for this account"}}`),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsQuotaError(tt.err)
			if got != tt.want {
				t.Errorf("IsQuotaError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsQuotaOrAuthErrorWithQuotaVariations tests various quota-related error messages
func TestIsQuotaOrAuthErrorWithQuotaVariations(t *testing.T) {
	tests := []struct {
		name    string
		errMsg  string
		want    bool
	}{
		{
			name:   "exact match",
			errMsg: "out of extra usage",
			want:   true,
		},
		{
			name:   "contains quota",
			errMsg: "quota exceeded for this account",
			want:   true,
		},
		{
			name:   "mixed case",
			errMsg: "Out Of Extra Usage",
			want:   true,
		},
		{
			name:   "with punctuation",
			errMsg: "Error: out of extra usage. Please upgrade.",
			want:   true,
		},
		{
			name:   "extra usage only",
			errMsg: "extra usage limit reached",
			want:   true, // 400 + "limit" matches narrow classifier's keyword set
		},
		{
			name:   "usage without extra",
			errMsg: "usage limit exceeded",
			want:   true, // 400 + "limit" matches narrow classifier's keyword set
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fmt.Errorf("API error: 400 - %s", tt.errMsg)
			got := IsQuotaError(err)
			if got != tt.want {
				t.Errorf("IsQuotaError() = %v, want %v for message %q", got, tt.want, tt.errMsg)
			}
		})
	}
}

// TestIsQuotaOrAuthErrorWithHTTPStatusCodes tests HTTP status code detection
func TestIsQuotaOrAuthErrorWithHTTPStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{name: "401", statusCode: 401, want: false}, // auth propagates
		{name: "403", statusCode: 403, want: false}, // auth propagates
		{name: "400", statusCode: 400, want: false}, // 400 only counts with quota text
		{name: "402", statusCode: 402, want: false},
		{name: "404", statusCode: 404, want: false},
		{name: "429", statusCode: 429, want: false},
		{name: "500", statusCode: 500, want: false},
		{name: "503", statusCode: 503, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fmt.Errorf("API error: %d - error", tt.statusCode)
			got := IsQuotaError(err)
			if got != tt.want {
				t.Errorf("IsQuotaError() = %v, want %v for status %d", got, tt.want, tt.statusCode)
			}
		})
	}
}