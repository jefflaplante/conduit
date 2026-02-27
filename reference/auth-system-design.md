# Conduit Go Gateway Authentication System Design

## Executive Summary

**Recommendation: API Key approach** with a hybrid path to JWT for future scalability.

The API key approach better meets the immediate requirements for easy revocation, simple setup, and human/AI access patterns, while providing a clear migration path to JWT when distributed features are needed.

## Requirements Analysis

### Functional Requirements
1. **Per-client tokens** - Unique token per client (no shared master key)
2. **CLI management** - Commands for token generation, listing, revocation
3. **Selective protection** - All endpoints except `/health` require auth
4. **Rate limiting** - `/health` gets 100/min regardless of IP
5. **Easy setup** - Simple onboarding for humans and AI clients

### Non-Functional Requirements
- **Security**: Token revocation must be immediate
- **Performance**: Auth check must be <1ms
- **Reliability**: Storage failures shouldn't break existing tokens
- **Maintainability**: Simple codebase, clear debugging

## JWT vs API Key Analysis

### JWT Pros/Cons
✅ **Pros:**
- Stateless validation (no lookup required)
- Self-contained claims (client name, issued time, expiry)
- Standard `golang-jwt/jwt/v5` library
- Built-in expiry handling
- Future-proof for distributed systems

❌ **Cons:**
- **Hard to revoke** (requires blacklist = stateful anyway)
- Token size overhead (~200+ bytes)
- Key rotation complexity
- Claims immutable after issuance

### API Key Pros/Cons
✅ **Pros:**
- **Instant revocation** (delete from storage)
- Simple implementation and debugging
- Small token size (32-64 bytes)
- Flexible metadata storage
- Easy to reason about

❌ **Cons:**
- Requires persistent storage
- Manual expiry handling
- Stateful system

### Decision Matrix

| Criteria | JWT | API Key | Weight | Winner |
|----------|-----|---------|--------|--------|
| Revocation ease | 2/5 | 5/5 | High | **API Key** |
| Implementation simplicity | 3/5 | 5/5 | High | **API Key** |
| Performance | 5/5 | 4/5 | Medium | JWT |
| Future scalability | 5/5 | 3/5 | Low | JWT |
| Setup experience | 3/5 | 5/5 | High | **API Key** |

**Winner: API Key** (better alignment with current needs)

## System Architecture

### Token Format
```
Format: conduit_<version>_<base58(16-byte-random)>
Example: conduit_v1_8KzABCdefghijk123456789
```

- **Prefix**: `conduit_` for easy identification
- **Version**: `v1` for future format evolution  
- **Random**: 16 bytes = 128 bits entropy (base58 encoded)
- **Length**: ~28 characters total

### Storage Schema
```json
{
  "tokens": {
    "conduit_v1_8KzABCdefghijk123456789": {
      "client_name": "jules-main",
      "created_at": "2026-02-07T19:46:00Z",
      "expires_at": "2027-02-07T19:46:00Z",
      "last_used": "2026-02-07T20:15:33Z",
      "permissions": ["websocket", "api"],
      "rate_limit_tier": "standard",
      "metadata": {
        "created_by": "admin",
        "purpose": "AI assistant access"
      }
    }
  },
  "revoked": [
    {
      "token_id": "conduit_v1_oldtoken123456789",
      "revoked_at": "2026-02-07T19:30:00Z",
      "reason": "rotation"
    }
  ],
  "config": {
    "default_expiry_days": 365,
    "max_tokens_per_client": 5
  }
}
```

### File Storage Strategy
- **Primary**: `~/projects/conduit/data/tokens.json`
- **Backup**: Atomic writes with `.tmp` + rename
- **Permissions**: `600` (owner read/write only)
- **Locking**: File locking for concurrent access
- **Migration path**: Interface allows database backend later

## CLI Command Design

### Token Management Commands
```bash
# Generate new token
conduit token create --client-name "jules-main" --expires-in "1y" --permissions "websocket,api"

# List tokens
conduit token list [--client-name filter] [--show-expired]

# Show token details
conduit token show <token_prefix>

# Revoke token
conduit token revoke <token_prefix> --reason "rotation"

# Rotate token (revoke old, create new)
conduit token rotate <token_prefix> --client-name "jules-main"

# Cleanup expired/revoked
conduit token cleanup [--dry-run]

# Export for sharing
conduit token export <token_prefix> [--format env|json|curl]
```

### Example Usage
```bash
# Create token for Jules
$ conduit token create --client-name "jules-main" --expires-in "1y"
Created token for client 'jules-main':
  Token: conduit_v1_8KzABCdefghijk123456789
  Expires: 2027-02-07 19:46:00 UTC
  
# Export for easy setup
$ conduit token export conduit_v1_8KzABCdefghijk123456789 --format env
export CONDUIT_TOKEN="conduit_v1_8KzABCdefghijk123456789"
export CONDUIT_URL="ws://localhost:18890/ws"

# List all tokens
$ conduit token list
CLIENT NAME    TOKEN PREFIX      CREATED              EXPIRES              LAST USED
jules-main     conduit_v1_8KzABC... 2026-02-07 19:46    2027-02-07 19:46    2026-02-07 20:15
human-browser  conduit_v1_9LbDEF... 2026-02-06 10:30    2027-02-06 10:30    never
```

## Authentication Middleware Architecture

### Middleware Stack
```go
// Route setup
router := mux.NewRouter()

// Public endpoints (no auth)
healthRouter := router.PathPrefix("/health").Subrouter()
healthRouter.Use(rateLimitMiddleware(100, time.Minute)) // 100/min rate limit

// Protected endpoints
protectedRouter := router.PathPrefix("").Subrouter()
protectedRouter.Use(authMiddleware)
protectedRouter.Use(rateLimitMiddleware(1000, time.Minute)) // 1000/min for authed

// Register handlers
protectedRouter.HandleFunc("/ws", websocketHandler)
protectedRouter.HandleFunc("/api/channels/status", channelsStatusHandler)
protectedRouter.HandleFunc("/api/test/message", testMessageHandler)
```

### Authentication Flow
```go
func authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Extract token from multiple sources
        token := extractToken(r) // Header, Query, WebSocket subprotocol
        
        if token == "" {
            http.Error(w, "Missing authentication token", 401)
            return
        }
        
        // Validate token format
        if !isValidTokenFormat(token) {
            http.Error(w, "Invalid token format", 401)
            return
        }
        
        // Lookup token in storage
        tokenInfo, err := tokenStore.ValidateToken(token)
        if err != nil {
            if errors.Is(err, ErrTokenNotFound) || errors.Is(err, ErrTokenExpired) {
                http.Error(w, "Invalid or expired token", 401)
            } else {
                http.Error(w, "Authentication error", 500)
            }
            return
        }
        
        // Update last_used (async to avoid latency)
        go tokenStore.UpdateLastUsed(token, time.Now())
        
        // Add context for downstream handlers
        ctx := context.WithValue(r.Context(), "client_name", tokenInfo.ClientName)
        ctx = context.WithValue(ctx, "permissions", tokenInfo.Permissions)
        
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### Token Extraction
```go
func extractToken(r *http.Request) string {
    // 1. Authorization header: Bearer <token>
    if auth := r.Header.Get("Authorization"); auth != "" {
        if strings.HasPrefix(auth, "Bearer ") {
            return strings.TrimPrefix(auth, "Bearer ")
        }
    }
    
    // 2. X-API-Key header
    if token := r.Header.Get("X-API-Key"); token != "" {
        return token
    }
    
    // 3. Query parameter (for WebSocket initial auth)
    if token := r.URL.Query().Get("token"); token != "" {
        return token
    }
    
    // 4. WebSocket subprotocol (RFC compliant)
    if websocket.IsWebSocketUpgrade(r) {
        for _, proto := range r.Header["Sec-WebSocket-Protocol"] {
            if strings.HasPrefix(proto, "conduit.auth.") {
                return strings.TrimPrefix(proto, "conduit.auth.")
            }
        }
    }
    
    return ""
}
```

## Rate Limiting Implementation

### Strategy: In-Memory + Token Bucket
```go
type RateLimiter struct {
    buckets map[string]*tokenBucket
    mutex   sync.RWMutex
    cleanup chan struct{}
}

type tokenBucket struct {
    tokens    int
    maxTokens int
    refill    time.Duration
    lastFill  time.Time
}

func rateLimitMiddleware(limit int, window time.Duration) func(http.Handler) http.Handler {
    limiter := NewRateLimiter(limit, window)
    
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            var key string
            
            // For /health: use IP address
            if strings.HasPrefix(r.URL.Path, "/health") {
                key = "ip:" + getClientIP(r)
            } else {
                // For authenticated endpoints: use client_name from context
                if clientName, ok := r.Context().Value("client_name").(string); ok {
                    key = "client:" + clientName
                } else {
                    key = "ip:" + getClientIP(r)
                }
            }
            
            if !limiter.Allow(key) {
                w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
                w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(window).Unix(), 10))
                http.Error(w, "Rate limit exceeded", 429)
                return
            }
            
            next.ServeHTTP(w, r)
        })
    }
}
```

## Configuration Management

### Config File Structure
```json
{
  "auth": {
    "token_file": "data/tokens.json",
    "default_expiry_days": 365,
    "max_tokens_per_client": 5,
    "require_auth": true
  },
  "rate_limits": {
    "health_endpoint": {
      "requests_per_minute": 100,
      "by": "ip"
    },
    "authenticated_endpoints": {
      "requests_per_minute": 1000,
      "by": "client"
    }
  },
  "server": {
    "host": "localhost",
    "port": 18890
  }
}
```

### Environment Override
```bash
# Disable auth for development
CONDUIT_AUTH_REQUIRE_AUTH=false

# Custom token file location
CONDUIT_AUTH_TOKEN_FILE=/custom/path/tokens.json

# Rate limit adjustments
CONDUIT_RATELIMIT_HEALTH_RPM=200
```

## Easy Setup Flow

### For AI Clients (Jules)
```bash
# 1. Generate token
conduit token create --client-name "jules-main" --expires-in "1y"

# 2. Export configuration
conduit token export conduit_v1_8KzABCdefghijk123456789 --format env > jules.env

# 3. Add to secrets
echo "export CONDUIT_TOKEN=conduit_v1_8KzABCdefghijk123456789" >> ~/.conduit-secrets.env
echo "export CONDUIT_URL=ws://localhost:18890/ws" >> ~/.conduit-secrets.env
```

### For Humans (Browser/Postman)
```bash
# 1. Generate token with longer name
conduit token create --client-name "my-browser" --expires-in "1y"

# 2. Export as curl examples
conduit token export conduit_v1_9LbDEF123456789 --format curl
# Outputs:
# WebSocket: wscat -c "ws://localhost:18890/ws" -H "Authorization: Bearer conduit_v1_9LbDEF123456789"
# API: curl -H "Authorization: Bearer conduit_v1_9LbDEF123456789" http://localhost:18890/api/channels/status
```

### For Development
```bash
# Disable auth entirely
export CONDUIT_AUTH_REQUIRE_AUTH=false
conduit serve

# Or use dev token
conduit token create --client-name "dev" --expires-in "1d"
```

## Security Considerations

### Token Generation
- **Entropy**: 16 bytes (128 bits) using crypto/rand
- **Format**: Base58 encoding (no confusing characters)
- **Prefix**: `conduit_v1_` for identification and versioning

### Storage Security
- **File permissions**: 600 (owner read/write only)
- **Atomic writes**: Prevent corruption during concurrent access
- **No plaintext logs**: Tokens never logged in full (only prefix)

### Revocation
- **Immediate**: Delete from storage = immediate revocation
- **Audit trail**: Keep revoked tokens with reason and timestamp
- **Cleanup**: Remove old revoked tokens after 90 days

### Network Security
- **HTTPS recommended**: Though not enforced for localhost
- **WebSocket subprotocol**: RFC-compliant token passing
- **Header flexibility**: Multiple auth methods for different clients

## Implementation Plan

### Phase 1: Core Token System (Week 1)
1. ✅ Token generation and validation logic
2. ✅ File-based storage with atomic operations
3. ✅ CLI commands for basic token management
4. ✅ Authentication middleware
5. ✅ Basic rate limiting

### Phase 2: Advanced Features (Week 2)
1. ✅ WebSocket authentication support
2. ✅ Token export/import functionality
3. ✅ Enhanced rate limiting with per-client tracking
4. ✅ Configuration management system
5. ✅ Comprehensive error handling

### Phase 3: Production Hardening (Week 3)
1. ✅ Security audit and testing
2. ✅ Performance optimization
3. ✅ Monitoring and metrics
4. ✅ Documentation and examples
5. ✅ Migration tooling

### Phase 4: Future Enhancements
1. Database backend option (SQLite/Postgres)
2. JWT hybrid mode for distributed scenarios
3. Web UI for token management
4. Integration with external auth providers
5. Advanced permission/scope system

## Migration Path to JWT

When distributed features are needed:

1. **Hybrid Mode**: Support both API keys and JWT
2. **JWT Claims**: Map API key metadata to JWT claims
3. **Signing Keys**: Implement key rotation system
4. **Blacklist**: Add JWT blacklist for revocation
5. **Gradual Migration**: Migrate clients one by one

Example JWT claims structure:
```json
{
  "iss": "conduit-gateway",
  "sub": "jules-main",
  "iat": 1738966800,
  "exp": 1770502800,
  "permissions": ["websocket", "api"],
  "rate_limit_tier": "standard"
}
```

## Testing Strategy

### Unit Tests
- Token generation and validation
- Storage operations (CRUD)
- Rate limiting algorithms
- Authentication middleware

### Integration Tests
- End-to-end authentication flow
- WebSocket authentication
- CLI command functionality
- Concurrent access handling

### Security Tests
- Token brute force resistance
- Rate limit bypass attempts
- File permission verification
- Injection attack prevention

## Conclusion

The API key approach provides the optimal balance of simplicity, security, and functionality for Conduit Go gateway's current needs. The design includes:

- **Immediate revocation** for security incidents
- **Simple CLI interface** for easy management
- **Flexible authentication** supporting multiple client types
- **Clear migration path** to JWT when distributed features are needed
- **Production-ready** security and performance characteristics

This system can be implemented incrementally and provides a solid foundation for future authentication enhancements.