package oauthflow

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// RefreshToken uses the refresh_token grant to obtain a new access token.
// It updates the token store on disk if successful. The clientID should match
// the one used during the original login.
func RefreshToken(refreshToken, clientID string) (*StoredToken, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	endpoints := DefaultEndpoints()
	if clientID == "" {
		clientID = endpoints.ClientID
	}
	if clientID == "" {
		return nil, fmt.Errorf("client ID is required for token refresh")
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tokenResp, err := postTokenRequest(ctx, endpoints.TokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}

	stored := &StoredToken{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix(),
		Scope:        tokenResp.Scope,
		ObtainedAt:   time.Now().Unix(),
		ClientID:     clientID,
	}

	// Keep the old refresh token if the server didn't issue a new one.
	if stored.RefreshToken == "" {
		stored.RefreshToken = refreshToken
	}

	return stored, nil
}
