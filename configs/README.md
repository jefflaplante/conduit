# Configuration Files

This directory contains configuration files for different purposes.

## Structure

```
configs/
├── examples/          # Template and example configurations
│   ├── config.example.json     # Full example config with comments
│   └── config-minimal.json     # Minimal working config
├── config.json               # Generic config (legacy)
├── config-oauth-test.json     # OAuth testing configuration
├── config.skills.json        # Skills system configuration
├── config.telegram.json      # Telegram channel configuration
└── config.tools.json         # Tools-only configuration
```

## Active Configuration

The **active configuration** is `config.live.json` in the project root. This is what the running gateway uses.

## Usage

### Development
Copy from examples and modify:
```bash
cp configs/examples/config.example.json config.dev.json
# Edit config.dev.json for your needs
./bin/conduit server --config config.dev.json
```

### Testing
Use test-specific configs from `test/configs/` for testing scenarios.

### Component Testing
- `config.skills.json` - Test skills system in isolation
- `config.telegram.json` - Test Telegram integration only
- `config.tools.json` - Test tool execution without channels

## Configuration Schema

See `internal/config/config.go` for the complete configuration structure and validation rules.