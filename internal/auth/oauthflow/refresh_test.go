package oauthflow

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		err := r.ParseForm()
		require.NoError(t, err)
		assert.Equal(t, "refresh_token", r.FormValue("grant_type"))
		assert.Equal(t, "old-refresh-token", r.FormValue("refresh_token"))
		assert.Equal(t, "test-client", r.FormValue("client_id"))

		resp := tokenResponse{
			AccessToken:  "sk-ant-oat01-new-access",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "new-refresh-token",
			Scope:        "user:inference",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Override token URL via env var.
	t.Setenv("ANTHROPIC_OAUTH_TOKEN_URL", server.URL)

	token, err := RefreshToken("old-refresh-token", "test-client")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-new-access", token.AccessToken)
	assert.Equal(t, "new-refresh-token", token.RefreshToken)
	assert.Equal(t, "Bearer", token.TokenType)
	assert.Equal(t, "test-client", token.ClientID)
	assert.Greater(t, token.ExpiresAt, int64(0))
}

func TestRefreshToken_KeepsOldRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			AccessToken: "sk-ant-oat01-new-access",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			// No new refresh token issued.
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_OAUTH_TOKEN_URL", server.URL)

	token, err := RefreshToken("keep-this-refresh", "test-client")
	require.NoError(t, err)
	assert.Equal(t, "keep-this-refresh", token.RefreshToken)
}

func TestRefreshToken_NoRefreshToken(t *testing.T) {
	_, err := RefreshToken("", "test-client")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token")
}

func TestRefreshToken_NoClientID(t *testing.T) {
	// Clear any env var.
	t.Setenv("ANTHROPIC_OAUTH_CLIENT_ID", "")

	_, err := RefreshToken("some-refresh-token", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client ID is required")
}

func TestRefreshToken_ServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token expired"}`))
	}))
	defer server.Close()

	t.Setenv("ANTHROPIC_OAUTH_TOKEN_URL", server.URL)

	_, err := RefreshToken("expired-refresh", "test-client")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh request failed")
	// Should have retried once.
	assert.Equal(t, 2, attempts)
}
