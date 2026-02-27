package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"conduit/internal/auth"
)

// setupTestDB creates an in-memory SQLite database with the auth_tokens table
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	// Create a temp file for the database (in-memory doesn't work well with modernc/sqlite)
	tmpFile, err := os.CreateTemp("", "auth_test_*.db")
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

	// Create table
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

// createTestToken creates a token in the test database and returns the raw token
func createTestToken(t *testing.T, storage *auth.TokenStorage, clientName string, expiresAt *time.Time) string {
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

func TestAuthMiddleware_ValidToken(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	token := createTestToken(t, storage, "test-client", nil)

	// Create middleware
	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})

	// Create test handler that checks for auth info
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authInfo := GetAuthInfo(r.Context())
		if authInfo == nil {
			t.Error("Expected auth info in context, got nil")
			http.Error(w, "No auth info", http.StatusInternalServerError)
			return
		}

		if authInfo.ClientName != "test-client" {
			t.Errorf("ClientName = %q, want %q", authInfo.ClientName, "test-client")
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))

	// Test request with valid token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called when token is missing")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// Check response body
	var response map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["error"] != "unauthorized" {
		t.Errorf("error = %q, want %q", response["error"], "unauthorized")
	}

	// Check WWW-Authenticate header
	if h := rec.Header().Get("WWW-Authenticate"); h == "" {
		t.Error("Expected WWW-Authenticate header")
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called when token is invalid")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid_token_that_does_not_exist")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	var response map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["error"] != "forbidden" {
		t.Errorf("error = %q, want %q", response["error"], "forbidden")
	}
}

func TestAuthMiddleware_ExpiredToken(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	// Create token that expired in the past
	pastTime := time.Now().Add(-1 * time.Hour)
	token := createTestToken(t, storage, "expired-client", &pastTime)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called when token is expired")
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthMiddleware_SkipPaths(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{
		SkipPaths: []string{"/health", "/public"},
	})

	handlerCalled := false
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantCalled bool
	}{
		{
			name:       "health endpoint skipped",
			path:       "/health",
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "public endpoint skipped",
			path:       "/public",
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "protected endpoint blocked",
			path:       "/api/data",
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlerCalled = false

			req := httptest.NewRequest("GET", tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("Status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if handlerCalled != tt.wantCalled {
				t.Errorf("handlerCalled = %v, want %v", handlerCalled, tt.wantCalled)
			}
		})
	}
}

func TestAuthMiddleware_OnAuthError(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	var capturedError AuthError
	var capturedRequest *http.Request

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{
		OnAuthError: func(r *http.Request, err AuthError) {
			capturedError = err
			capturedRequest = r
		},
	})

	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if capturedRequest == nil {
		t.Error("OnAuthError was not called")
	}
	if capturedError.Code != http.StatusUnauthorized {
		t.Errorf("OnAuthError code = %d, want %d", capturedError.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_APIKeyHeader(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	token := createTestToken(t, storage, "api-client", nil)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authInfo := GetAuthInfo(r.Context())
		if authInfo == nil {
			t.Error("Expected auth info in context")
			return
		}
		if authInfo.Source != auth.TokenSourceAPIKeyHeader {
			t.Errorf("Source = %v, want %v", authInfo.Source, auth.TokenSourceAPIKeyHeader)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-API-Key", token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_QueryParam(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	token := createTestToken(t, storage, "query-client", nil)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authInfo := GetAuthInfo(r.Context())
		if authInfo == nil {
			t.Error("Expected auth info in context")
			return
		}
		if authInfo.Source != auth.TokenSourceQueryParam {
			t.Errorf("Source = %v, want %v", authInfo.Source, auth.TokenSourceQueryParam)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test?token="+token, nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestGetAuthInfo_NoAuth(t *testing.T) {
	ctx := context.Background()
	info := GetAuthInfo(ctx)
	if info != nil {
		t.Errorf("GetAuthInfo on empty context = %v, want nil", info)
	}
}

func TestIsAuthenticated(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{
			name: "no auth",
			ctx:  context.Background(),
			want: false,
		},
		{
			name: "with auth",
			ctx:  context.WithValue(context.Background(), AuthContextKey, &AuthInfo{ClientName: "test"}),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAuthenticated(tt.ctx); got != tt.want {
				t.Errorf("IsAuthenticated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequireAuth(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	token := createTestToken(t, storage, "test-client", nil)

	handler := RequireAuth(storage, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("with valid token", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("without token", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
}

func TestRequireAuthFunc(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)
	token := createTestToken(t, storage, "test-client", nil)

	handler := RequireAuthFunc(storage, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_NoInformationLeakage(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	tests := []struct {
		name  string
		token string
	}{
		{
			name:  "invalid token",
			token: "invalid_token_12345",
		},
		{
			name:  "sql injection attempt",
			token: "'; DROP TABLE auth_tokens; --",
		},
		{
			name:  "very long token",
			token: string(make([]byte, 10000)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			// Should return 403 for all invalid tokens
			if rec.Code != http.StatusForbidden {
				t.Errorf("Status = %d, want %d", rec.Code, http.StatusForbidden)
			}

			// Check that response doesn't contain the token
			body, _ := io.ReadAll(rec.Body)
			bodyStr := string(body)

			// Response should only contain generic message
			var response map[string]string
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatalf("Failed to parse response: %v", err)
			}

			// Should use generic "Access denied" message
			if response["message"] != "Access denied" {
				t.Errorf("message = %q, want generic 'Access denied'", response["message"])
			}

			// Response should not contain token details
			if len(tt.token) > 10 && contains(bodyStr, tt.token[:10]) {
				t.Error("Response contains part of the token - information leakage!")
			}
		})
	}
}

func TestAuthMiddleware_MalformedBearerToken(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer ") // Bearer with no token
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Malformed token should return 401, not 403
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_SecurityHeaders(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	storage := auth.NewTokenStorage(db)

	middleware := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Check security headers are set
	if h := rec.Header().Get("X-Content-Type-Options"); h != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", h, "nosniff")
	}

	if h := rec.Header().Get("Content-Type"); h != "application/json" {
		t.Errorf("Content-Type = %q, want %q", h, "application/json")
	}
}
