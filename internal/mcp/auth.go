package mcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// conduit-31jg.8: the MCP endpoint exposes Bash/Write/Message, so it requires
// a bearer token. The token is a dedicated secret (see gateway wiring:
// {data_dir}/auth/mcp_token, 0600) rather than a gateway auth token: those
// are stored only as HMAC hashes, so the gateway could not hand the plaintext
// to its own `claude -p` child, and keeping the code-execution credential
// separate from WebSocket client tokens limits blast radius.

// TokenEnvVar is the environment variable clients reference from .mcp.json
// ("Authorization": "Bearer ${CONDUIT_MCP_TOKEN}"). The gateway exports it
// into its own environment so the claude-code provider's subprocess inherits
// it.
const TokenEnvVar = "CONDUIT_MCP_TOKEN"

// AuthMode is the MCP bearer-token policy.
type AuthMode int

const (
	// AuthDisabled serves every request (mcp.require_auth=false).
	AuthDisabled AuthMode = iota
	// AuthWarn serves requests without a token but logs them; a presented
	// but wrong token is still rejected. Default while clients migrate.
	AuthWarn
	// AuthEnforce rejects requests without a valid token (401).
	AuthEnforce
)

func (m AuthMode) String() string {
	switch m {
	case AuthDisabled:
		return "disabled"
	case AuthWarn:
		return "warn"
	case AuthEnforce:
		return "enforce"
	}
	return "unknown"
}

// ResolveAuthMode maps config mcp.require_auth to a mode: nil => AuthWarn
// (one-release transition), true => AuthEnforce, false => AuthDisabled.
func ResolveAuthMode(requireAuth *bool) AuthMode {
	if requireAuth == nil {
		return AuthWarn
	}
	if *requireAuth {
		return AuthEnforce
	}
	return AuthDisabled
}

// unauthWarnInterval rate-limits the warn-mode log line.
const unauthWarnInterval = time.Minute

// bearerAuth wraps next with the bearer-token check.
type bearerAuth struct {
	mode    AuthMode
	want    [sha256.Size]byte
	haveKey bool
	next    http.Handler

	unauthCount atomic.Int64 // requests served without a token (warn mode)
	rejected    atomic.Int64

	warnMu   sync.Mutex
	lastWarn time.Time
}

func newBearerAuth(mode AuthMode, token string, next http.Handler) *bearerAuth {
	a := &bearerAuth{mode: mode, next: next}
	if token != "" {
		a.want = sha256.Sum256([]byte(token))
		a.haveKey = true
	}
	return a
}

// bearerToken extracts the token from "Authorization: Bearer <token>".
// present is false when there is no Authorization header or the bearer value
// is empty (a client config using "${CONDUIT_MCP_TOKEN:-}" with the variable
// unset sends "Bearer "), so such clients are treated as unauthenticated,
// not as presenting a wrong token.
func bearerToken(r *http.Request) (token string, present bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", false
	}
	scheme, rest, _ := strings.Cut(h, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return h, true // some other scheme: present, and never valid
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

// valid compares in constant time. Hashing first makes the comparison
// independent of the presented token's length.
func (a *bearerAuth) valid(token string) bool {
	if !a.haveKey {
		return false
	}
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], a.want[:]) == 1
}

func (a *bearerAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.mode == AuthDisabled {
		a.next.ServeHTTP(w, r)
		return
	}
	token, present := bearerToken(r)
	switch {
	case present && a.valid(token):
		a.next.ServeHTTP(w, r)
	case present:
		// A client that sends a token means to authenticate: a wrong one is
		// rejected in every mode (e.g. stale token after rotation).
		a.rejected.Add(1)
		log.Printf("[mcp] rejected request with invalid bearer token from %s", r.RemoteAddr)
		a.reject(w, `invalid_token`)
	case a.mode == AuthWarn:
		a.unauthCount.Add(1)
		a.warnUnauthenticated(r)
		a.next.ServeHTTP(w, r)
	default:
		a.rejected.Add(1)
		a.reject(w, "")
	}
}

func (a *bearerAuth) reject(w http.ResponseWriter, errCode string) {
	challenge := `Bearer realm="conduit-mcp"`
	if errCode != "" {
		challenge += `, error="` + errCode + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, "unauthorized: conduit MCP requires Authorization: Bearer <token> (see "+TokenEnvVar+")", http.StatusUnauthorized)
}

func (a *bearerAuth) warnUnauthenticated(r *http.Request) {
	a.warnMu.Lock()
	defer a.warnMu.Unlock()
	now := time.Now()
	if !a.lastWarn.IsZero() && now.Sub(a.lastWarn) < unauthWarnInterval {
		return
	}
	a.lastWarn = now
	log.Printf("[mcp] WARNING: served unauthenticated MCP request from %s (%d so far). mcp.require_auth is unset (warn-only); "+
		"add \"headers\": {\"Authorization\": \"Bearer ${%s}\"} to the client's .mcp.json, then set mcp.require_auth=true",
		r.RemoteAddr, a.unauthCount.Load(), TokenEnvVar)
}
