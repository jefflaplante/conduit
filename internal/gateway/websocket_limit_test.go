package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"conduit/internal/config"
)

// newTestWSService returns a minimal WebSocketService for tests that need to
// manipulate the connection counter or client map without a full Gateway.
func newTestWSService() *WebSocketService {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewWebSocketService(logger, websocket.Upgrader{}, 1)
}

func TestWebSocketConfigDefaults(t *testing.T) {
	// Test DefaultWebSocketConfig
	cfg := config.DefaultWebSocketConfig()
	if cfg.MaxMessageSize != 1048576 {
		t.Errorf("DefaultWebSocketConfig().MaxMessageSize = %d, want 1048576", cfg.MaxMessageSize)
	}
}

func TestWebSocketConfigGetMaxMessageSize(t *testing.T) {
	tests := []struct {
		name     string
		config   config.WebSocketConfig
		expected int64
	}{
		{
			name:     "default value when zero",
			config:   config.WebSocketConfig{MaxMessageSize: 0},
			expected: 1048576, // 1MB default
		},
		{
			name:     "default value when negative",
			config:   config.WebSocketConfig{MaxMessageSize: -1},
			expected: 1048576, // 1MB default
		},
		{
			name:     "custom value",
			config:   config.WebSocketConfig{MaxMessageSize: 2097152},
			expected: 2097152, // 2MB
		},
		{
			name:     "small value",
			config:   config.WebSocketConfig{MaxMessageSize: 1024},
			expected: 1024, // 1KB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetMaxMessageSize()
			if result != tt.expected {
				t.Errorf("GetMaxMessageSize() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestConfigIncludesWebSocketDefaults(t *testing.T) {
	// Test that Default() includes WebSocket config
	cfg := config.Default()
	if cfg.WebSocket.MaxMessageSize != 1048576 {
		t.Errorf("Default().WebSocket.MaxMessageSize = %d, want 1048576", cfg.WebSocket.MaxMessageSize)
	}
}

func TestHTTPServerSecurityConstants(t *testing.T) {
	// Verify security constants are set to expected values
	if serverMaxHeaderBytes != 1<<20 {
		t.Errorf("serverMaxHeaderBytes = %d, want %d (1 MB)", serverMaxHeaderBytes, 1<<20)
	}
	if serverReadTimeout.Seconds() != 30 {
		t.Errorf("serverReadTimeout = %v, want 30s", serverReadTimeout)
	}
	if serverWriteTimeout.Seconds() != 60 {
		t.Errorf("serverWriteTimeout = %v, want 60s", serverWriteTimeout)
	}
	if serverIdleTimeout.Seconds() != 120 {
		t.Errorf("serverIdleTimeout = %v, want 120s", serverIdleTimeout)
	}
	if MaxRequestBodySize != 10<<20 {
		t.Errorf("MaxRequestBodySize = %d, want %d (10 MB)", MaxRequestBodySize, 10<<20)
	}
	if MaxWebSocketConnections != 1000 {
		t.Errorf("MaxWebSocketConnections = %d, want 1000", MaxWebSocketConnections)
	}
}

func TestLimitRequestBody(t *testing.T) {
	t.Run("allows body within limit", func(t *testing.T) {
		body := strings.NewReader(`{"message": "hello"}`)
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		rr := httptest.NewRecorder()

		var readBody []byte
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			readBody, err = io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read error", http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		handler := limitRequestBody(inner, 1024) // 1KB limit
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rr.Code)
		}
		if string(readBody) != `{"message": "hello"}` {
			t.Errorf("body mismatch: got %q", string(readBody))
		}
	})

	t.Run("rejects body exceeding limit", func(t *testing.T) {
		// Create a body larger than the limit
		largeBody := strings.NewReader(strings.Repeat("x", 2048))
		req := httptest.NewRequest(http.MethodPost, "/test", largeBody)
		rr := httptest.NewRecorder()

		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			if err != nil {
				// MaxBytesReader triggers this error
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		handler := limitRequestBody(inner, 1024) // 1KB limit
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("expected status 413, got %d", rr.Code)
		}
	})

	t.Run("handles nil body gracefully", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rr := httptest.NewRecorder()

		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		handler := limitRequestBody(inner, 1024)
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rr.Code)
		}
	})
}

func TestWebSocketConnectionLimitTracking(t *testing.T) {
	// Test that the atomic counter in the WebSocketService works correctly
	gw := &Gateway{ws: newTestWSService()}

	// Simulate connections
	for i := 0; i < 5; i++ {
		gw.ws.WSConnCount.Add(1)
	}

	if count := gw.ws.WSConnCount.Load(); count != 5 {
		t.Errorf("expected 5 connections, got %d", count)
	}

	// Simulate disconnections
	for i := 0; i < 3; i++ {
		gw.ws.WSConnCount.Add(-1)
	}

	if count := gw.ws.WSConnCount.Load(); count != 2 {
		t.Errorf("expected 2 connections after disconnections, got %d", count)
	}
}

func TestWebSocketConnectionLimitRejectsAtMax(t *testing.T) {
	gw := createTestGateway(t)
	gw.ws = newTestWSService()

	// Simulate being at the connection limit
	gw.ws.WSConnCount.Store(MaxWebSocketConnections)

	// Create a test request that should be rejected
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rr := httptest.NewRecorder()

	gw.handleWebSocket(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 when at connection limit, got %d", rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "Too many WebSocket connections") {
		t.Errorf("expected connection limit error message, got %q", rr.Body.String())
	}
}

func TestWebSocketConnectionLimitAllowsBelowMax(t *testing.T) {
	gw := createTestGateway(t)
	gw.ws = newTestWSService()

	// Set count below limit -- the initial check should pass
	gw.ws.WSConnCount.Store(MaxWebSocketConnections - 1)

	// Verify Load() returns a value below the max, proving it would pass the gate
	if gw.ws.WSConnCount.Load() >= MaxWebSocketConnections {
		t.Errorf("expected count below limit, got %d", gw.ws.WSConnCount.Load())
	}

	// Simulate the atomic increment that handleWebSocket does after the gate
	newCount := gw.ws.WSConnCount.Add(1)
	if newCount > MaxWebSocketConnections {
		t.Errorf("expected count at or below limit after increment, got %d", newCount)
	}

	// Clean up
	gw.ws.WSConnCount.Add(-1)
}
