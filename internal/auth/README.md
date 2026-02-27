# Authentication Token Storage

This package provides database storage for authentication tokens used in the Conduit Gateway.

## Features

- **Secure Token Storage**: Tokens are hashed using SHA-256 before storage
- **Token Metadata**: Flexible JSON metadata field for additional token information
- **Token Expiration**: Optional expiration dates with automatic cleanup
- **Client Management**: Organize tokens by client name
- **Token Revocation**: Soft deletion via `is_active` flag
- **Activity Tracking**: Track last usage timestamps

## Database Schema

The `auth_tokens` table is automatically created via database migrations:

```sql
CREATE TABLE auth_tokens (
    token_id TEXT PRIMARY KEY,
    client_name TEXT NOT NULL,
    hashed_token TEXT NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME,
    last_used_at DATETIME,
    is_active BOOLEAN DEFAULT 1,
    metadata TEXT DEFAULT '{}'
);
```

## Usage

### Creating a Token Storage Instance

```go
import (
    "database/sql"
    "conduit/internal/auth"
    "conduit/internal/database"
    _ "modernc.org/sqlite"
)

// Open database
db, err := sql.Open("sqlite", "gateway.db")
if err != nil {
    return err
}

// Configure database and run migrations
if err := database.ConfigureDatabase(db); err != nil {
    return err
}

// Create token storage
storage := auth.NewTokenStorage(db)
```

### Creating Authentication Tokens

```go
// Create a token for a client
req := auth.CreateTokenRequest{
    ClientName: "my-client-app",
    ExpiresAt:  &time.Time{}, // Optional expiration
    Metadata: map[string]string{
        "description": "Production API access",
        "environment": "prod",
        "created_by":  "admin@example.com",
    },
}

resp, err := storage.CreateToken(req)
if err != nil {
    return err
}

// resp.Token contains the raw token (only shown once!)
// resp.TokenInfo contains public token information
fmt.Printf("Token: %s\n", resp.Token)
fmt.Printf("Token ID: %s\n", resp.TokenInfo.TokenID)
```

### Validating Tokens

```go
// Validate a token (updates last_used_at)
tokenInfo, err := storage.ValidateToken("claw_v1_abc123...")
if err != nil {
    // Token is invalid, expired, or revoked
    return fmt.Errorf("invalid token: %w", err)
}

// Token is valid - proceed with request
fmt.Printf("Authenticated as: %s\n", tokenInfo.ClientName)
```

### Managing Tokens

```go
// List all tokens for a client
tokens, err := storage.ListTokens("my-client-app", false) // active only
if err != nil {
    return err
}

// Get token details
tokenInfo, err := storage.GetTokenInfo(tokenID)
if err != nil {
    return err
}

// Revoke a token (soft delete)
if err := storage.RevokeToken(tokenID); err != nil {
    return err
}

// Permanently delete a token
if err := storage.DeleteToken(tokenID); err != nil {
    return err
}

// Update token metadata
newMetadata := map[string]string{
    "description": "Updated description",
    "last_updated": time.Now().String(),
}
if err := storage.UpdateTokenMetadata(tokenID, newMetadata); err != nil {
    return err
}
```

### Cleanup Operations

```go
// Clean up expired tokens
deletedCount, err := storage.CleanupExpiredTokens()
if err != nil {
    return err
}

fmt.Printf("Cleaned up %d expired tokens\n", deletedCount)
```

## Token Format

Generated tokens have the format: `conduit_v1_<base58-random>` (new) or `claw_v1_<base58-random>` (legacy)

- **Prefix**: `conduit_v1_` (new) or `claw_v1_` (legacy) for easy identification
- **Random Data**: 32 bytes (256 bits) of cryptographically secure random data
- **Encoding**: Hexadecimal encoding
- **Total Length**: 72 characters

## Security Considerations

1. **Token Hashing**: Raw tokens are never stored; only SHA-256 hashes are kept in the database
2. **One-time Display**: Raw tokens are only returned during creation and should be stored securely by the client
3. **Secure Generation**: Tokens use `crypto/rand` for cryptographically secure randomness
4. **Database Constraints**: Unique constraints prevent duplicate token hashes
5. **Activity Tracking**: Last usage timestamps help identify unused tokens

## Migration

This package uses the centralized migration system in `internal/database`. The auth tokens table is created automatically when the gateway starts up.

Migration version 2 creates the auth tokens table and related indexes.

## Performance

The following indexes are created for optimal query performance:

- `idx_auth_tokens_client_name`: For filtering by client
- `idx_auth_tokens_expires_at`: For expiration queries
- `idx_auth_tokens_hashed_token`: For token validation (unique constraint)
- `idx_auth_tokens_active`: For filtering active/inactive tokens

## Integration with Gateway

To integrate with the gateway's authentication middleware:

1. Create a `TokenStorage` instance using the same database connection
2. Use `ValidateToken()` in your authentication middleware
3. Extract client information from the returned `TokenInfo`
4. Use token metadata for additional authorization logic

Example middleware:

```go
func authMiddleware(storage *auth.TokenStorage) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Extract token from Authorization header
            authHeader := r.Header.Get("Authorization")
            if !strings.HasPrefix(authHeader, "Bearer ") {
                http.Error(w, "Missing or invalid authorization", 401)
                return
            }
            
            token := strings.TrimPrefix(authHeader, "Bearer ")
            
            // Validate token
            tokenInfo, err := storage.ValidateToken(token)
            if err != nil {
                http.Error(w, "Invalid token", 401)
                return
            }
            
            // Add client info to context
            ctx := context.WithValue(r.Context(), "client_name", tokenInfo.ClientName)
            ctx = context.WithValue(ctx, "token_id", tokenInfo.TokenID)
            
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```