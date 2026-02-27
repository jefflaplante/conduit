package oauthflow

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// callbackResult holds the result from the OAuth callback.
type callbackResult struct {
	Code  string
	State string
	Err   error
}

// callbackServer runs a temporary localhost HTTP server to receive the OAuth callback.
type callbackServer struct {
	listener net.Listener
	server   *http.Server
	result   chan callbackResult
	once     sync.Once
}

// newCallbackServer creates a callback server. It tries the preferred port first,
// then falls back through a range, and finally to an OS-assigned port.
func newCallbackServer(preferredPort int) (*callbackServer, error) {
	var listener net.Listener
	var err error

	// Try preferred port first.
	if preferredPort > 0 {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	}

	// If preferred port failed, try a range.
	if listener == nil {
		for port := 8085; port <= 8095; port++ {
			if port == preferredPort {
				continue // Already tried.
			}
			listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				break
			}
		}
	}

	// Last resort: OS-assigned port.
	if listener == nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("failed to find available port: %w", err)
		}
	}

	cs := &callbackServer{
		listener: listener,
		result:   make(chan callbackResult, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)

	cs.server = &http.Server{Handler: mux}

	return cs, nil
}

// Port returns the port the server is listening on.
func (cs *callbackServer) Port() int {
	return cs.listener.Addr().(*net.TCPAddr).Port
}

// RedirectURI returns the full redirect URI including the port.
func (cs *callbackServer) RedirectURI() string {
	return fmt.Sprintf("http://localhost:%d/callback", cs.Port())
}

// Start begins serving in a goroutine and returns immediately.
func (cs *callbackServer) Start() {
	go cs.server.Serve(cs.listener) //nolint:errcheck
}

// WaitForCallback blocks until the callback is received or the context is cancelled.
func (cs *callbackServer) WaitForCallback(ctx context.Context) (code, state string, err error) {
	select {
	case r := <-cs.result:
		return r.Code, r.State, r.Err
	case <-ctx.Done():
		return "", "", fmt.Errorf("timed out waiting for OAuth callback")
	}
}

// Shutdown gracefully shuts down the server.
func (cs *callbackServer) Shutdown(ctx context.Context) error {
	return cs.server.Shutdown(ctx)
}

// handleCallback processes the OAuth redirect.
func (cs *callbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	cs.once.Do(func() {
		q := r.URL.Query()

		if errParam := q.Get("error"); errParam != "" {
			desc := q.Get("error_description")
			cs.result <- callbackResult{Err: fmt.Errorf("OAuth error: %s — %s", errParam, desc)}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<html><body><h2>Authentication Failed</h2><p>%s: %s</p><p>You can close this tab.</p></body></html>", errParam, desc)
			return
		}

		code := q.Get("code")
		state := q.Get("state")

		if code == "" {
			cs.result <- callbackResult{Err: fmt.Errorf("no authorization code in callback")}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h2>Authentication Failed</h2><p>No authorization code received.</p><p>You can close this tab.</p></body></html>")
			return
		}

		cs.result <- callbackResult{Code: code, State: state}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>Authentication Successful</h2><p>You can close this tab and return to the terminal.</p></body></html>")
	})
}
