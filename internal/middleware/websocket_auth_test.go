package middleware

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/auth"
)

// setupWSTestDB creates an in-memory SQLite database with the auth_tokens table
func setupWSTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "ws_auth_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		t.Fatalf("Failed to open database: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE auth_tokens (
			token_id TEXT PRIMARY KEY,
			client_name TEXT NOT NULL,
			hashed_token TEXT UNIQUE NOT NULL,
			created_at DATETIME NOT NULL,
			expires_at DATETIME,
			last_used_at DATETIME,
			is_active BOOLEAN DEFAULT 1,
			metadata TEXT DEFAULT '{}'
		)
	`)
	if err != nil {
		db.Close()
		os.Remove(tmpPath)
		t.Fatalf("Failed to create table: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.Remove(tmpPath)
	}

	return db, cleanup
}

// createWSTestToken creates a token in the test database
func createWSTestToken(t *testing.T, storage *auth.TokenStorage, clientName string, expiresAt *time.Time) string {
	t.Helper()

	resp, err := storage.CreateToken(auth.CreateTokenRequest{
		ClientName: clientName,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	return resp.Token
}

func TestWebSocketAuthenticator_ValidToken(t *testing.T) {
	db, cleanup := setupWSTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	token := createWSTestToken(t, storage, "ws-client", nil)

	authenticator := NewWebSocketAuthenticator(storage)

	tests := []struct {
		name         string
		setupRequest func(*http.Request)
		wantAuth     bool
		wantProtocol string
	}{
		{
			name: "bearer header",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+token)
			},
			wantAuth:     true,
			wantProtocol: "",
		},
		{
			name: "api key header",
			setupRequest: func(r *http.Request) {
				r.Header.Set("X-API-Key", token)
			},
			wantAuth:     true,
			wantProtocol: "",
		},
		{
			name: "websocket protocol",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Sec-WebSocket-Protocol", "conduit-auth, "+token)
			},
			wantAuth:     true,
			wantProtocol: "conduit-auth",
		},
		{
			name: "query parameter",
			setupRequest: func(r *http.Request) {
				r.URL.RawQuery = "token=" + token
			},
			wantAuth:     true,
			wantProtocol: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws", nil)
			tt.setupRequest(req)

			result := authenticator.Authenticate(req)

			if result.Authenticated != tt.wantAuth {
				t.Errorf("Authenticated = %v, want %v", result.Authenticated, tt.wantAuth)
			}
			if result.ResponseProtocol != tt.wantProtocol {
				t.Errorf("ResponseProtocol = %q, want %q", result.ResponseProtocol, tt.wantProtocol)
			}
			if tt.wantAuth && result.AuthInfo == nil {
				t.Error("Expected AuthInfo to be set")
			}
			if tt.wantAuth && result.AuthInfo.ClientName != "ws-client" {
				t.Errorf("ClientName = %q, want %q", result.AuthInfo.ClientName, "ws-client")
			}
		})
	}
}

func TestWebSocketAuthenticator_InvalidToken(t *testing.T) {
	db, cleanup := setupWSTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	authenticator := NewWebSocketAuthenticator(storage)

	tests := []struct {
		name         string
		setupRequest func(*http.Request)
		wantCode     int
	}{
		{
			name: "missing token",
			setupRequest: func(r *http.Request) {
				// No auth headers
			},
			wantCode: http.StatusUnauthorized,
		},
		{
			name: "invalid token",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer invalid_token")
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "malformed bearer",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer ")
			},
			wantCode: http.StatusUnauthorized,
		},
		{
			name: "malformed websocket protocol",
			setupRequest: func(r *http.Request) {
				r.Header.Set("Sec-WebSocket-Protocol", "conduit-auth")
			},
			wantCode: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws", nil)
			tt.setupRequest(req)

			result := authenticator.Authenticate(req)

			if result.Authenticated {
				t.Error("Expected authentication to fail")
			}
			if result.Error == nil {
				t.Error("Expected error to be set")
			}
			if result.Error.Code != tt.wantCode {
				t.Errorf("Error.Code = %d, want %d", result.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestWebSocketAuthenticator_ExpiredToken(t *testing.T) {
	db, cleanup := setupWSTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	pastTime := time.Now().Add(-1 * time.Hour)
	token := createWSTestToken(t, storage, "expired-ws-client", &pastTime)

	authenticator := NewWebSocketAuthenticator(storage)

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	result := authenticator.Authenticate(req)

	if result.Authenticated {
		t.Error("Expected authentication to fail for expired token")
	}
	if result.Error.Code != http.StatusForbidden {
		t.Errorf("Error.Code = %d, want %d", result.Error.Code, http.StatusForbidden)
	}
}

func TestWebSocketAuthenticator_RejectUpgrade(t *testing.T) {
	db, cleanup := setupWSTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	authenticator := NewWebSocketAuthenticator(storage)

	rec := httptest.NewRecorder()
	authenticator.RejectUpgrade(rec, &ErrMissingToken)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	if h := rec.Header().Get("WWW-Authenticate"); h == "" {
		t.Error("Expected WWW-Authenticate header")
	}
}

func TestGetRequestedProtocols(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   []string
	}{
		{
			name:   "empty",
			header: "",
			want:   nil,
		},
		{
			name:   "single protocol",
			header: "graphql-ws",
			want:   []string{"graphql-ws"},
		},
		{
			name:   "multiple protocols",
			header: "conduit-auth, token123, graphql-ws",
			want:   []string{"conduit-auth", "token123", "graphql-ws"},
		},
		{
			name:   "with extra whitespace",
			header: "  conduit-auth  ,  token123  ",
			want:   []string{"conduit-auth", "token123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws", nil)
			if tt.header != "" {
				req.Header.Set("Sec-WebSocket-Protocol", tt.header)
			}

			got := GetRequestedProtocols(req)

			if len(got) != len(tt.want) {
				t.Errorf("len = %d, want %d", len(got), len(tt.want))
				return
			}

			for i, p := range got {
				if p != tt.want[i] {
					t.Errorf("protocols[%d] = %q, want %q", i, p, tt.want[i])
				}
			}
		})
	}
}

func TestHasAuthProtocol(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{
			name:   "no protocols",
			header: "",
			want:   false,
		},
		{
			name:   "has auth protocol",
			header: "conduit-auth, token123",
			want:   true,
		},
		{
			name:   "no auth protocol",
			header: "graphql-ws, other-protocol",
			want:   false,
		},
		{
			name:   "auth protocol only",
			header: "conduit-auth",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws", nil)
			if tt.header != "" {
				req.Header.Set("Sec-WebSocket-Protocol", tt.header)
			}

			got := HasAuthProtocol(req)
			if got != tt.want {
				t.Errorf("HasAuthProtocol() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthenticationError(t *testing.T) {
	err := &AuthenticationError{AuthError: ErrInvalidToken}

	if err.Error() != "Access denied" {
		t.Errorf("Error() = %q, want %q", err.Error(), "Access denied")
	}
}

func TestWebSocketCloseCodes(t *testing.T) {
	// Verify close codes are in the 4000-4999 private use range
	if CloseUnauthorized < 4000 || CloseUnauthorized > 4999 {
		t.Errorf("CloseUnauthorized = %d, should be in 4000-4999 range", CloseUnauthorized)
	}
	if CloseForbidden < 4000 || CloseForbidden > 4999 {
		t.Errorf("CloseForbidden = %d, should be in 4000-4999 range", CloseForbidden)
	}

	// Verify they map to HTTP codes
	if CloseUnauthorized%1000 != 401 {
		t.Errorf("CloseUnauthorized = %d, should end in 401", CloseUnauthorized)
	}
	if CloseForbidden%1000 != 403 {
		t.Errorf("CloseForbidden = %d, should end in 403", CloseForbidden)
	}
}

// Integration test with actual WebSocket upgrade
func TestAuthenticatedUpgrader_Integration(t *testing.T) {
	db, cleanup := setupWSTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	token := createWSTestToken(t, storage, "ws-integration-client", nil)

	upgrader := &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	authUpgrader := NewAuthenticatedUpgrader(storage, upgrader)

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, authInfo, err := authUpgrader.UpgradeWithAuth(w, r)
		if err != nil {
			return
		}
		defer conn.Close()

		// Send auth info back to verify it works
		conn.WriteMessage(websocket.TextMessage, []byte("client:"+authInfo.ClientName))
	}))
	defer server.Close()

	t.Run("valid token via header", func(t *testing.T) {
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
		header := http.Header{}
		header.Set("Authorization", "Bearer "+token)

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Failed to read message: %v", err)
		}

		if string(msg) != "client:ws-integration-client" {
			t.Errorf("Message = %q, want %q", string(msg), "client:ws-integration-client")
		}
	})

	t.Run("valid token via subprotocol", func(t *testing.T) {
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

		// Use Dialer.Subprotocols to set the subprotocols (don't set header manually)
		dialer := websocket.Dialer{
			Subprotocols: []string{"conduit-auth", token},
		}

		conn, resp, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		// Check that server echoed back the auth protocol
		negotiatedProtocol := resp.Header.Get("Sec-WebSocket-Protocol")
		if negotiatedProtocol != "conduit-auth" {
			t.Errorf("Negotiated protocol = %q, want %q", negotiatedProtocol, "conduit-auth")
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Failed to read message: %v", err)
		}

		if string(msg) != "client:ws-integration-client" {
			t.Errorf("Message = %q, want %q", string(msg), "client:ws-integration-client")
		}
	})

	t.Run("invalid token rejected", func(t *testing.T) {
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
		header := http.Header{}
		header.Set("Authorization", "Bearer invalid_token")

		_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err == nil {
			t.Error("Expected connection to fail")
			return
		}

		// Check that we got a 403 response
		if resp != nil && resp.StatusCode != http.StatusForbidden {
			t.Errorf("Status = %d, want %d", resp.StatusCode, http.StatusForbidden)
		}
	})

	t.Run("missing token rejected", func(t *testing.T) {
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

		_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err == nil {
			t.Error("Expected connection to fail")
			return
		}

		// Check that we got a 401 response
		if resp != nil && resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
	})
}
