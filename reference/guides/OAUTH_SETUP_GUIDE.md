# OAuth Setup Guide for Conduit Go

## Overview

The OAuth token acquisition process involves getting an access token from Anthropic's OAuth service that can be used instead of API keys for authentication.

## Current Status

From `conduit models status`, you currently have:
- **anthropic:default** = token (static API key)
- **OAuth providers available**: anthropic (1)  
- **OAuth tokens configured**: 0

## OAuth Token Acquisition Process

### Method 1: Using Conduit CLI (Recommended)

The TypeScript Conduit has built-in OAuth support via the `@mariozechner/pi-ai` library:

```bash
# Interactive OAuth flow for Anthropic
conduit models auth login --provider anthropic

# This will:
# 1. Open your browser to Anthropic's OAuth authorization page
# 2. You authorize Conduit to access your account
# 3. Conduit receives the authorization code
# 4. Exchanges code for access token + refresh token
# 5. Stores tokens in auth-profiles.json
```

### Method 2: Manual OAuth Flow

If you need to set up OAuth manually:

#### Step 1: Create OAuth Application
1. Go to **Anthropic Console** → **API Keys** → **OAuth Apps**
2. Create new OAuth application:
   - **Application Name**: "Conduit Go Gateway"
   - **Redirect URI**: `http://localhost:8080/callback` (or your callback URL)
   - **Scopes**: Select appropriate API access scopes

#### Step 2: Get Authorization Code
```bash
# Browser redirect to:
https://api.anthropic.com/oauth/authorize?
  client_id=YOUR_CLIENT_ID&
  response_type=code&
  scope=api&
  redirect_uri=http://localhost:8080/callback&
  state=random_state_string
```

#### Step 3: Exchange Code for Token
```bash
curl -X POST https://api.anthropic.com/oauth/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=authorization_code" \
  -d "code=AUTHORIZATION_CODE_FROM_STEP_2" \
  -d "client_id=YOUR_CLIENT_ID" \
  -d "client_secret=YOUR_CLIENT_SECRET" \
  -d "redirect_uri=http://localhost:8080/callback"
```

**Response:**
```json
{
  "access_token": "at_XXXXXXXXXXXXXXXXXX",
  "token_type": "Bearer", 
  "expires_in": 3600,
  "refresh_token": "rt_XXXXXXXXXXXXXXXXXX",
  "scope": "api"
}
```

#### Step 4: Set Environment Variable
```bash
export ANTHROPIC_OAUTH_TOKEN="at_XXXXXXXXXXXXXXXXXX"
export ANTHROPIC_API_KEY="sk-ant-fallback..."  # Keep as fallback
```

## Integration with Conduit Go

### Current Implementation Status

✅ **OAuth Support Added**:
- Config structure supports OAuth tokens
- Environment variable expansion for `ANTHROPIC_OAUTH_TOKEN`
- Automatic Bearer token authentication when OAuth token present
- Fallback to API key when OAuth token missing/expired

### Using OAuth Tokens

1. **Set your OAuth token**:
   ```bash
   export ANTHROPIC_OAUTH_TOKEN="at_your_oauth_token_here"
   ```

2. **Start Conduit Go**:
   ```bash
   cd ~/projects/conduit
   make run
   ```

3. **Verification**: Check logs for Bearer token usage vs API key fallback

## Token Management

### Current TypeScript Implementation
- Tokens stored in: `~/.conduit/agents/main/agent/auth-profiles.json`
- Format:
```json
{
  "profiles": {
    "anthropic:default": {
      "type": "oauth",
      "provider": "anthropic", 
      "access_token": "at_...",
      "refresh_token": "rt_...", 
      "expires_at": 1733456789
    }
  }
}
```

### Future Go Implementation
The Go version needs to implement:
1. **Token refresh** when approaching expiration
2. **Token storage** persistence between restarts  
3. **OAuth authorization flow** for initial token acquisition

## Next Steps

### For Immediate Use
1. **Get OAuth token** via TypeScript Conduit:
   ```bash
   conduit models auth login --provider anthropic
   ```

2. **Extract token** from auth-profiles.json:
   ```bash
   cat ~/.conduit/agents/main/agent/auth-profiles.json | jq '.profiles."anthropic:default".access_token'
   ```

3. **Use with Go version**:
   ```bash
   export ANTHROPIC_OAUTH_TOKEN="extracted_token_here"
   cd ~/projects/conduit && make run
   ```

### For Complete Go OAuth Implementation
1. **Add OAuth client** to config structure
2. **Implement authorization flow** (browser redirect handling)
3. **Add token refresh** logic with automatic persistence
4. **Create OAuth CLI commands** similar to TypeScript version

## Benefits of OAuth vs API Keys

- **Better Security**: Tokens can be scoped and revoked
- **Automatic Refresh**: No manual token rotation needed
- **Audit Trail**: Better tracking of API usage
- **Modern Standard**: Industry best practice for API authentication

## Troubleshooting

### Token Validation
```bash
# Test your OAuth token
curl -H "Authorization: Bearer $ANTHROPIC_OAUTH_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"model": "claude-3-5-sonnet-20250114", "max_tokens": 10, "messages": [{"role": "user", "content": "Hi"}]}' \
     https://api.anthropic.com/v1/messages
```

### Common Issues
- **Invalid token**: Check token format and expiration
- **Wrong scopes**: Ensure OAuth app has API access scope
- **Network issues**: Verify HTTPS connectivity to Anthropic API
- **Config errors**: Check environment variable expansion in config.json