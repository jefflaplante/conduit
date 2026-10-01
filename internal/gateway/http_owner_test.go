package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/auth"
	"conduit/internal/middleware"
)

// conduit-25lt.1: endpoints exposing the owner's data refuse automation-role
// tokens; monitoring and the scripts' test-message endpoint stay open.
func TestOwnerOnlyEndpoints(t *testing.T) {
	gw, _ := createTestGatewayWithDiagnosticsConfig(t, true, nil)
	gw.search = &SearchService{}
	store := auth.NewTokenStorage(gw.sessions.DB(), "test-secret")
	bot, err := store.CreateToken(auth.CreateTokenRequest{
		ClientName: "bot", Metadata: map[string]string{auth.MetadataKeyRole: auth.RoleAutomation},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.buildHTTPServer().Handler)
	defer srv.Close()

	do := func(method, path, token string) int {
		t.Helper()
		var body *strings.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"message":"hi","query":"x"}`)
		} else {
			body = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, srv.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	ownerOnly := []struct{ method, path string }{
		{http.MethodGet, "/diagnostics"},
		{http.MethodGet, "/debug/prompt"},
		{http.MethodGet, "/api/brain/graph"},
		{http.MethodPost, "/api/vector/search"},
		{http.MethodPost, "/api/vector/index"},
		{http.MethodPost, "/api/vector/delete"},
		{http.MethodGet, "/api/vector/status"},
	}
	for _, e := range ownerOnly {
		if got := do(e.method, e.path, bot.Token); got != http.StatusForbidden {
			t.Errorf("automation %s %s = %d, want 403", e.method, e.path, got)
		}
	}

	open := []struct{ method, path string }{
		{http.MethodGet, "/metrics"},
		{http.MethodGet, "/prometheus"},
		{http.MethodPost, "/api/test/message"},
	}
	for _, e := range open {
		if got := do(e.method, e.path, bot.Token); got == http.StatusForbidden || got == http.StatusUnauthorized {
			t.Errorf("automation %s %s = %d, want access", e.method, e.path, got)
		}
	}
}

// Owner and untagged tokens (default owner) pass; a request the auth
// middleware let through without auth info (a configured public path) is
// left to that decision.
func TestRequireOwnerRole(t *testing.T) {
	gw := &Gateway{logger: newTestLogger()}
	h := gw.requireOwnerRole(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, tt := range []struct {
		name string
		info *middleware.AuthInfo
		want int
	}{
		{"owner", &middleware.AuthInfo{ClientName: "me", Metadata: map[string]string{"role": "owner"}}, http.StatusTeapot},
		{"untagged", &middleware.AuthInfo{ClientName: "old"}, http.StatusTeapot},
		{"automation", &middleware.AuthInfo{ClientName: "bot", Metadata: map[string]string{"role": "automation"}}, http.StatusForbidden},
		{"public path", nil, http.StatusTeapot},
	} {
		req := httptest.NewRequest(http.MethodGet, "/debug/prompt", nil)
		if tt.info != nil {
			req = req.WithContext(context.WithValue(req.Context(), middleware.AuthContextKey, tt.info))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, rec.Code, tt.want)
		}
	}
}
