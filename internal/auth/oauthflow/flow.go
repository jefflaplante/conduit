package oauthflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// FlowOptions configures the authorization flow.
type FlowOptions struct {
	ClientID      string
	PreferredPort int
	NoBrowser     bool
	Endpoints     OAuthEndpoints
}

// tokenResponse is the JSON structure returned by the token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// RunLogin performs the full Authorization Code + PKCE flow.
// It starts a temporary local server, opens the browser, waits for the callback,
// exchanges the code for tokens, and stores them.
func RunLogin(ctx context.Context, opts FlowOptions) (*StoredToken, error) {
	endpoints := opts.Endpoints
	clientID := opts.ClientID
	if clientID == "" {
		clientID = endpoints.ClientID
	}
	if clientID == "" {
		return nil, fmt.Errorf("client ID is required: set --client-id flag, ANTHROPIC_OAUTH_CLIENT_ID env var, or auth.oauth_endpoints.client_id in config")
	}

	// 1. Generate PKCE parameters.
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}
	challenge := computeCodeChallenge(verifier)
	state, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	// 2. Start callback server.
	server, err := newCallbackServer(opts.PreferredPort)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	server.Start()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx) //nolint:errcheck
	}()

	redirectURI := server.RedirectURI()

	// 3. Build authorization URL.
	authURL := buildAuthorizeURL(endpoints.AuthorizeURL, clientID, challenge, state, redirectURI, endpoints.Scopes)

	// 4. Open browser or print URL.
	if opts.NoBrowser {
		fmt.Println("Open this URL in your browser to authenticate:")
		fmt.Println()
		fmt.Println("  " + authURL)
		fmt.Println()
	} else {
		fmt.Printf("Opening browser for authentication (callback on port %d)...\n", server.Port())
		if err := openBrowser(authURL); err != nil {
			fmt.Println("Could not open browser automatically. Open this URL manually:")
			fmt.Println()
			fmt.Println("  " + authURL)
			fmt.Println()
		}
	}

	fmt.Println("Waiting for authentication...")

	// 5. Wait for callback with timeout.
	callbackCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	code, callbackState, err := server.WaitForCallback(callbackCtx)
	if err != nil {
		return nil, err
	}

	// 6. Verify state.
	if callbackState != state {
		return nil, fmt.Errorf("state mismatch: possible CSRF attack (expected %s, got %s)", state, callbackState)
	}

	// 7. Exchange code for tokens.
	token, err := exchangeCode(ctx, endpoints.TokenURL, clientID, code, verifier, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	// 8. Build stored token.
	stored := &StoredToken{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		ExpiresAt:    time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix(),
		Scope:        token.Scope,
		ObtainedAt:   time.Now().Unix(),
		ClientID:     clientID,
	}

	// 9. Save to disk.
	if err := SaveProviderToken("anthropic", stored); err != nil {
		return nil, fmt.Errorf("failed to save token: %w", err)
	}

	return stored, nil
}

// generateCodeVerifier creates a random 64-byte base64url-encoded string (86 chars)
// per RFC 7636 (43-128 characters).
func generateCodeVerifier() (string, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// computeCodeChallenge returns SHA256(verifier) base64url-encoded per RFC 7636 S256.
func computeCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// generateState creates a random 32-byte base64url-encoded state parameter.
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// buildAuthorizeURL constructs the authorization URL with query parameters.
func buildAuthorizeURL(baseURL, clientID, challenge, state, redirectURI, scopes string) string {
	params := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"state":                 {state},
	}
	return baseURL + "?" + params.Encode()
}

// exchangeCode exchanges the authorization code for tokens via POST.
func exchangeCode(ctx context.Context, tokenURL, clientID, code, verifier, redirectURI string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}

	return postTokenRequest(ctx, tokenURL, data)
}

// postTokenRequest sends a POST to the token endpoint and parses the response.
// Retries once on transient failure.
func postTokenRequest(ctx context.Context, tokenURL string, data url.Values) (*tokenResponse, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			log.Printf("Retrying token request (attempt %d)...", attempt+1)
			time.Sleep(time.Second)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
			continue
		}

		var tokenResp tokenResponse
		if err := json.Unmarshal(body, &tokenResp); err != nil {
			return nil, fmt.Errorf("failed to parse token response: %w", err)
		}

		if tokenResp.Error != "" {
			return nil, fmt.Errorf("token error: %s — %s", tokenResp.Error, tokenResp.ErrorDesc)
		}

		return &tokenResp, nil
	}

	return nil, lastErr
}

// openBrowser opens a URL in the user's default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
