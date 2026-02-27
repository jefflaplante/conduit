# Conduit Gateway Security Guide

This guide covers security considerations, best practices, and deployment checklist for the Conduit Go Gateway authentication system.

## Table of Contents

1. [Security Architecture](#security-architecture)
2. [Token Management](#token-management)
3. [Rate Limiting & DoS Prevention](#rate-limiting--dos-prevention)
4. [Information Leakage Prevention](#information-leakage-prevention)
5. [Database Security](#database-security)
6. [Logging & Monitoring](#logging--monitoring)
7. [Configuration Security](#configuration-security)
8. [Deployment Checklist](#deployment-checklist)
9. [Incident Response](#incident-response)

## Security Architecture

### Authentication Flow

The authentication system uses a token-based approach with the following security properties:

1. **Token Generation**
   - Uses `crypto/rand` for cryptographically secure random bytes
   - 256-bit tokens (32 bytes) for strong entropy
   - Prefix `conduit_` for easy identification

2. **Token Storage**
   - Tokens are **never stored in plaintext**
   - Only SHA256 hashes are persisted in the database
   - Hashes use constant-time comparison to prevent timing attacks
   - Token IDs are UUIDs for anonymization

3. **Token Validation**
   - Incoming tokens are hashed with SHA256
   - Hash is compared against stored hashes
   - Timing-safe comparison prevents timing attacks
   - Response doesn't reveal whether token exists or is invalid

4. **Expiration Enforcement**
   - Expiration times are checked on every validation
   - Expired tokens are rejected immediately
   - No background cleanup needed (lazy evaluation)

### Threat Model

We protect against:

| Threat | Mitigation |
|--------|-----------|
| Token interception | HTTPS required in production, store in secrets |
| Token replay | Tokens are stateless; IP restriction optional |
| Brute force guessing | 256-bit entropy; 2^256 possible tokens |
| Timing attacks | Constant-time hash comparison |
| Database compromise | Only hashes stored; raw token is unrecoverable |
| Token leakage in logs | Tokens masked in error messages |
| Expired token use | Checked on validation; immediate rejection |
| DoS attacks | Rate limiting per IP and per client |

## Token Management

### Token Lifecycle

#### Creation Phase

```bash
conduit token create --client-name "my-app" --expires-in "90d"
```

**What happens:**
1. 256-bit random token generated
2. SHA256 hash computed and stored in database
3. Raw token returned once (never retrievable again)
4. Token ID (UUID) associated with client name

**Best practices:**
- Create tokens with reasonable expiration (30-90 days for prod, 1 year for dev)
- Add metadata to track purpose and owner
- Store returned token immediately in secrets management

#### Active Phase

**Valid requests with token:**
```bash
curl -H "Authorization: Bearer conduit_abc123..." http://localhost:18890/api/...
```

**What happens:**
1. Token extracted from request
2. Hash computed from provided token
3. Database queried for matching hash
4. Expiration checked
5. `last_used_at` timestamp updated

**Security properties:**
- No plaintext token passes through logs
- Hash comparison prevents token visibility
- Expiration prevents indefinite token use
- `last_used_at` helps identify abandoned tokens

#### Rotation Phase

```bash
# Create new token
conduit token create --client-name "my-app"

# Update application with new token

# Revoke old token
conduit token revoke <old-token-id>
```

**Best practices:**
- Rotate tokens at least every 6 months
- Rotate immediately if compromised
- Maintain one-week overlap during rotation
- Monitor token usage to ensure rotation was successful

#### Revocation Phase

```bash
conduit token revoke <token-id>
```

**What happens:**
1. Token marked as inactive in database
2. Any request with that token is rejected
3. Action is immediate and irreversible
4. No cleanup or async tasks needed

**Emergency revocation:**
```sql
-- Direct database access if CLI unavailable
UPDATE auth_tokens SET is_active = 0 WHERE token_id = 'uuid-here';
```

### Token Storage Checklist

- [ ] Tokens stored in environment variables or secrets manager
- [ ] Never committed to version control
- [ ] Never logged or printed to stdout
- [ ] File permissions restricted (644 or better)
- [ ] In production, use secrets manager (e.g., HashiCorp Vault, AWS Secrets Manager)
- [ ] Tokens rotated every 90 days or sooner
- [ ] Old tokens revoked after rotation overlap period

## Rate Limiting & DoS Prevention

### Rate Limit Configuration

The gateway implements two-tier rate limiting:

```json
{
  "ratelimit": {
    "enabled": true,
    "anonymous": {
      "windowSeconds": 60,
      "maxRequests": 100
    },
    "authenticated": {
      "windowSeconds": 60,
      "maxRequests": 1000
    },
    "cleanupIntervalSeconds": 300
  }
}
```

**Behavior:**
- Anonymous (no token): 100 req/min per IP
- Authenticated (with token): 1000 req/min per client token
- Sliding window (not fixed window) prevents time-boundary bursts

### Rate Limit Mechanics

1. **Identification**
   - Anonymous: Client IP address
   - Authenticated: Token/client identifier

2. **Tracking**
   - Requests tracked in memory using sliding window
   - Window size: 60 seconds (configurable)
   - Per-identifier counters maintained

3. **Enforcement**
   - Requests exceeding limit rejected with 429 status
   - `Retry-After` header indicates wait time
   - Remaining quota in `X-RateLimit-Remaining` header

4. **Cleanup**
   - Old window data purged periodically
   - Memory-bounded: windows older than `cleanupIntervalSeconds` removed
   - No unbounded memory growth

### Rate Limit Bypass Prevention

**Potential bypasses and mitigations:**

| Attack | Mitigation |
|--------|-----------|
| Distributed IPs | Track per-token not IP for auth requests |
| Token enumeration | Same 429 response regardless of token validity |
| Slow requests | Timeout + rate limiting apply to request count, not duration |
| WebSocket keepalive | Each message counts toward limit |

### DoS Protection Recommendations

1. **Configure appropriately for your load:**
   ```bash
   # For high-traffic apps
   authenticated.maxRequests = 5000  # per minute
   anonymous.maxRequests = 500       # per minute per IP
   ```

2. **Monitor rate limiting:**
   ```bash
   # Check logs for rate limit hits
   journalctl -u conduit | grep "Rate limit exceeded"
   ```

3. **Implement client-side backoff:**
   ```python
   # Clients should respect Retry-After header
   retry_after = int(response.headers["Retry-After"])
   time.sleep(retry_after)
   ```

4. **Use reverse proxy limits:**
   ```nginx
   limit_req_zone $binary_remote_addr zone=per_ip:10m rate=100r/m;
   limit_req zone=per_ip burst=10;
   ```

5. **Monitor for attack patterns:**
   - Track 429 responses per IP
   - Alert on sudden spikes in rate limit hits
   - Block IPs with sustained attacks

## Information Leakage Prevention

### Error Messages

All authentication errors return generic messages:

```json
// Token missing
{"error": "unauthorized", "message": "Authentication required"}

// Token invalid/expired
{"error": "forbidden", "message": "Access denied"}

// Rate limited
{"error": "rate_limit_exceeded", "message": "Rate limit exceeded", "retry_after": 45}
```

**Why generic messages?**
- Prevent attackers from enumerating tokens
- Don't reveal whether token exists or is expired
- Prevent timing attacks based on error messages

### Logging

**What IS logged:**
- Request path and method
- Client name (authenticated requests only)
- Token source (header, parameter, etc.)
- Rate limit violations
- Request timing metrics

**What IS NOT logged:**
- Raw tokens or token hashes
- Token details in error paths
- Query parameters (which may contain tokens)
- Request/response bodies

**Example safe log:**
```
[Auth] Request authenticated: client=my-app path=/api/channels/status source=bearer_header
[RateLimit] Rate limit exceeded: GET /api/test (identifier: my-app, type: authenticated_client)
[WS Auth] Connection authenticated: client=my-app source=websocket_protocol
```

### Header Handling

The gateway strips sensitive headers from logs:

- `Authorization` header values not logged
- `X-API-Key` header values not logged
- Only header names appear in debug logs

### Database Query Logs

If database query logging is enabled:

- Token hashes ARE logged (safe, not reversible)
- Avoid logging full query results
- Redact any identifiable information

## Database Security

### SQLite Configuration

```sql
-- Enable secure defaults
PRAGMA foreign_keys = ON;        -- Enforce referential integrity
PRAGMA journal_mode = WAL;       -- Write-ahead logging for crash recovery
PRAGMA synchronous = FULL;       -- Ensure durability
```

### Table Schema

```sql
CREATE TABLE auth_tokens (
    token_id TEXT PRIMARY KEY,
    client_name TEXT NOT NULL,
    hashed_token TEXT UNIQUE NOT NULL,    -- SHA256, not reversible
    created_at DATETIME NOT NULL,
    expires_at DATETIME,                   -- NULL = no expiration
    last_used_at DATETIME,
    is_active BOOLEAN DEFAULT 1,           -- Soft delete
    metadata TEXT DEFAULT '{}'             -- JSON metadata
);

-- Index for fast lookups
CREATE INDEX idx_auth_tokens_hashed_token ON auth_tokens(hashed_token);
CREATE INDEX idx_auth_tokens_client_name ON auth_tokens(client_name);
```

### Security Properties

1. **No plaintext tokens**
   - Only SHA256 hashes stored
   - Hashes are not reversible
   - Database compromise doesn't leak tokens

2. **Referential integrity**
   - Foreign keys enabled
   - Data consistency enforced

3. **Audit trail**
   - `created_at` timestamp
   - `last_used_at` for usage tracking
   - Soft deletes via `is_active` flag

4. **Metadata constraints**
   - Arbitrary key-value pairs
   - Stored as JSON
   - Searchable and queryable

### File Permissions

```bash
# Database file permissions
-rw-r--r-- 1 conduit developers gateway.db  # Readable by process only

# Ensure proper ownership
sudo chown conduit:developers /path/to/gateway.db
sudo chmod 644 /path/to/gateway.db

# Directory permissions
drwxr-xr-x conduit developers /path/to/data/dir
```

### Backup Security

When backing up the database:

1. **Encrypt backups:**
   ```bash
   gpg --encrypt --recipient your-key@example.com gateway.db
   ```

2. **Restrict access:**
   ```bash
   chmod 600 gateway.db.gpg
   ```

3. **Verify backups:**
   ```bash
   # Test restore process
   gpg --decrypt gateway.db.gpg > /tmp/test.db
   sqlite3 /tmp/test.db "SELECT COUNT(*) FROM auth_tokens;"
   rm /tmp/test.db
   ```

4. **Retention policy:**
   - Keep backups for 30 days minimum
   - Delete older backups securely (shred, not rm)
   - Store off-site copies

## Logging & Monitoring

### Log Levels

Configure logging appropriately:

```json
{
  "logging": {
    "level": "info"  // debug, info, warn, error
  }
}
```

**For production:** Use `info` level
**For troubleshooting:** Use `debug` level temporarily

### Log Destinations

```bash
# View gateway logs
journalctl -u conduit -f

# Log file (if configured)
tail -f /var/log/conduit/gateway.log

# Structured logging (JSON)
journalctl -u conduit -o json | jq .
```

### Metrics to Monitor

1. **Authentication metrics:**
   ```
   - Successful authentications per minute
   - Failed authentications per minute (should be low)
   - Token creation rate
   - Token revocation rate
   ```

2. **Rate limiting metrics:**
   ```
   - 429 responses per minute
   - Rate limit hits by identifier
   - Max requests approaching limit threshold
   ```

3. **Performance metrics:**
   ```
   - Auth middleware latency (p50, p95, p99)
   - Token validation cache hit rate
   - Database query time
   ```

4. **Security metrics:**
   ```
   - Invalid token attempts (should match failed auth)
   - Expired token attempts
   - Anonymous vs authenticated request ratio
   ```

### Alert Thresholds

Set up alerts for:

- `Failed authentications > 10/min` - Possible attack
- `Rate limit hits > 100/min from single IP` - Potential DoS
- `Token creation > 10/hour` - Unusual pattern
- `Auth middleware latency p95 > 100ms` - Performance degradation

## Configuration Security

### Secure Defaults

The gateway comes with secure defaults:

```json
{
  "auth": {
    "enabled": true,
    "required": true
  },
  "ratelimit": {
    "enabled": true,
    "anonymous": {
      "windowSeconds": 60,
      "maxRequests": 100
    },
    "authenticated": {
      "windowSeconds": 60,
      "maxRequests": 1000
    }
  },
  "logging": {
    "level": "info",
    "maskTokens": true
  }
}
```

### Configuration Validation

Never disable security features in production:

```bash
# ✓ GOOD - auth required
conduit server --config config.json  # Has "auth.required": true

# ✗ BAD - auth disabled (security risk)
# Config with "auth.enabled": false
```

### Configuration Files

Protect configuration files:

```bash
# Restrict permissions
chmod 600 config.json

# Remove from version control
echo "config.json" >> .gitignore
echo "*.db" >> .gitignore
echo ".env" >> .gitignore
```

## Deployment Checklist

### Pre-Deployment

- [ ] **Authentication enabled** - `auth.enabled: true` in config
- [ ] **Auth required for protected endpoints** - `auth.required: true`
- [ ] **Rate limiting enabled** - `ratelimit.enabled: true`
- [ ] **Database encrypted at rest** - Use encrypted storage or LUKS
- [ ] **HTTPS enabled** - TLS certificates installed
- [ ] **Secrets stored securely** - Not in code or config files
- [ ] **Logging configured** - Log level set appropriately
- [ ] **Backups configured** - Daily encrypted backups
- [ ] **Monitoring enabled** - Alerts set up for key metrics

### Initial Deployment

- [ ] **Database initialized** - Tables created with proper schema
- [ ] **Admin token created** - For initial setup
- [ ] **File permissions correct** - 644 for DB, 600 for config
- [ ] **Service account created** - Limited privileges
- [ ] **Process running as unprivileged user** - Not root
- [ ] **SSL/TLS configured** - Certificate valid and not self-signed
- [ ] **Firewall rules in place** - Only allow necessary ports
- [ ] **Backup tested** - Restore process verified

### Ongoing Operations

- [ ] **Token rotation schedule** - Quarterly or sooner
- [ ] **Database maintenance** - Monthly `VACUUM`, integrity checks
- [ ] **Log rotation** - Prevent unbounded disk usage
- [ ] **Monitoring review** - Alert thresholds appropriate
- [ ] **Security patches** - Apply Go updates and dependencies
- [ ] **Incident response plan** - Documented and tested
- [ ] **Access audit** - Review who has access to tokens

## Incident Response

### Suspected Token Compromise

If you believe a token has been compromised:

**Immediate actions (within minutes):**

```bash
# 1. Revoke the token immediately
conduit token revoke <token-id>

# 2. Check token usage logs
journalctl -u conduit | grep "client=<client-name>"

# 3. Monitor for unauthorized access
journalctl -u conduit | grep -i "error\|failed"
```

**Follow-up actions (within hours):**

1. Create new replacement token
2. Update all systems using old token
3. Analyze logs for unauthorized activity
4. Check for persistence (backdoors, additional tokens)

**Reporting:**

```bash
# Create incident report
cat > incident-report.md << EOF
## Token Compromise Incident

- **Time discovered:** 2026-02-07 14:30 UTC
- **Token ID:** uuid-here
- **Client:** my-app
- **Created:** 2026-01-15
- **Last rotated:** 2026-01-30
- **Revoked:** 2026-02-07 14:35
- **Compromise source:** (suspected source)
- **Duration exposed:** ~5 minutes
- **Unauthorized requests:** (check logs)
- **Remediation:** New token created and deployed

## Follow-up
- [ ] Change all related passwords
- [ ] Enable additional monitoring
- [ ] Review access logs for other tokens
- [ ] Document lessons learned
EOF
```

### Database Compromise

If the database file is exposed:

**Risk assessment:**
- Raw tokens are **NOT recoverable** from hashes
- Attacker gains knowledge of active tokens (but not their values)
- Attacker knows client names and metadata

**Response:**

```bash
# 1. Revoke ALL tokens immediately
# (SQL query if CLI unavailable)
sqlite3 gateway.db "UPDATE auth_tokens SET is_active = 0 WHERE token_id != '';"

# 2. Create new tokens for all clients
for client in $(openclw-go token list | awk '{print $2}'); do
    conduit token create --client-name "$client"
done

# 3. Distribute new tokens securely
# (use secure channel - not email, not Slack)

# 4. Verify no unauthorized tokens created
conduit token list --include-revoked | tail -20

# 5. Review access logs for suspicious activity
journalctl -u conduit -n 1000 | grep -i error
```

### Denial of Service

If experiencing DoS attacks:

1. **Check rate limit logs:**
   ```bash
   journalctl -u conduit | grep "Rate limit exceeded"
   ```

2. **Identify attack source:**
   ```bash
   journalctl -u conduit | grep "Rate limit" | awk '{print $NF}' | sort | uniq -c | sort -rn
   ```

3. **Temporarily increase limits** (if under attack):
   ```json
   {
     "ratelimit": {
       "anonymous": { "maxRequests": 500 },  // Increased temporarily
       "authenticated": { "maxRequests": 5000 }
     }
   }
   ```

4. **Implement IP blocking:**
   ```bash
   # In reverse proxy (nginx)
   deny 192.168.1.100;
   deny 192.168.1.101;
   ```

5. **Scale infrastructure:**
   - Load balance across multiple gateway instances
   - Each instance tracks rate limits independently
   - Use shared rate limit service for consistency

### Support Escalation

When to escalate:

- Suspected compromise with evidence of unauthorized access
- Database corruption or data loss
- Widespread service outages
- Suspicious patterns not covered by monitoring

Contact Conduit support with:
- Timeline of events
- Relevant log excerpts
- Current risk assessment
- Steps already taken

## Security Testing

### Penetration Testing

Test your deployment:

```bash
# 1. Valid token auth
curl -H "Authorization: Bearer $VALID_TOKEN" \
  http://localhost:18890/api/test  # Should succeed

# 2. Invalid token
curl -H "Authorization: Bearer invalid" \
  http://localhost:18890/api/test  # Should fail with 403

# 3. Missing token
curl http://localhost:18890/api/test  # Should fail with 401

# 4. Rate limiting
for i in {1..150}; do
  curl -H "Authorization: Bearer $TOKEN" http://localhost:18890/api/test
done
# After 100 requests, should get 429

# 5. Expired token
conduit token create --client-name test --expires-in "-1h"
# Should be rejected immediately
```

### Vulnerability Scanning

Scan dependencies for vulnerabilities:

```bash
# Go security scanner
go list -json ./... | nancy sleuth

# Or using govulncheck
govulncheck ./...

# OWASP Dependency-Check
dependency-check --project "Conduit Gateway" --scan .
```

## Further Reading

- [OWASP Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)
- [OWASP API Security Top 10](https://owasp.org/www-project-api-security/)
- [Go Security Best Practices](https://golang.org/security)
- [SQLite Security](https://www.sqlite.org/security.html)
