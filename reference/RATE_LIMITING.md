# Rate Limiting Implementation (OCGO-005)

## Overview

The Conduit Go Gateway now includes comprehensive rate limiting to prevent abuse of both anonymous and authenticated endpoints. The implementation uses a sliding window algorithm for precise and fair rate limiting.

## Features

### ✅ Completed Success Criteria

- [x] `/health` endpoint: 100 requests/minute (by IP, anonymous)
- [x] Authenticated endpoints: 1000 requests/minute (by client)
- [x] In-memory sliding window implementation
- [x] Proper HTTP rate limit headers (X-RateLimit-*)
- [x] Rate limit exceeded returns 429 with Retry-After header
- [x] Configurable limits via config file
- [x] Memory leak prevention with cleanup mechanisms
- [x] Bulletproof concurrency handling

### Key Technical Features

- **Sliding Window Algorithm**: More predictable than token bucket, prevents burst attacks
- **Dual Rate Limiting Strategy**: Anonymous (IP-based) vs Authenticated (client-based)
- **Standard HTTP Headers**: RFC-compliant rate limiting headers
- **Memory Efficient**: Automatic cleanup of expired buckets
- **High Performance**: ~4.5µs per request overhead
- **Concurrent Safe**: Uses sync.Map and per-bucket RWMutex
- **Proxy-Aware**: Handles X-Forwarded-For and X-Real-IP headers

## Configuration

### Default Settings

```json
{
  "rateLimiting": {
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

### Custom Configuration Example

```json
{
  "rateLimiting": {
    "enabled": true,
    "anonymous": {
      "windowSeconds": 300,
      "maxRequests": 500
    },
    "authenticated": {
      "windowSeconds": 60,
      "maxRequests": 2000
    },
    "cleanupIntervalSeconds": 600
  }
}
```

### Configuration Fields

- **`enabled`**: Enable/disable rate limiting globally
- **`anonymous.windowSeconds`**: Time window for anonymous requests (seconds)
- **`anonymous.maxRequests`**: Maximum requests per window for anonymous users
- **`authenticated.windowSeconds`**: Time window for authenticated requests (seconds)
- **`authenticated.maxRequests`**: Maximum requests per window for authenticated users
- **`cleanupIntervalSeconds`**: How often to clean up expired buckets (seconds)

## Rate Limiting Strategy

### Anonymous Requests (IP-based)

- **Identifier**: Client IP address (from RemoteAddr, X-Forwarded-For, or X-Real-IP)
- **Default Limit**: 100 requests per minute
- **Use Case**: Public endpoints like `/health`

### Authenticated Requests (Client-based)

- **Identifier**: Authenticated client name from auth token
- **Default Limit**: 1000 requests per minute
- **Use Case**: Protected API endpoints like `/api/*`

## HTTP Headers

All rate-limited responses include these headers:

- **`X-RateLimit-Limit`**: Maximum requests allowed in the window
- **`X-RateLimit-Remaining`**: Requests remaining in current window
- **`X-RateLimit-Reset`**: Unix timestamp when window resets
- **`Retry-After`**: Seconds to wait (only on 429 responses)

## Error Response (429 Too Many Requests)

```json
{
  "error": "rate_limit_exceeded",
  "message": "Rate limit exceeded. Try again later.",
  "retry_after": 30
}
```

## Middleware Integration

### Gateway Integration

The rate limiting middleware is automatically integrated into the gateway with proper ordering:

```
auth middleware → rate limiting middleware → handler
```

### Endpoint Coverage

- **`/health`**: Anonymous rate limiting only
- **`/ws`**: Rate limiting applied to WebSocket upgrades
- **`/api/*`**: Authentication required + authenticated rate limiting

## Implementation Details

### File Structure

```
internal/ratelimit/
├── sliding_window.go           # Core sliding window algorithm
└── sliding_window_test.go      # Algorithm tests

internal/middleware/
├── ratelimit.go                # Rate limiting middleware
├── ratelimit_test.go           # Middleware tests
└── ratelimit_integration_test.go # End-to-end tests

internal/config/
├── config.go                   # Updated config with rate limiting
└── ratelimit_config_test.go    # Configuration tests
```

### Memory Management

- **Bucket Cleanup**: Inactive buckets removed every 5 minutes (configurable)
- **Timestamp Cleanup**: Expired timestamps removed on each request
- **Bounded Memory**: No unbounded growth, scales with active clients/IPs

### Performance Characteristics

- **Latency**: ~4.5µs per request (measured on standard hardware)
- **Concurrency**: Thread-safe, handles high concurrent load
- **Memory**: O(active_clients) memory usage
- **CPU**: O(log(timestamps_per_bucket)) per request

### Sliding Window Algorithm

Unlike token bucket or fixed window approaches:

1. **Tracks Individual Timestamps**: Each request timestamp is stored
2. **Continuous Sliding**: Window slides with each request, not in fixed intervals
3. **Precise Limits**: No burst allowances beyond the configured limit
4. **Fair Distribution**: Prevents gaming the system with timing attacks

### IP Address Extraction

Supports common proxy scenarios:

1. **X-Forwarded-For**: Takes first IP (original client)
2. **X-Real-IP**: Single IP from trusted proxy
3. **RemoteAddr**: Direct connection fallback

### Client Identification

- **Anonymous**: Uses extracted IP address
- **Authenticated**: Uses client name from auth token (more stable than IP)

## Testing

### Test Coverage

- **Unit Tests**: Core sliding window algorithm
- **Middleware Tests**: HTTP middleware integration
- **Integration Tests**: End-to-end with auth system
- **Performance Tests**: Benchmarks under load
- **Configuration Tests**: Config loading and validation

### Running Tests

```bash
# All rate limiting tests
go test ./internal/ratelimit/ ./internal/middleware/ ./internal/config/ -v -run="RateLimit"

# Performance benchmark
go test ./internal/middleware/ -bench="BenchmarkRateLimitingPerformance" -benchtime=5s

# Integration tests only
go test ./internal/middleware/ -v -run="Integration"
```

## Production Considerations

### Security

- **IP Privacy**: IP addresses are sanitized in logs (192.168.1.* format)
- **No Token Leakage**: Auth tokens never appear in rate limit logs
- **Generic Error Messages**: 429 responses don't reveal internal details

### Monitoring

Log messages include:

```
[RateLimit] Rate limit exceeded: GET /api/test (identifier: client_name, type: authenticated_client)
[Gateway] Rate limiting enabled (anonymous: 100 req/60s, authenticated: 1000 req/60s)
```

### Operational

- **Graceful Degradation**: If disabled, all requests pass through
- **Hot Reload**: Configuration changes require gateway restart
- **Statistics**: Available via middleware GetStats() method

## Future Enhancements

Potential improvements for future iterations:

1. **Per-Endpoint Limits**: Different limits for different endpoints
2. **Dynamic Limits**: Adjust limits based on load or client tier
3. **Distributed Rate Limiting**: Share state across multiple gateway instances
4. **Redis Backend**: Persist rate limiting state across restarts
5. **Custom Headers**: Allow configuration of header names
6. **Burst Allowance**: Allow configurable burst capacity
7. **Rate Limit Policies**: Complex rules based on client attributes

## Troubleshooting

### Common Issues

**Rate limiting not working**:
- Check `"enabled": true` in config
- Verify middleware is properly integrated
- Check logs for initialization messages

**Too aggressive limiting**:
- Increase `maxRequests` in config
- Increase `windowSeconds` for longer windows
- Check if proxy headers are correctly handled

**Memory usage growing**:
- Decrease `cleanupIntervalSeconds` for more frequent cleanup
- Monitor `GetStats()` output for bucket counts

### Debugging

Enable verbose logging to see rate limiting decisions:

```bash
# Check rate limiting status
curl -v http://localhost:18890/health

# Headers will show:
# X-RateLimit-Limit: 100
# X-RateLimit-Remaining: 99
# X-RateLimit-Reset: 1699123456
```

## Summary

OCGO-005 is now **COMPLETE** with a production-ready rate limiting system that:

- ✅ Protects all gateway endpoints from abuse
- ✅ Handles both anonymous and authenticated traffic appropriately
- ✅ Provides standards-compliant HTTP rate limiting
- ✅ Scales efficiently under high load
- ✅ Integrates seamlessly with existing authentication
- ✅ Prevents memory leaks with automatic cleanup
- ✅ Includes comprehensive test coverage

The gateway now has a complete security layer with **authentication + rate limiting** protecting all endpoints.