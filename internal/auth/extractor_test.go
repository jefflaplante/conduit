package auth

import (
	"net/http/httptest"
	"testing"
)

func TestTokenExtractor_BearerHeader(t *testing.T) {
	extractor := NewTokenExtractor()

	tests := []struct {
		name          string
		authHeader    string
		wantToken     string
		wantSource    TokenSource
		wantMalformed bool
	}{
		{
			name:       "valid bearer token",
			authHeader: "Bearer claw_v1_abc123",
			wantToken:  "claw_v1_abc123",
			wantSource: TokenSourceBearerHeader,
		},
		{
			name:       "bearer with extra whitespace",
			authHeader: "Bearer   claw_v1_abc123  ",
			wantToken:  "claw_v1_abc123",
			wantSource: TokenSourceBearerHeader,
		},
		{
			name:       "lowercase bearer",
			authHeader: "bearer claw_v1_abc123",
			wantToken:  "claw_v1_abc123",
			wantSource: TokenSourceBearerHeader,
		},
		{
			name:       "mixed case bearer",
			authHeader: "BeArEr claw_v1_abc123",
			wantToken:  "claw_v1_abc123",
			wantSource: TokenSourceBearerHeader,
		},
		{
			name:          "bearer with no token",
			authHeader:    "Bearer ",
			wantToken:     "",
			wantSource:    TokenSourceBearerHeader,
			wantMalformed: true,
		},
		{
			name:       "empty header",
			authHeader: "",
			wantSource: TokenSourceNone,
		},
		{
			name:       "non-bearer auth",
			authHeader: "Basic dXNlcjpwYXNz",
			wantSource: TokenSourceNone,
		},
		{
			name:       "bearer too short",
			authHeader: "Bear",
			wantSource: TokenSourceNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			result := extractor.Extract(req)

			if result.Token != tt.wantToken {
				t.Errorf("Token = %q, want %q", result.Token, tt.wantToken)
			}
			if result.Source != tt.wantSource {
				t.Errorf("Source = %v, want %v", result.Source, tt.wantSource)
			}
			if result.IsMalformed != tt.wantMalformed {
				t.Errorf("IsMalformed = %v, want %v", result.IsMalformed, tt.wantMalformed)
			}
		})
	}
}

func TestTokenExtractor_APIKeyHeader(t *testing.T) {
	extractor := NewTokenExtractor()

	tests := []struct {
		name          string
		apiKey        string
		wantToken     string
		wantSource    TokenSource
		wantMalformed bool
	}{
		{
			name:       "valid api key",
			apiKey:     "claw_v1_xyz789",
			wantToken:  "claw_v1_xyz789",
			wantSource: TokenSourceAPIKeyHeader,
		},
		{
			name:       "api key with whitespace",
			apiKey:     "  claw_v1_xyz789  ",
			wantToken:  "claw_v1_xyz789",
			wantSource: TokenSourceAPIKeyHeader,
		},
		{
			name:          "empty api key header",
			apiKey:        "   ",
			wantSource:    TokenSourceAPIKeyHeader,
			wantMalformed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}

			result := extractor.Extract(req)

			if result.Token != tt.wantToken {
				t.Errorf("Token = %q, want %q", result.Token, tt.wantToken)
			}
			if result.Source != tt.wantSource {
				t.Errorf("Source = %v, want %v", result.Source, tt.wantSource)
			}
			if result.IsMalformed != tt.wantMalformed {
				t.Errorf("IsMalformed = %v, want %v", result.IsMalformed, tt.wantMalformed)
			}
		})
	}
}

func TestTokenExtractor_QueryParam(t *testing.T) {
	extractor := NewTokenExtractor()

	tests := []struct {
		name          string
		url           string
		wantToken     string
		wantSource    TokenSource
		wantMalformed bool
	}{
		{
			name:       "valid query token",
			url:        "/test?token=claw_v1_query123",
			wantToken:  "claw_v1_query123",
			wantSource: TokenSourceQueryParam,
		},
		{
			name:       "query token with other params",
			url:        "/test?foo=bar&token=claw_v1_query123&baz=qux",
			wantToken:  "claw_v1_query123",
			wantSource: TokenSourceQueryParam,
		},
		{
			name:       "no token param",
			url:        "/test?foo=bar",
			wantSource: TokenSourceNone,
		},
		{
			name:       "empty token param",
			url:        "/test?token=",
			wantSource: TokenSourceNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.url, nil)

			result := extractor.Extract(req)

			if result.Token != tt.wantToken {
				t.Errorf("Token = %q, want %q", result.Token, tt.wantToken)
			}
			if result.Source != tt.wantSource {
				t.Errorf("Source = %v, want %v", result.Source, tt.wantSource)
			}
			if result.IsMalformed != tt.wantMalformed {
				t.Errorf("IsMalformed = %v, want %v", result.IsMalformed, tt.wantMalformed)
			}
		})
	}
}

func TestTokenExtractor_Priority(t *testing.T) {
	extractor := NewTokenExtractor()

	// Bearer header should take priority over X-API-Key
	t.Run("bearer over api key", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer token_from_bearer")
		req.Header.Set("X-API-Key", "token_from_apikey")

		result := extractor.Extract(req)

		if result.Token != "token_from_bearer" {
			t.Errorf("Token = %q, want token_from_bearer", result.Token)
		}
		if result.Source != TokenSourceBearerHeader {
			t.Errorf("Source = %v, want %v", result.Source, TokenSourceBearerHeader)
		}
	})

	// X-API-Key should take priority over query param
	t.Run("api key over query", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test?token=token_from_query", nil)
		req.Header.Set("X-API-Key", "token_from_apikey")

		result := extractor.Extract(req)

		if result.Token != "token_from_apikey" {
			t.Errorf("Token = %q, want token_from_apikey", result.Token)
		}
		if result.Source != TokenSourceAPIKeyHeader {
			t.Errorf("Source = %v, want %v", result.Source, TokenSourceAPIKeyHeader)
		}
	})

	// All sources, bearer wins
	t.Run("all sources bearer wins", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test?token=token_from_query", nil)
		req.Header.Set("Authorization", "Bearer token_from_bearer")
		req.Header.Set("X-API-Key", "token_from_apikey")

		result := extractor.Extract(req)

		if result.Token != "token_from_bearer" {
			t.Errorf("Token = %q, want token_from_bearer", result.Token)
		}
		if result.Source != TokenSourceBearerHeader {
			t.Errorf("Source = %v, want %v", result.Source, TokenSourceBearerHeader)
		}
	})
}

func TestWebSocketTokenExtractor(t *testing.T) {
	extractor := NewWebSocketTokenExtractor()

	tests := []struct {
		name          string
		protocols     string
		wantToken     string
		wantSource    TokenSource
		wantMalformed bool
	}{
		{
			name:       "valid websocket auth protocol",
			protocols:  "conduit-auth, claw_v1_wstoken123",
			wantToken:  "claw_v1_wstoken123",
			wantSource: TokenSourceWebSocketProtocol,
		},
		{
			name:       "auth protocol without comma space",
			protocols:  "conduit-auth,claw_v1_wstoken123",
			wantToken:  "claw_v1_wstoken123",
			wantSource: TokenSourceWebSocketProtocol,
		},
		{
			name:       "auth protocol with extra protocols",
			protocols:  "some-other-protocol, conduit-auth, claw_v1_wstoken123, another-protocol",
			wantToken:  "claw_v1_wstoken123",
			wantSource: TokenSourceWebSocketProtocol,
		},
		{
			name:          "auth protocol without token",
			protocols:     "conduit-auth",
			wantSource:    TokenSourceWebSocketProtocol,
			wantMalformed: true,
		},
		{
			name:       "no auth protocol",
			protocols:  "graphql-ws, some-other-protocol",
			wantSource: TokenSourceNone,
		},
		{
			name:       "empty protocols",
			protocols:  "",
			wantSource: TokenSourceNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws", nil)
			if tt.protocols != "" {
				req.Header.Set("Sec-WebSocket-Protocol", tt.protocols)
			}

			result := extractor.Extract(req)

			if result.Token != tt.wantToken {
				t.Errorf("Token = %q, want %q", result.Token, tt.wantToken)
			}
			if result.Source != tt.wantSource {
				t.Errorf("Source = %v, want %v", result.Source, tt.wantSource)
			}
			if result.IsMalformed != tt.wantMalformed {
				t.Errorf("IsMalformed = %v, want %v", result.IsMalformed, tt.wantMalformed)
			}
		})
	}
}

func TestWebSocketTokenExtractor_PriorityWithHeaders(t *testing.T) {
	extractor := NewWebSocketTokenExtractor()

	// Bearer header should still take priority over WebSocket protocol
	t.Run("bearer over websocket protocol", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Authorization", "Bearer token_from_bearer")
		req.Header.Set("Sec-WebSocket-Protocol", "conduit-auth, token_from_ws")

		result := extractor.Extract(req)

		if result.Token != "token_from_bearer" {
			t.Errorf("Token = %q, want token_from_bearer", result.Token)
		}
		if result.Source != TokenSourceBearerHeader {
			t.Errorf("Source = %v, want %v", result.Source, TokenSourceBearerHeader)
		}
	})

	// WebSocket protocol should take priority over query param
	t.Run("websocket protocol over query", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/ws?token=token_from_query", nil)
		req.Header.Set("Sec-WebSocket-Protocol", "conduit-auth, token_from_ws")

		result := extractor.Extract(req)

		if result.Token != "token_from_ws" {
			t.Errorf("Token = %q, want token_from_ws", result.Token)
		}
		if result.Source != TokenSourceWebSocketProtocol {
			t.Errorf("Source = %v, want %v", result.Source, TokenSourceWebSocketProtocol)
		}
	})
}

func TestSanitizeTokenForLogging(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected string
	}{
		{
			name:     "empty token",
			token:    "",
			expected: "<empty>",
		},
		{
			name:     "short token",
			token:    "abc",
			expected: "****",
		},
		{
			name:     "normal token",
			token:    "claw_v1_abcdefghijklmnop",
			expected: "claw_v1_****mnop",
		},
		{
			name:     "exactly 12 chars",
			token:    "123456789012",
			expected: "12345678****9012",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeTokenForLogging(tt.token)
			if result != tt.expected {
				t.Errorf("SanitizeTokenForLogging(%q) = %q, want %q", tt.token, result, tt.expected)
			}
		})
	}
}

func TestMaskToken(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected string
	}{
		{
			name:     "empty token",
			token:    "",
			expected: "<none>",
		},
		{
			name:     "any token",
			token:    "claw_v1_secret",
			expected: "<redacted>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskToken(tt.token)
			if result != tt.expected {
				t.Errorf("MaskToken(%q) = %q, want %q", tt.token, result, tt.expected)
			}
		})
	}
}

func TestTokenSource_String(t *testing.T) {
	tests := []struct {
		source TokenSource
		want   string
	}{
		{TokenSourceNone, "none"},
		{TokenSourceBearerHeader, "bearer_header"},
		{TokenSourceAPIKeyHeader, "api_key_header"},
		{TokenSourceQueryParam, "query_param"},
		{TokenSourceWebSocketProtocol, "websocket_protocol"},
		{TokenSource(99), "none"}, // Unknown source
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.source.String(); got != tt.want {
				t.Errorf("TokenSource.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
