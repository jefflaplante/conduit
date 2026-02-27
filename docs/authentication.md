# Conduit Gateway Authentication Guide

This guide covers how to set up and use authentication with the Conduit Go Gateway.

## Table of Contents

1. [Administrator Setup](#administrator-setup)
2. [HTTP Client Usage](#http-client-usage)
3. [WebSocket Client Usage](#websocket-client-usage)
4. [Jules AI Setup](#jules-ai-setup)
5. [Token Management](#token-management)
6. [Rate Limiting](#rate-limiting)
7. [Troubleshooting](#troubleshooting)

## Administrator Setup

### Prerequisites

- Conduit Go Gateway installed and available on your PATH
- SQLite database file (auto-created on first run)
- Basic command-line knowledge

### Creating Tokens

#### Basic Token Creation

Create a token for a client:

```bash
conduit token create --client-name "my-app"
```

This returns a token that looks like:

```
Token: conduit_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0...
TokenID: uuid-string-here
ClientName: my-app
CreatedAt: 2026-02-07T21:00:00Z
```

**Important:** The raw token is only displayed once. Store it securely immediately.

#### Token with Expiration

Create a token that expires in 1 year:

```bash
conduit token create --client-name "julian-ai" --expires-in "1y"
```

Supported expiration formats:
- `24h` - 24 hours from now
- `7d` - 7 days from now
- `30d` - 30 days from now
- `1y` - 1 year from now
- RFC3339 timestamp: `2026-12-31T23:59:59Z`

#### Token with Metadata

Create a token with custom metadata:

```bash
conduit token create \
  --client-name "prod-server" \
  --metadata "environment=production" \
  --metadata "version=2.0"
```

### Listing Tokens

View all active tokens:

```bash
conduit token list
```

Include revoked tokens:

```bash
conduit token list --include-revoked
```

Output shows:
- TokenID - unique identifier
- ClientName - the client name
- CreatedAt - when the token was created
- ExpiresAt - expiration time (if set)
- IsActive - whether the token is still valid
- Metadata - custom key-value pairs

### Revoking Tokens

Revoke a token by its ID:

```bash
conduit token revoke claw_v1_abc123...
```

Or by ID:

```bash
conduit token revoke <token-id-from-list>
```

Once revoked, the token cannot be used. This action is immediate and irreversible.

### Exporting Token Configuration

Export a token as an environment variable:

```bash
conduit token export conduit_abc123... --format env
```

Output:

```bash
export CONDUIT_TOKEN="conduit_abc123..."
```

## HTTP Client Usage

### Bearer Token in Authorization Header

The most common method. Send the token in the `Authorization` header:

```bash
curl -H "Authorization: Bearer conduit_abc123..." \
  http://localhost:18890/api/channels/status
```

**Python example:**

```python
import requests

token = "conduit_abc123..."
headers = {"Authorization": f"Bearer {token}"}
response = requests.get("http://localhost:18890/api/channels/status", headers=headers)
print(response.json())
```

**JavaScript example:**

```javascript
const token = "conduit_abc123...";
const response = await fetch("http://localhost:18890/api/channels/status", {
  headers: {
    "Authorization": `Bearer ${token}`
  }
});
const data = await response.json();
```

### X-API-Key Header

Alternative method using the `X-API-Key` header:

```bash
curl -H "X-API-Key: conduit_abc123..." \
  http://localhost:18890/api/channels/status
```

### Query Parameter

For WebSocket connections without header support, use the `token` query parameter:

```
ws://localhost:18890/ws?token=conduit_abc123...
```

**Note:** Query parameters are less secure since they appear in logs and URL history. Use for development only.

## WebSocket Client Usage

### Authentication During Upgrade

WebSocket authentication happens during the HTTP upgrade handshake. Three methods are supported:

#### Method 1: Sec-WebSocket-Protocol Header

This is the recommended secure method:

```python
import asyncio
import websockets

async def connect():
    uri = "ws://localhost:18890/ws"
    
    # Include "conduit-auth" protocol and your token
    subprotocols = ["conduit-auth", "conduit_abc123..."]
    
    async with websockets.connect(uri, subprotocols=subprotocols) as websocket:
        msg = await websocket.recv()
        print(f"Received: {msg}")

asyncio.run(connect())
```

The server will echo back "conduit-auth" in the response if successful.

#### Method 2: Authorization Header

Some WebSocket libraries support custom headers:

```python
import asyncio
import websockets

async def connect():
    uri = "ws://localhost:18890/ws"
    
    headers = {
        "Authorization": "Bearer conduit_abc123..."
    }
    
    async with websockets.connect(uri, extra_headers=headers) as websocket:
        msg = await websocket.recv()
        print(f"Received: {msg}")

asyncio.run(connect())
```

#### Method 3: Query Parameter

For clients without header support:

```python
import asyncio
import websockets

async def connect():
    token = "conduit_abc123..."
    uri = f"ws://localhost:18890/ws?token={token}"
    
    async with websockets.connect(uri) as websocket:
        msg = await websocket.recv()
        print(f"Received: {msg}")

asyncio.run(connect())
```

### WebSocket Close Codes

If authentication fails, the server closes the connection with these codes:

- **4401** (Unauthorized) - No token provided
- **4403** (Forbidden) - Invalid or expired token

## Jules AI Setup

Jules is Conduit's built-in AI agent. Here's how to set up authentication for Jules:

### Step 1: Create a Jules Token

Create a long-lived token for Jules:

```bash
# Create a token valid for 1 year
conduit token create \
  --client-name "jules-main" \
  --expires-in "1y"
```

Save the returned token (it looks like `conduit_...`).

### Step 2: Store in Environment

Add the token to your shell configuration:

```bash
# Add to ~/.bashrc, ~/.zshrc, or ~/.conduit-secrets.env
export CONDUIT_TOKEN="conduit_abc123def456..."
```

### Step 3: Verify Setup

Test that Jules can authenticate:

```bash
# Start the gateway
conduit server

# In another terminal, test Jules access
curl -H "Authorization: Bearer $CONDUIT_TOKEN" \
  http://localhost:18890/api/channels/status
```

You should get a 200 response with channel information.

### Step 4: Jules Configuration

Jules automatically uses the `CONDUIT_TOKEN` environment variable when making requests to the gateway. Ensure it's available whenever Jules runs.

## Token Management

### Security Best Practices

1. **Never commit tokens to version control**
   - Use `.gitignore` for files containing tokens
   - Store in environment variables or secrets management systems

2. **Use short-lived tokens for sensitive operations**
   - Production tokens: 30-90 days expiration
   - Development tokens: 1 year (for convenience)

3. **Rotate tokens periodically**
   ```bash
   # Create replacement token
   conduit token create --client-name "my-app"
   
   # Update your application with new token
   
   # Revoke old token
   conduit token revoke <old-token-id>
   ```

4. **Monitor token usage**
   - Check `last_used_at` in token listings to find unused tokens
   - Revoke tokens that haven't been used in 90+ days

### Token Metadata

Add context to tokens using metadata:

```bash
conduit token create \
  --client-name "production-worker" \
  --expires-in "90d" \
  --metadata "environment=prod" \
  --metadata "owner=devops@example.com" \
  --metadata "purpose=background-jobs"
```

Later, you can review this metadata:

```bash
conduit token list
```

This helps track which tokens are used for what purpose.

## Rate Limiting

The gateway applies rate limits to all endpoints. Here's what you need to know:

### Anonymous Endpoints

The `/health` endpoint doesn't require authentication:

```bash
curl http://localhost:18890/health
# Returns: {"status": "ok"}
```

Rate limit: **100 requests per minute per IP address**

### Authenticated Endpoints

All other endpoints require authentication and have higher limits:

Rate limit: **1000 requests per minute per client token**

### Rate Limit Headers

Every response includes rate limit information:

```
X-RateLimit-Limit: 1000        # Total requests allowed
X-RateLimit-Remaining: 999     # Requests left in current window
X-RateLimit-Reset: 1707357000  # Unix timestamp when limit resets
```

### Handling Rate Limits

When you hit the rate limit, you get a 429 response:

```json
{
  "error": "rate_limit_exceeded",
  "message": "Rate limit exceeded",
  "retry_after": 45
}
```

**Retry-After** tells you how many seconds to wait before trying again.

**Best practices:**

1. Check `X-RateLimit-Remaining` before making non-critical requests
2. Implement exponential backoff when retrying (wait 1s, 2s, 4s, 8s, etc.)
3. Batch requests when possible to reduce total API calls
4. For high-traffic scenarios, contact the administrator for token tuning

Example retry logic:

```python
import time
import requests

def request_with_retry(url, token, max_retries=3):
    headers = {"Authorization": f"Bearer {token}"}
    
    for attempt in range(max_retries):
        response = requests.get(url, headers=headers)
        
        if response.status_code == 429:
            retry_after = int(response.json().get("retry_after", 60))
            wait_time = min(retry_after * (2 ** attempt), 300)
            print(f"Rate limited. Waiting {wait_time}s...")
            time.sleep(wait_time)
            continue
        
        return response
    
    raise Exception("Max retries exceeded")
```

## Troubleshooting

### "Invalid token" error

**Problem:** You get a 403 Forbidden response with "invalid token"

**Solutions:**

1. Verify the token is complete and hasn't been truncated
2. Ensure you're copying the full token including the `conduit_` prefix
3. Check if the token has expired:
   ```bash
   conduit token list --include-revoked | grep your-token-id
   ```
4. Verify the token hasn't been revoked

### "Missing token" error

**Problem:** You get a 401 Unauthorized response with "missing token"

**Solutions:**

1. Add the `Authorization: Bearer` header
2. Or use `X-API-Key` header
3. Or add `?token=` query parameter for WebSocket

Examples:

```bash
# Bearer header
curl -H "Authorization: Bearer conduit_..." http://...

# X-API-Key header
curl -H "X-API-Key: conduit_..." http://...

# Query parameter
curl "http://...?token=conduit_..."
```

### WebSocket connection fails immediately

**Problem:** WebSocket connection closes with 4401 or 4403 code

**Solutions:**

1. For 4401 (no token): Add token to request
2. For 4403 (invalid token): Check token validity
3. Verify server is running: `curl http://localhost:18890/health`
4. Check network connectivity and CORS settings if cross-origin

### Rate limit errors

**Problem:** Getting 429 Too Many Requests

**Solutions:**

1. **For authenticated requests:**
   - Check if you're making >1000 requests/minute
   - Implement request batching
   - Contact admin to increase limits if this is expected

2. **For anonymous requests:**
   - Check if you're making >100 requests/minute
   - If so, get a token for higher limits
   - Distribute requests across multiple IPs if appropriate

3. **Implement backoff:**
   ```python
   import time
   retry_after = int(response.headers.get("Retry-After", 60))
   time.sleep(retry_after)
   ```

### Token creation fails

**Problem:** `conduit token create` returns an error

**Solutions:**

1. Verify the database file exists and is writable:
   ```bash
   ls -la gateway.db
   ```

2. Check database permissions:
   ```bash
   chmod 644 gateway.db
   ```

3. Try with explicit database path:
   ```bash
   conduit token create \
     --database /path/to/gateway.db \
     --client-name "my-app"
   ```

4. Check server isn't running (would lock database):
   ```bash
   ps aux | grep conduit
   ```

### Performance issues

**Problem:** Requests are slow or timing out

**Solutions:**

1. Check server status:
   ```bash
   curl http://localhost:18890/health
   ```

2. Look at server logs for errors

3. Verify network latency:
   ```bash
   time curl -H "Authorization: Bearer $CONDUIT_TOKEN" \
     http://localhost:18890/api/channels/status
   ```

4. If authenticating takes too long, contact admin
   - Expected auth time: <1ms for cached tokens

## Additional Resources

- [Security Guide](./security.md) - Security considerations and best practices
- [Rate Limiting Guide](./rate-limiting.md) - Detailed rate limiting information
- [Troubleshooting Guide](./troubleshooting.md) - More troubleshooting scenarios

## Support

For issues or questions:
1. Check the troubleshooting section above
2. Review logs: `journalctl -u conduit`
3. Contact your Conduit administrator
