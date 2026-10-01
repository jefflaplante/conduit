# Authentication Middleware

This package provides HTTP and WebSocket authentication middleware for the Conduit Gateway.

## Overview

The authentication system provides:
- Multi-source token extraction (Bearer header, X-API-Key, query param, WebSocket subprotocol)
- HTTP middleware for protecting REST endpoints
- WebSocket authenticator for WebSocket upgrade requests
- Context propagation for downstream handlers
- Security-hardened error responses

## Components

### Token Extractor (`internal/auth/extractor.go`)

Extracts tokens from multiple sources in priority order:

1. `Authorization: Bearer <token>` header
2. `X-API-Key: <token>` header
3. `?token=<token>` query parameter (WebSocket extractor only; ignored on plain HTTP routes — conduit-31jg.4)
4. WebSocket: `Sec-WebSocket-Protocol: conduit-auth, <token>`

```go
extractor := auth.NewTokenExtractor()
extracted := extractor.Extract(r)
if extracted.Token == "" {
    // No token found
}
```

### HTTP Middleware (`internal/middleware/auth.go`)

Standard HTTP middleware for protecting endpoints:

```go
authMiddleware := middleware.NewAuthMiddleware(storage, middleware.AuthMiddlewareConfig{
    SkipPaths: []string{"/health"},
    OnAuthError: func(r *http.Request, err middleware.AuthError) {
        log.Printf("Auth failed: %v", err)
    },
})

// Wrap handlers
mux.Handle("/api/data", authMiddleware.Wrap(handler))
```

Access auth info in handlers:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    authInfo := middleware.GetAuthInfo(r.Context())
    if authInfo != nil {
        log.Printf("Authenticated as: %s", authInfo.ClientName)
    }
}
```

### WebSocket Authenticator (`internal/middleware/websocket_auth.go`)

Authenticates WebSocket upgrade requests:

```go
wsAuth := middleware.NewWebSocketAuthenticator(storage)

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
    result := wsAuth.Authenticate(r)
    if !result.Authenticated {
        wsAuth.RejectUpgrade(w, result.Error)
        return
    }
    
    // Build response header for protocol negotiation
    var responseHeader http.Header
    if result.ResponseProtocol != "" {
        responseHeader = http.Header{
            "Sec-WebSocket-Protocol": []string{result.ResponseProtocol},
        }
    }
    
    conn, err := upgrader.Upgrade(w, r, responseHeader)
    // ...
}
```

## Security Features

### Error Response Hardening

- **401 Unauthorized**: Missing or malformed token
- **403 Forbidden**: Invalid or expired token
- Generic error messages prevent information leakage
- Token values are never included in responses

### HTTP Status Codes

| Condition | Status Code | Error Response |
|-----------|-------------|----------------|
| No token | 401 | `{"error":"unauthorized","message":"Authentication required"}` |
| Malformed token | 401 | `{"error":"unauthorized","message":"Authentication required"}` |
| Invalid token | 403 | `{"error":"forbidden","message":"Access denied"}` |
| Expired token | 403 | `{"error":"forbidden","message":"Access denied"}` |

### WebSocket Close Codes

| Condition | Close Code | Description |
|-----------|------------|-------------|
| No/malformed token | 4401 | Maps to HTTP 401 |
| Invalid/expired token | 4403 | Maps to HTTP 403 |

### Timing-Safe Validation

Raw tokens are never compared directly: `TokenStorage.ValidateToken` computes an HMAC-SHA256 of the presented token and looks the hash up in the database, so validation time does not depend on how much of a stored token matches.

### Logging Security

- Token values are never logged
- Token sources are logged for debugging without exposing values
- Client names are logged on successful authentication

## Token Sources

### HTTP Headers

```http
Authorization: Bearer conduit_v1_abc123
```

or

```http
X-API-Key: conduit_v1_abc123
```

### Query Parameter

```
/api/data?token=conduit_v1_abc123
```

**Note**: Query parameter auth is less secure (token may appear in logs/history). Prefer headers when possible.

### WebSocket Subprotocol

```http
Sec-WebSocket-Protocol: conduit-auth, conduit_v1_abc123
```

The server echoes back `conduit-auth` to confirm protocol acceptance.

## Context Propagation

Authenticated requests include `AuthInfo` in the context:

```go
type AuthInfo struct {
    TokenID         string            // Unique token ID
    ClientName      string            // Client name from token
    ExpiresAt       *time.Time        // Token expiration
    Metadata        map[string]string // Additional metadata
    Source          TokenSource       // Where token was found
    AuthenticatedAt time.Time         // When request was authenticated
}
```

Retrieve using:

```go
authInfo := middleware.GetAuthInfo(r.Context())
if authInfo != nil {
    // Use authInfo.ClientName, authInfo.TokenID, etc.
}
```

## Integration with Gateway

The gateway automatically integrates auth middleware:

- `/health` - Public (no auth required)
- `/ws` - WebSocket auth via `wsAuthenticator`
- `/api/*` - HTTP auth via `authMiddleware`

## Rate Limiting Integration (OCGO-005)

The middleware is designed for rate limiting integration:

- Auth failures trigger `OnAuthError` callback
- Error responses include placeholder headers for rate limiting
- Token source tracking enables per-source rate limits

## Testing

Run tests:

```bash
go test ./internal/auth/... ./internal/middleware/... -v
```

Key test coverage:
- Token extraction from all sources
- Priority ordering of sources
- Error handling and status codes
- Information leakage prevention
- WebSocket protocol negotiation
- Integration tests with real HTTP/WebSocket servers
