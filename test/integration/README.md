# Integration Test Suite for Web Search System

This directory contains comprehensive integration tests for the Conduit hybrid web search system, validating OAuth compatibility, provider routing, error handling, and performance requirements.

## Test Categories

### 1. OAuth Validation Tests (`TestOAuthValidation`)
- **OAuth Token Detection**: Validates detection of `sk-ant-oat01-*` tokens
- **Tool Name Mapping**: Tests mapping from `web_search_20250305` → `WebSearch`
- **Header Validation**: Ensures all required OAuth headers are present
- **System Prompt Format**: Validates array format requirement for OAuth requests

### 2. Provider Routing Tests (`TestProviderRouting`)
- **Model Detection**: Tests Anthropic vs non-Anthropic model routing
- **OAuth vs Regular Tokens**: Side-by-side OAuth and regular token scenarios
- **Fallback Behavior**: Tests Anthropic down → Brave fallback

### 3. Search Provider Integration (`TestSearchProviders`)
- **Brave Direct Search**: Real API integration tests (when API key available)
- **Anthropic Native Search**: OAuth-compatible search integration
- **Response Validation**: Tests result structure and metadata

### 4. Error Handling (`TestErrorHandling`)
- **Network Failures**: Timeout and connection error handling
- **API Quota Exceeded**: Rate limiting and authentication error handling
- **Provider Fallback**: Graceful degradation when primary provider fails

### 5. Performance Testing (`TestPerformance`)
- **Search Response Time**: < 3 seconds requirement validation
- **Fallback Response Time**: < 1 second fallback requirement
- **Rate Limiting Protection**: Prevents API abuse
- **Cache Effectiveness**: Measures cache performance improvement

### 6. WebSearchTool Integration (`TestWebSearchTool`)
- **Mock Provider Testing**: Tests tool with mock search providers
- **Real API Integration**: End-to-end testing with actual APIs
- **Parameter Validation**: Input validation and error handling

## Test Setup Requirements

### Environment Variables

For complete testing, set these environment variables:

```bash
# Required for Brave search integration tests
export BRAVE_API_KEY="your_brave_api_key_here"

# Required for Anthropic native search tests (OAuth tokens preferred)
export ANTHROPIC_API_KEY="sk-ant-oat01-your_oauth_token_here"
```

### Test Modes

**Full Integration Tests** (with real APIs):
```bash
cd ~/projects/conduit
go test ./test/integration/... -v
```

**Mock-Only Tests** (no API keys required):
```bash
cd ~/projects/conduit
go test ./test/integration/... -v -short
```

**Performance Benchmarks**:
```bash
cd ~/projects/conduit
go test ./test/integration/... -v -bench=. -benchmem
```

**CLI Integration Tests**:
```bash
cd ~/projects/conduit
go test ./cmd/gateway/... -v
```

### Test Configuration

The tests use configuration file: `test/configs/config.search-integration.json`

Key configuration sections:
- Search provider settings (Brave, Anthropic)
- Cache configuration
- Rate limiting settings
- Authentication setup

### Test Fixtures

Mock response data is stored in `test/fixtures/search_responses/`:
- `brave_success.json` - Successful Brave API response
- `brave_error_401.json` - Authentication error response
- `anthropic_success.json` - Anthropic API success response

## Running Specific Test Suites

### OAuth Validation Only
```bash
go test ./test/integration/... -v -run TestWebSearchIntegrationSuite/OAuthValidation
```

### Provider Routing Only
```bash
go test ./test/integration/... -v -run TestWebSearchIntegrationSuite/ProviderRouting
```

### Performance Tests Only
```bash
go test ./test/integration/... -v -run TestWebSearchIntegrationSuite/Performance
```

## Expected Test Coverage

The integration test suite validates:

- ✅ OAuth token detection (`sk-ant-oat01-*` pattern)
- ✅ Tool name mapping for OAuth compatibility
- ✅ Required OAuth headers validation
- ✅ System prompt array format for OAuth
- ✅ Provider routing based on model names
- ✅ Fallback from Anthropic to Brave when needed
- ✅ Real API integration (when keys available)
- ✅ Error handling for network failures
- ✅ Error handling for API quota/auth issues
- ✅ Performance requirements (< 3s search, < 1s fallback)
- ✅ Rate limiting protection
- ✅ Cache effectiveness measurement
- ✅ End-to-end WebSearchTool functionality

## Performance Requirements Validation

### Search Response Time
- **Requirement**: All searches complete under 3 seconds
- **Test**: `testSearchResponseTimes`
- **Validates**: Real API performance with actual network calls

### Fallback Response Time  
- **Requirement**: Fallback scenarios complete under 1 second additional delay
- **Test**: `testFallbackResponseTimes`
- **Validates**: Router efficiently switches between providers

### Cache Effectiveness
- **Requirement**: Cached responses significantly faster than API calls
- **Test**: `testCacheEffectiveness`
- **Validates**: Cache hit performance vs cache miss performance

## OAuth Compatibility Validation

### Critical OAuth Scenarios Tested

1. **Token Detection**: `sk-ant-oat01-*` tokens correctly identified
2. **Header Requirements**: All anthropic-beta, user-agent, etc. headers present
3. **Tool Name Mapping**: `web_search_20250305` → `WebSearch` mapping works
4. **System Prompt Format**: String prompts converted to required array format
5. **Forbidden Tools**: Conduit-specific tools filtered out for OAuth requests

### OAuth vs Regular Token Side-by-Side Testing

The test suite runs parallel scenarios with:
- OAuth tokens (`sk-ant-oat01-*`) - should route to Anthropic native search
- Regular tokens (`sk-ant-api01-*`) - should route to Brave fallback

## Troubleshooting

### Common Issues

**Tests Skipped - No API Keys**:
- Set `BRAVE_API_KEY` environment variable
- Tests will run with mock data only without keys

**Performance Tests Failing**:
- Check network connectivity to search APIs
- Verify API key quotas and rate limits
- Consider geographic latency to API endpoints

**OAuth Tests Failing**:
- Ensure OAuth token format: `sk-ant-oat01-*`
- Verify all required headers are configured
- Check system prompt is properly converted to array format

### Debug Mode

Enable verbose logging:
```bash
go test ./test/integration/... -v -args -log-level=debug
```

## Integration with CI/CD

For automated testing without API keys:
```bash
# CI-friendly test run (mock data only)
go test ./test/integration/... -short -v
```

The test suite is designed to provide meaningful validation even without real API access, using comprehensive mock data and error simulation.