package middleware

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

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
			hash_version INTEGER NOT NULL DEFAULT 1,
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

	storage := auth.NewTokenStorage(db, "test-secret")
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

	storage := auth.NewTokenStorage(db, "test-secret")
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

	storage := auth.NewTokenStorage(db, "test-secret")
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

	storage := auth.NewTokenStorage(db, "test-secret")
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
