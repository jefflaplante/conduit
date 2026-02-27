package oauthflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateCodeVerifier(t *testing.T) {
	v, err := generateCodeVerifier()
	require.NoError(t, err)
	// 64 bytes → 86 base64url characters (no padding).
	assert.Len(t, v, 86)

	// Each call should produce a different value.
	v2, err := generateCodeVerifier()
	require.NoError(t, err)
	assert.NotEqual(t, v, v2)
}

func TestComputeCodeChallenge(t *testing.T) {
	// RFC 7636 Appendix B test vector.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := computeCodeChallenge(verifier)
	assert.Equal(t, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", challenge)
}

func TestGenerateState(t *testing.T) {
	s, err := generateState()
	require.NoError(t, err)
	assert.NotEmpty(t, s)
	// 32 bytes → 43 base64url characters.
	assert.Len(t, s, 43)

	s2, err := generateState()
	require.NoError(t, err)
	assert.NotEqual(t, s, s2)
}

func TestBuildAuthorizeURL(t *testing.T) {
	url := buildAuthorizeURL(
		"https://example.com/authorize",
		"test-client",
		"test-challenge",
		"test-state",
		"http://localhost:8085/callback",
		"user:inference",
	)

	assert.Contains(t, url, "https://example.com/authorize?")
	assert.Contains(t, url, "client_id=test-client")
	assert.Contains(t, url, "response_type=code")
	assert.Contains(t, url, "code_challenge=test-challenge")
	assert.Contains(t, url, "code_challenge_method=S256")
	assert.Contains(t, url, "redirect_uri=http")
	assert.Contains(t, url, "scope=user%3Ainference")
	assert.Contains(t, url, "state=test-state")
}

func TestExchangeCode_Success(t *testing.T) {
	// Mock token endpoint.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		err := r.ParseForm()
		require.NoError(t, err)
		assert.Equal(t, "authorization_code", r.FormValue("grant_type"))
		assert.Equal(t, "test-code", r.FormValue("code"))
		assert.Equal(t, "test-client", r.FormValue("client_id"))
		assert.Equal(t, "test-verifier", r.FormValue("code_verifier"))

		resp := tokenResponse{
			AccessToken:  "sk-ant-oat01-test-access",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "anthro-rt-test-refresh",
			Scope:        "user:inference",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	token, err := exchangeCode(
		context.Background(),
		server.URL,
		"test-client",
		"test-code",
		"test-verifier",
		"http://localhost:8085/callback",
	)

	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-test-access", token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
	assert.Equal(t, 3600, token.ExpiresIn)
	assert.Equal(t, "anthro-rt-test-refresh", token.RefreshToken)
}

func TestExchangeCode_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			Error:     "invalid_grant",
			ErrorDesc: "Authorization code expired",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	_, err := exchangeCode(context.Background(), server.URL, "c", "code", "v", "http://localhost/cb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
}

func TestExchangeCode_HTTPError_RetriesOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
			return
		}
		resp := tokenResponse{
			AccessToken: "sk-ant-oat01-retry-ok",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	token, err := exchangeCode(context.Background(), server.URL, "c", "code", "v", "http://localhost/cb")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-retry-ok", token.AccessToken)
	assert.Equal(t, 2, attempts)
}

func TestCallbackServer(t *testing.T) {
	server, err := newCallbackServer(0) // OS-assigned port.
	require.NoError(t, err)
	server.Start()
	defer server.Shutdown(context.Background()) //nolint:errcheck

	assert.Greater(t, server.Port(), 0)
	assert.Contains(t, server.RedirectURI(), "http://localhost:")

	// Simulate browser callback.
	callbackURL := server.RedirectURI() + "?code=test-code&state=test-state"
	resp, err := http.Get(callbackURL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Should receive the result.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	code, state, err := server.WaitForCallback(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test-code", code)
	assert.Equal(t, "test-state", state)
}

func TestCallbackServer_Error(t *testing.T) {
	server, err := newCallbackServer(0)
	require.NoError(t, err)
	server.Start()
	defer server.Shutdown(context.Background()) //nolint:errcheck

	callbackURL := server.RedirectURI() + "?error=access_denied&error_description=user+cancelled"
	resp, err := http.Get(callbackURL)
	require.NoError(t, err)
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err = server.WaitForCallback(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access_denied")
}

func TestCallbackServer_Timeout(t *testing.T) {
	server, err := newCallbackServer(0)
	require.NoError(t, err)
	server.Start()
	defer server.Shutdown(context.Background()) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err = server.WaitForCallback(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}
