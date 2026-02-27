# Tool Versioning Strategy

This document explains Conduit's tool versioning and aliasing system for Anthropic's versioned tool names.

## Overview

Anthropic provides versioned tool names in their API (e.g., `web_search_20250305`, `computer_20241022`). To avoid hardcoding these versioned names throughout our codebase, we implement a centralized aliasing system.

## Architecture

### Core Files
- `internal/tools/anthropic.go` - Tool constants and version mappings
- `internal/tools/aliases.go` - Helper functions for tool resolution  
- `internal/tools/anthropic_test.go` - Tests for constants
- `internal/tools/aliases_test.go` - Tests for aliasing functions

### Key Components

#### Tool Constants
```go
const (
    // Current versioned tool names
    AnthropicWebSearchTool = "web_search_20250305"
    AnthropicWebFetchTool  = "web_fetch_20250305"
    
    // Legacy unversioned names  
    LegacyWebSearchTool = "web_search"
    LegacyWebFetchTool  = "web_fetch"
)
```

#### Alias Resolution
```go
// Get current version by alias
config, err := GetAnthropicTool("web_search")
// Returns: ToolConfig{Name: "web_search_20250305", Alias: "web_search", Version: "20250305"}
```

#### Environment Overrides
```bash
# Override tool versions for beta testing
export ANTHROPIC_TOOL_WEB_SEARCH_VERSION=web_search_20250401
export ANTHROPIC_TOOL_COMPUTER_USE_VERSION=computer_20241025
```

## Usage Patterns

### ✅ Good: Use Aliasing System
```go
// Get tool configuration
config, err := GetAnthropicTool("web_search")
if err != nil {
    return fmt.Errorf("failed to resolve tool: %w", err)
}

// Use the versioned name
toolName := config.Name // "web_search_20250305"
```

### ❌ Avoid: Hardcoded Tool Names
```go
// Don't do this - hardcoded version
toolName := "web_search_20250305"

// Don't do this - unversioned reference
if toolName == "web_search" {
    // ...
}
```

### ✅ Better: Check Tool Identity
```go
// Use alias for comparison
if config, err := GetAnthropicTool("web_search"); err == nil && config.Name == toolName {
    // This is a web search tool
}

// Or check if it's any Anthropic tool
if IsAnthropicTool(toolName) {
    // Handle Anthropic tool
}
```

## Version Management

### Adding New Tool Versions

1. **Update Constants** (`anthropic.go`)
   ```go
   const AnthropicWebSearchTool = "web_search_20250401" // Update version
   ```

2. **Update Version History** (`anthropic.go`)
   ```go
   ToolVersionHistory = map[string][]ToolVersionInfo{
       "web_search": {
           {
               Version:     "web_search_20250401",
               Introduced:  "2025-04-01", 
               Description: "Added filtering improvements",
               Changes:     []string{"Better relevance", "Content filtering"},
           },
           // ... previous versions
       },
   }
   ```

3. **Run Tests**
   ```bash
   go test ./internal/tools -v
   ```

### Adding New Tools

1. **Add Constants** (`anthropic.go`)
   ```go
   const AnthropicComputerUseTool = "computer_20241022"
   ```

2. **Add to Mapping** (`anthropic.go`)
   ```go
   var AnthropicToolVersions = map[string]string{
       "web_search":   AnthropicWebSearchTool,
       "computer_use": AnthropicComputerUseTool, // Add new mapping
   }
   ```

3. **Add Internal Name Mapping** (`aliases.go`)
   ```go
   func getInternalToolName(alias string) string {
       switch alias {
       case "computer_use":
           return "Computer" // Map to internal tool name
       // ...
       }
   }
   ```

4. **Add Version History** (`anthropic.go`)
   ```go
   ToolVersionHistory["computer_use"] = []ToolVersionInfo{...}
   ```

## Testing Strategy

### Unit Tests
- **Constants validation**: Verify all constants are properly defined
- **Alias resolution**: Test mapping from aliases to versioned names  
- **Environment overrides**: Test version override functionality
- **Error handling**: Test invalid aliases and malformed inputs

### Integration Tests  
- **Registry integration**: Test with actual tool registry
- **Tool execution**: Verify aliasing works end-to-end
- **Configuration loading**: Test with real configuration files

## Migration Guide

### For Planning Module (Future Work)
⚠️ **Note**: The planning module contains hardcoded tool references that should be updated in a future ticket to avoid import cycles.

Current hardcoded references in `internal/tools/planning/cache.go`:
- Line 334: `case "web_search", "web_fetch"`
- Line 545: `if toolName != "web_search"`
- Line 566: `if toolName != "web_search"`
- Line 590: `if toolName != "web_fetch"`

**Future approach** (requires refactoring to avoid circular imports):
```go
// Create a shared constants package to break the circular dependency
// internal/constants/anthropic.go
package constants

const (
    WebSearchTool = "web_search_20250305"
    WebFetchTool  = "web_fetch_20250305"
)
```

### For New Code
Use the aliasing system for any new tool implementations:
```go
// Good: Use aliasing system
config, err := GetAnthropicTool("web_search")
if err == nil && config.Name == toolName {
    // Handle web search tool
}
```

### For Configuration Files
Update tool references in config files to use aliases where possible.

## Environment Configuration

### Development
```bash
# Use default versions (from constants)
# No environment variables needed
```

### Beta Testing
```bash
# Override specific tools for testing new versions
export ANTHROPIC_TOOL_WEB_SEARCH_VERSION=web_search_20250401
export ANTHROPIC_TOOL_COMPUTER_USE_VERSION=computer_20241025
```

### Production
```bash
# Use default versions for stability
# Override only when explicitly upgrading
```

## Benefits

1. **Centralized Version Management**: Update tool versions in one place
2. **Environment Flexibility**: Override versions for testing without code changes  
3. **Backward Compatibility**: Support both versioned and unversioned references
4. **Type Safety**: Compile-time validation of tool configurations
5. **Easy Migration**: When Anthropic releases new versions, update constants only
6. **Documentation**: Track version history and changes over time

## Future Considerations

- **Automatic Version Detection**: Query Anthropic API for available tool versions
- **Version Negotiation**: Fall back to older versions if newer ones fail
- **A/B Testing**: Split traffic between different tool versions
- **Gradual Rollout**: Phased deployment of new tool versions