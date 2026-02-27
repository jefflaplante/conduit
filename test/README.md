# Test Files and Configuration

This directory contains test-specific files, configurations, and databases.

## Structure

```
test/
├── configs/                   # Test configurations
│   ├── config.direct-test.json      # Direct API testing
│   ├── config.live-test.json        # Live environment testing  
│   ├── config.test-integration.json # Integration tests
│   ├── config.test.json             # Basic unit tests
│   └── config.test-jules.json       # Jules AI client testing
├── databases/                 # Test databases
│   ├── direct-test.db              # Direct test database
│   ├── gateway.db*                 # Gateway test databases
│   ├── live-test.db                # Live test database
│   ├── test.db                     # Basic test database
│   ├── test-integration.db         # Integration test database
│   └── test-jules.db               # Jules client test database
├── integration/               # Integration tests (from auth sprint)
│   ├── auth_test.go
│   └── README.md
├── coverage.out              # Test coverage report
├── test_oauth.go             # OAuth testing utilities
└── test-jules-client.js      # Jules client test script
```

## Running Tests

### Unit Tests
```bash
go test ./internal/... ./pkg/...
```

### Integration Tests  
```bash
go test ./test/integration/...
```

### With Specific Config
```bash
./bin/conduit server --config test/configs/config.test-integration.json
```

## Test Databases

Test databases are SQLite files created during testing:

- **In-memory tests**: Use `:memory:` (no files created)
- **Integration tests**: Create temporary databases in `test/databases/`
- **Manual testing**: Use configs that point to specific test databases

### Cleanup Test Databases

```bash
# Remove all test databases
rm test/databases/*.db*

# Remove specific test database
rm test/databases/test-integration.db*
```

## Test Configuration Guidelines

When creating test configs:

1. **Use test-specific database paths**: Point to `test/databases/`
2. **Use test ports**: Avoid conflicts with production (e.g., 18891, 18892)
3. **Use test tokens**: Never use production API keys
4. **Include test data**: Pre-populate test databases if needed

## Test Client Scripts

### Jules Client Test (`test-jules-client.js`)

JavaScript client for testing AI agent authentication:

```bash
node test/test-jules-client.js
```

### OAuth Test (`test_oauth.go`)

Go utilities for testing OAuth token flows:

```bash
go run test/test_oauth.go
```

## Coverage Reports

Test coverage is generated in `coverage.out`:

```bash
# Generate coverage
go test -coverprofile=test/coverage.out ./...

# View coverage in browser
go tool cover -html=test/coverage.out
```

## CI/CD Integration

Tests are designed to run in CI environments:

- **No external dependencies**: All tests use in-memory or local databases
- **Parallel safe**: Tests don't conflict with each other
- **Fast execution**: Most tests complete in <100ms
- **Deterministic**: No random failures or timing issues

## Contributing

When adding tests:

1. **Create appropriate config**: Add to `test/configs/` if needed
2. **Use test databases**: Store in `test/databases/`
3. **Follow naming**: `Test*` for unit tests, `TestIntegration*` for integration tests
4. **Include benchmarks**: `Benchmark*` for performance-critical code
5. **Document purpose**: Add comments explaining what each test verifies