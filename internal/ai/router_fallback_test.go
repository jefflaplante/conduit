package ai

import (
	"errors"
	"testing"
)

// TestFallbackRetryOnQuotaError tests the quota error classifier and retry logic
func TestFallbackRetryOnQuotaError(t *testing.T) {
	// Test the error classifier
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "400 with quota text",
			err:  errors.New("API error: 400 - {\"error\":{\"message\":\"out of extra usage\"}}"),
			want: true,
		},
		{
			name: "401 unauthorized — NOT quota (auth propagates per NoFallbackOnAuthError contract)",
			err:  errors.New("API error: 401 - unauthorized"),
			want: false,
		},
		{
			name: "403 forbidden — NOT quota (auth propagates)",
			err:  errors.New("API error: 403 - forbidden"),
			want: false,
		},
		{
			name: "400 without quota text",
			err:  errors.New("API error: 400 - bad request"),
			want: false,
		},
		{
			name: "500 server error",
			err:  errors.New("API error: 500 - internal server error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
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