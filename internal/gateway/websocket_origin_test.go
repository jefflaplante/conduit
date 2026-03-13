package gateway

import (
	"net/http"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		name           string
		allowedOrigins []string
		origin         string
		expected       bool
	}{
		// Default policy (no allowed origins configured)
		{
			name:           "no origin header accepted",
			allowedOrigins: nil,
			origin:         "",
			expected:       true,
		},
		{
			name:           "localhost http accepted by default",
			allowedOrigins: nil,
			origin:         "http://localhost",
			expected:       true,
		},
		{
			name:           "localhost with port accepted by default",
			allowedOrigins: nil,
			origin:         "http://localhost:3000",
			expected:       true,
		},
		{
			name:           "localhost https accepted by default",
			allowedOrigins: nil,
			origin:         "https://localhost",
			expected:       true,
		},
		{
			name:           "127.0.0.1 accepted by default",
			allowedOrigins: nil,
			origin:         "http://127.0.0.1:8080",
			expected:       true,
		},
		{
			name:           "ipv6 loopback accepted by default",
			allowedOrigins: nil,
			origin:         "http://[::1]:8080",
			expected:       true,
		},
		{
			name:           "external origin rejected by default",
			allowedOrigins: nil,
			origin:         "https://evil.example.com",
			expected:       false,
		},
		{
			name:           "arbitrary origin rejected by default",
			allowedOrigins: nil,
			origin:         "https://myapp.com",
			expected:       false,
		},
		// With explicit allowed origins
		{
			name:           "allowed origin accepted",
			allowedOrigins: []string{"https://myapp.com"},
			origin:         "https://myapp.com",
			expected:       true,
		},
		{
			name:           "allowed origin case insensitive",
			allowedOrigins: []string{"https://MyApp.com"},
			origin:         "https://myapp.com",
			expected:       true,
		},
		{
			name:           "disallowed origin rejected",
			allowedOrigins: []string{"https://myapp.com"},
			origin:         "https://evil.example.com",
			expected:       false,
		},
		{
			name:           "no origin with allowlist accepted",
			allowedOrigins: []string{"https://myapp.com"},
			origin:         "",
			expected:       true,
		},
		{
			name:           "multiple allowed origins",
			allowedOrigins: []string{"https://app1.com", "https://app2.com"},
			origin:         "https://app2.com",
			expected:       true,
		},
		{
			name:           "localhost rejected when not in explicit allowlist",
			allowedOrigins: []string{"https://myapp.com"},
			origin:         "http://localhost:3000",
			expected:       false,
		},
		// Empty allowed origins slice (same as nil)
		{
			name:           "empty slice acts as default policy",
			allowedOrigins: []string{},
			origin:         "https://evil.example.com",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := checkOrigin(tt.allowedOrigins)
			req, err := http.NewRequest("GET", "/ws", nil)
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			result := fn(req)
			if result != tt.expected {
				t.Errorf("checkOrigin(%v) with Origin=%q: got %v, want %v",
					tt.allowedOrigins, tt.origin, result, tt.expected)
			}
		})
	}
}
