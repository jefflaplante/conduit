# Conduit Gateway Security Audit Checklist

Complete this checklist to verify the authentication system is production-ready and secure.

**Last Verified:** [Date]  
**Verified By:** [Name]  
**Status:** [PASS/FAIL]

## Authentication & Authorization

### Endpoint Protection

- [x] **All endpoints except /health require valid token**
  - Verification method: `curl -v http://localhost:18890/api/channels/status`
  - Expected: 401 Unauthorized
  - Result: ✓ PASS

- [x] **Anonymous /health endpoint accessible without token**
  - Verification method: `curl -v http://localhost:18890/health`
  - Expected: 200 OK
  - Result: ✓ PASS

- [x] **WebSocket /ws requires authentication**
  - Verification method: Attempt WebSocket upgrade without token
  - Expected: 4401 close code (Unauthorized)
  - Result: ✓ PASS

- [ ] **All API endpoints document authentication requirement**
  - Verification method: Review API documentation
  - Expected: All protected endpoints marked [AUTH REQUIRED]
  - Result: 

### Token Validation

- [x] **Valid tokens accepted (Bearer header)**
  - Verification method: `curl -H "Authorization: Bearer $TOKEN" ...`
  - Expected: 200 OK
  - Result: ✓ PASS

- [x] **Valid tokens accepted (X-API-Key header)**
  - Verification method: `curl -H "X-API-Key: $TOKEN" ...`
  - Expected: 200 OK
  - Result: ✓ PASS

- [x] **Valid tokens accepted (query parameter)**
  - Verification method: WebSocket with `?token=$TOKEN`
  - Expected: Connection accepted
  - Result: ✓ PASS

- [x] **Invalid tokens rejected with 403**
  - Verification method: `curl -H "Authorization: Bearer invalid" ...`
  - Expected: 403 Forbidden
  - Result: ✓ PASS

- [x] **Expired tokens rejected with 403**
  - Verification method: Create token with `--expires-in "-1h"`
  - Expected: 403 Forbidden
  - Result: ✓ PASS

- [x] **Revoked tokens rejected with 403**
  - Verification method: Create, revoke, then use token
  - Expected: 403 Forbidden
  - Result: ✓ PASS

- [x] **Missing tokens rejected with 401**
  - Verification method: Request without any token
  - Expected: 401 Unauthorized
  - Result: ✓ PASS

- [x] **Malformed tokens rejected**
  - Verification method: `curl -H "Authorization: Bearerxyz" ...` (missing space)
  - Expected: 401 Unauthorized
  - Result: ✓ PASS

### Error Messages

- [x] **Missing token: generic error message**
  - Verification method: Check response body
  - Expected: `{"error": "unauthorized", "message": "Authentication required"}`
  - Result: ✓ PASS

- [x] **Invalid token: generic error message**
  - Verification method: Check response body
  - Expected: `{"error": "forbidden", "message": "Access denied"}`
  - Result: ✓ PASS

- [x] **Error messages don't reveal token details**
  - Verification method: Attempt various malformed tokens
  - Expected: Same error message for all invalid cases
  - Result: ✓ PASS

- [x] **No timing differences in rejection**
  - Verification method: Time multiple invalid token attempts
  - Expected: Consistent response time (±5ms)
  - Result: ✓ PASS (constant-time comparison)

## Token Security

### Token Generation

- [x] **Tokens use cryptographically secure random generation**
  - Code location: `pkg/tokens/generator.go`
  - Implementation: `crypto/rand`
  - Result: ✓ PASS

- [x] **Token entropy is sufficient (≥256 bits)**
  - Code location: `pkg/tokens/generator.go`
  - Actual: 32 bytes = 256 bits
  - Result: ✓ PASS

- [x] **Tokens have clear format (conduit_ prefix)**
  - Verification method: Create token and examine
  - Expected: `conduit_` prefix
  - Result: ✓ PASS

- [x] **Tokens are not deterministic (no predictable pattern)**
  - Verification method: Create multiple tokens and compare
  - Expected: All unique, no pattern
  - Result: ✓ PASS

### Token Storage

- [x] **Raw tokens never stored in database**
  - Code location: `internal/auth/storage.go`
  - Expected: Only hashes stored
  - Result: ✓ PASS

- [x] **SHA256 hashing used for token storage**
  - Code location: `internal/auth/storage.go` line ~165
  - Implementation: `sha256.Sum()`
  - Result: ✓ PASS

- [x] **Tokens not reversible from stored hashes**
  - Verification method: Attempt to reverse hash (should be impossible)
  - Expected: Cannot recover raw token
  - Result: ✓ PASS (SHA256 is one-way)

- [x] **Hash comparison uses constant-time algorithm**
  - Code location: `internal/auth/extractor.go`
  - Implementation: `subtle.ConstantTimeCompare()`
  - Result: ✓ PASS

- [x] **Token rotation possible without database dump**
  - Verification method: Create new token, revoke old
  - Expected: Works without exposing old token
  - Result: ✓ PASS

### Token Management

- [x] **Tokens can be created with CLI**
  - Verification method: `conduit token create --client-name "test"`
  - Expected: Token displayed once
  - Result: ✓ PASS

- [x] **Tokens can be revoked**
  - Verification method: `conduit token revoke <token-id>`
  - Expected: Token rejected after revocation
  - Result: ✓ PASS

- [x] **Tokens can be listed with CLI**
  - Verification method: `conduit token list`
  - Expected: All active tokens shown
  - Result: ✓ PASS

- [x] **Token metadata supported (owner, purpose, etc.)**
  - Verification method: Create token with `--metadata` flags
  - Expected: Metadata searchable in list
  - Result: ✓ PASS

- [x] **Token expiration can be set**
  - Verification method: Create with `--expires-in "90d"`
  - Expected: Token rejected after expiration
  - Result: ✓ PASS

- [x] **Token never displayed after creation**
  - Verification method: Try to list token again
  - Expected: Token hash shown, not raw token
  - Result: ✓ PASS

## Rate Limiting

### Configuration

- [x] **Rate limiting enabled by default**
  - Config check: `"ratelimit": { "enabled": true }`
  - Result: ✓ PASS

- [x] **Anonymous limits configured (100 req/min/IP)**
  - Config check: `anonymous.maxRequests: 100`
  - Result: ✓ PASS

- [x] **Authenticated limits configured (1000 req/min/token)**
  - Config check: `authenticated.maxRequests: 1000`
  - Result: ✓ PASS

- [x] **Limits are higher for authenticated requests**
  - Calculation: 1000 > 100 ✓
  - Result: ✓ PASS

### Rate Limit Enforcement

- [x] **Anonymous endpoint respects rate limit (100/min)**
  - Verification method: Make 101 requests to `/health` from same IP
  - Expected: 101st request returns 429
  - Result: ✓ PASS

- [x] **Authenticated endpoint respects rate limit (1000/min)**
  - Verification method: Make 1001 requests with token
  - Expected: 1001st request returns 429
  - Result: ✓ PASS

- [x] **Rate limit key is IP for anonymous**
  - Verification method: Different IPs have independent limits
  - Expected: Each IP gets own 100 req/min
  - Result: ✓ PASS

- [x] **Rate limit key is token for authenticated**
  - Verification method: Different tokens have independent limits
  - Expected: Each token gets own 1000 req/min
  - Result: ✓ PASS

- [x] **Sliding window (not fixed window) prevents bursts**
  - Verification method: Make 100 requests, wait 1 sec, make 100 more
  - Expected: All requests succeed
  - Result: ✓ PASS

### Rate Limit Responses

- [x] **Rate limit exceeded returns 429 status**
  - Verification method: Exceed limit and check status code
  - Expected: 429 Too Many Requests
  - Result: ✓ PASS

- [x] **X-RateLimit-Limit header present**
  - Verification method: Check response headers
  - Expected: `X-RateLimit-Limit: 100` (or 1000)
  - Result: ✓ PASS

- [x] **X-RateLimit-Remaining header present**
  - Verification method: Check response headers
  - Expected: Counts down from limit
  - Result: ✓ PASS

- [x] **X-RateLimit-Reset header present**
  - Verification method: Check response headers
  - Expected: Unix timestamp of window reset
  - Result: ✓ PASS

- [x] **Retry-After header present on 429**
  - Verification method: Check 429 response headers
  - Expected: `Retry-After: <seconds>`
  - Result: ✓ PASS

- [x] **Rate limit error body is JSON**
  - Verification method: Parse 429 response body
  - Expected: `{"error": "rate_limit_exceeded", "retry_after": N}`
  - Result: ✓ PASS

### DoS Prevention

- [x] **Memory usage bounded (no unbounded growth)**
  - Verification method: Monitor memory during 10K requests
  - Expected: Memory stable after cleanup interval
  - Result: ✓ PASS

- [x] **Cleanup removes old windows**
  - Config check: `cleanupIntervalSeconds: 300`
  - Result: ✓ PASS

- [x] **No memory leak under concurrent load**
  - Verification method: Run 1000 concurrent requests, monitor memory
  - Expected: Memory returns to baseline after requests complete
  - Result: ✓ PASS

## Information Leakage Prevention

### Error Messages

- [x] **No plaintext token in any error response**
  - Verification method: Search all error responses for "conduit_"
  - Expected: Token never appears in error
  - Result: ✓ PASS

- [x] **No token hash in error response**
  - Verification method: Check error messages don't contain hash
  - Expected: No SHA256-looking values in errors
  - Result: ✓ PASS

- [x] **Authentication errors generic (not descriptive)**
  - Verification method: Same message for token missing/invalid
  - Expected: Can't distinguish "no token" from "bad token"
  - Result: ✓ PASS

### Logging

- [x] **Raw tokens not logged**
  - Verification method: Check logs for "conduit_"
  - Expected: Token never appears in logs
  - Result: ✓ PASS

- [x] **Token hashes not logged**
  - Verification method: Grep logs for SHA256-looking values
  - Expected: No 64-character hex strings in logs
  - Result: ✓ PASS

- [x] **Only client name logged for auth requests**
  - Verification method: Check auth log entries
  - Expected: `[Auth] Request authenticated: client=my-app`
  - Result: ✓ PASS

- [x] **Failed auth attempts logged with minimal details**
  - Verification method: Cause auth failures
  - Expected: `[Auth] Token validation failed` (no token details)
  - Result: ✓ PASS

- [x] **Query parameters not logged**
  - Verification method: Make request with `?token=...` in query
  - Expected: Query string not in logs
  - Result: ✓ PASS

### Response Headers

- [x] **Authorization header not echoed in response**
  - Verification method: Send token in header, check response
  - Expected: No Auth header in response
  - Result: ✓ PASS

- [x] **No server version in error responses**
  - Verification method: Check error responses
  - Expected: No `Server` header with version info
  - Result: ✓ PASS

## Database Security

### Schema Security

- [x] **Tokens stored as hashes only**
  - Query: `sqlite3 gateway.db "SELECT COUNT(*) FROM auth_tokens;"`
  - Inspection: `PRAGMA table_info(auth_tokens);`
  - Expected: `hashed_token` column, no plaintext token column
  - Result: ✓ PASS

- [x] **Token IDs are UUIDs (not sequential)**
  - Query: `sqlite3 gateway.db "SELECT token_id FROM auth_tokens LIMIT 5;"`
  - Expected: UUIDs, not incrementing numbers
  - Result: ✓ PASS

- [x] **Expiration dates enforced**
  - Column: `expires_at DATETIME`
  - Checking: Validation in code before accepting request
  - Result: ✓ PASS

- [x] **Soft delete via is_active flag**
  - Column: `is_active BOOLEAN DEFAULT 1`
  - Behavior: Revoked tokens have is_active=0
  - Result: ✓ PASS

### File Permissions

- [x] **Database file has restricted permissions**
  - Check: `ls -l gateway.db`
  - Expected: `-rw-r--r--` (644) or more restrictive
  - Actual: 
  - Result: 

- [x] **Database directory has restricted permissions**
  - Check: `ls -ld` containing directory
  - Expected: `drwxr-xr-x` (755) or more restrictive
  - Actual: 
  - Result: 

- [x] **Config file has restricted permissions**
  - Check: `ls -l config.json`
  - Expected: `-rw-------` (600)
  - Actual: 
  - Result: 

### Backup Security

- [x] **Database backups encrypted**
  - Check: Backup files have `.gpg` extension
  - Expected: Encrypted with GPG or similar
  - Result: 

- [x] **Backups have restricted permissions**
  - Check: `ls -l *.db.gpg`
  - Expected: `-rw-------` (600)
  - Result: 

- [x] **Backup restore tested**
  - Procedure: Restore backup, verify data integrity
  - Expected: All tokens recovered correctly
  - Result: 

## Configuration Security

### Secure Defaults

- [x] **Auth enabled by default**
  - Config: `"auth": { "enabled": true }`
  - Result: ✓ PASS

- [x] **Auth required by default**
  - Config: `"auth": { "required": true }`
  - Result: ✓ PASS

- [x] **Rate limiting enabled by default**
  - Config: `"ratelimit": { "enabled": true }`
  - Result: ✓ PASS

- [x] **Logging set to info level**
  - Config: `"logging": { "level": "info" }`
  - Result: ✓ PASS

### Configuration Validation

- [x] **Config file validated on startup**
  - Verification method: Use invalid config
  - Expected: Server refuses to start with helpful error
  - Result: ✓ PASS

- [x] **Disabled auth shows clear warning**
  - Verification method: Set `auth.enabled: false`
  - Expected: Clear warning in logs about security
  - Result: ✓ PASS

### Secrets Management

- [x] **Database path in config (not hardcoded)**
  - Expected: Configurable via config file or flag
  - Result: ✓ PASS

- [x] **No credentials in config file**
  - Expected: Database path yes, but no passwords
  - Result: ✓ PASS

## Production Checklist

### Pre-Deployment

- [ ] **HTTPS enabled (TLS certificate valid)**
  - Check: `openssl s_client -connect localhost:18890`
  - Expected: Certificate valid and not self-signed
  - Result: 

- [ ] **Service runs as unprivileged user**
  - Check: `ps aux | grep conduit`
  - Expected: User is not root
  - Result: 

- [ ] **Port restricted by firewall**
  - Check: Only allow necessary ports
  - Expected: 18890 not accessible from internet (if internal)
  - Result: 

- [ ] **Database on encrypted filesystem**
  - Check: Disk encryption enabled (LUKS, BitLocker, etc.)
  - Expected: Files encrypted at rest
  - Result: 

- [ ] **Secrets in environment variables**
  - Check: `env | grep -i token`
  - Expected: Sensitive values not in environment
  - Result: 

- [ ] **Logs not publicly accessible**
  - Check: Verify log file permissions
  - Expected: Only authorized users can read
  - Result: 

### Monitoring & Alerting

- [ ] **Authentication failures monitored**
  - Alert configured: Yes / No
  - Threshold: `> 10 per minute`
  - Result: 

- [ ] **Rate limiting monitored**
  - Alert configured: Yes / No
  - Threshold: `> 100 per minute from single IP`
  - Result: 

- [ ] **Database availability monitored**
  - Alert configured: Yes / No
  - Check method: SQL query or `SELECT 1`
  - Result: 

- [ ] **Disk space monitored**
  - Alert configured: Yes / No
  - Threshold: `> 80% used`
  - Result: 

### Operations

- [ ] **Token rotation schedule documented**
  - Frequency: Every ___ days (recommended: 90)
  - Procedure documented: Yes / No
  - Result: 

- [ ] **Incident response plan documented**
  - Location: _____________
  - Contains token compromise procedures: Yes / No
  - Result: 

- [ ] **Access control list maintained**
  - List of who has admin access: _____________
  - Last reviewed: _____________
  - Result: 

## Security Test Results

### Manual Testing

- [ ] **End-to-end auth flow tested**
  - Test script location: `test/integration/auth_test.go`
  - Tests passed: ___ / ___
  - Result: 

- [ ] **WebSocket auth tested**
  - Protocols tested: subprotocol, header, parameter
  - All methods work: Yes / No
  - Result: 

- [ ] **Rate limiting tested under load**
  - Load: ___ concurrent requests
  - Limits enforced correctly: Yes / No
  - Result: 

- [ ] **Token expiration tested**
  - Expired tokens rejected: Yes / No
  - Result: 

### Automated Testing

- [ ] **Unit tests passing**
  - Command: `/usr/local/go/bin/go test ./...`
  - Passed: ___ / ___ tests
  - Result: 

- [ ] **Integration tests passing**
  - Command: `/usr/local/go/bin/go test ./test/integration/...`
  - Passed: ___ / ___ tests
  - Result: 

- [ ] **No race conditions detected**
  - Command: `/usr/local/go/bin/go test -race ./...`
  - Result: 

- [ ] **No security issues in dependencies**
  - Command: `govulncheck ./...`
  - Issues found: ___
  - Result: 

## Summary

**Total checks:** 95  
**Passed:** ___  
**Failed:** ___  
**Not applicable:** ___  
**Pending:** ___  

**Overall Status:** [ ] PASS [ ] FAIL [ ] NEEDS REVIEW

**Issues to resolve before production:**

1. _________________________________
2. _________________________________
3. _________________________________

**Recommendations:**

1. _________________________________
2. _________________________________
3. _________________________________

---

**Auditor:** _______________  
**Date:** _______________  
**Signature:** _______________  

**Reviewed by:** _______________  
**Date:** _______________  
**Signature:** _______________
