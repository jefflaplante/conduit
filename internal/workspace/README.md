# Workspace Context Loader

The workspace package provides automatic loading and injection of workspace context files (SOUL.md, USER.md, AGENTS.md, MEMORY.md, etc.) into the Conduit agent system.

## Features

- **Automatic File Discovery**: Finds and loads standard workspace context files
- **Security Filtering**: Restricts access to sensitive files based on session type
- **Efficient Caching**: In-memory caching with TTL and size limits
- **Graceful Degradation**: Continues working when context files are missing
- **Configurable**: Flexible configuration for different deployment scenarios

## Quick Start

```go
import "conduit/internal/workspace"

// Create workspace context manager
manager, err := workspace.NewManagerWithDefaults("/path/to/workspace")
if err != nil {
    log.Fatal(err)
}

// Load context for a session
bundle, err := manager.LoadContextForSession(
    ctx,
    "main",           // session type
    "",               // channel ID
    "user123",        // user ID  
    "session456",     // session ID
)
if err != nil {
    log.Fatal(err)
}

// Access loaded files
if soulContent, exists := bundle.Files["SOUL.md"]; exists {
    fmt.Printf("Agent personality: %s\n", soulContent)
}
```

## Architecture

### Core Components

1. **WorkspaceContext**: Main service for file discovery and loading
2. **SecurityManager**: Handles access control and filtering
3. **FileCache**: Provides efficient caching with TTL and size limits
4. **Manager**: High-level interface for easy integration

### File Types

The system automatically discovers and loads these workspace files:

- `SOUL.md` - Agent personality and identity (all sessions)
- `USER.md` - Information about the human (all sessions)
- `AGENTS.md` - Operational instructions (all sessions) 
- `TOOLS.md` - Local tool configuration (all sessions)
- `MEMORY.md` - Long-term memory (main sessions only)
- `HEARTBEAT.md` - Heartbeat instructions (main sessions only)
- `memory/YYYY-MM-DD.md` - Daily memory files (all sessions, recent days only)

## Security Model

The workspace context loader implements a security model that respects session boundaries:

### Session Types

- **main**: Direct conversation with the human - has access to all files including MEMORY.md
- **shared**: Group conversations - restricted access, no sensitive files
- **isolated**: Sandboxed sessions - minimal context

### Access Rules

```go
// MEMORY.md only in main sessions
{
    Pattern: "MEMORY.md",
    Condition: func(sc SecurityContext) bool {
        return sc.SessionType == "main"
    },
}

// Daily memory files available to all sessions for continuity
{
    Pattern: "memory/*.md", 
    Condition: func(sc SecurityContext) bool {
        return true
    },
}
```

## Configuration

### Default Configuration

```json
{
  "context_dir": "./workspace",
  "files": {
    "core": ["SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md", "MEMORY.md", "HEARTBEAT.md"],
    "memory": {
      "enabled": true,
      "daily_lookback_days": 2
    },
    "max_file_size_kb": 500
  },
  "security": {
    "enforce_access_rules": true,
    "memory_main_only": true,
    "strict_mode": false
  },
  "caching": {
    "enabled": true,
    "ttl_seconds": 300,
    "max_cache_size_mb": 50,
    "watch_file_changes": false
  }
}
```

### Custom Configuration

```go
config := &workspace.Config{
    ContextDir: "/custom/workspace",
    Files: workspace.FilesConfig{
        Core: []string{"SOUL.md", "USER.md"},
        Memory: workspace.MemoryConfig{
            Enabled:           true,
            DailyLookbackDays: 3,
        },
        MaxFileSizeKB: 1000,
    },
    Security: workspace.SecurityConfig{
        EnforceAccessRules: true,
        MemoryMainOnly:     true,
    },
    Caching: workspace.CachingConfig{
        Enabled:        true,
        TTLSeconds:     600,
        MaxCacheSizeMB: 100,
    },
}

manager, err := workspace.NewManager(config)
```

## Integration with Agent System

When the agent system (ticket #007) is complete, integration will look like:

```go
// In agent system
func (a *ConduitAgent) BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]SystemBlock, error) {
    // Load workspace context
    bundle, err := a.workspace.LoadContextForSession(
        ctx,
        session.Type,
        session.ChannelID, 
        session.UserID,
        session.ID,
    )
    if err != nil {
        return nil, err
    }

    blocks := []SystemBlock{}
    
    // Add identity
    blocks = append(blocks, a.buildIdentityBlock())
    
    // Add workspace context files
    if soulContent, exists := bundle.Files["SOUL.md"]; exists {
        blocks = append(blocks, SystemBlock{
            Type: "personality",
            Text: fmt.Sprintf("## Your Personality\n%s", soulContent),
        })
    }
    
    // ... continue for other files
    
    return blocks, nil
}
```

## Performance

### Caching Strategy

- **TTL-based expiration**: Default 5 minutes
- **Size-based eviction**: LRU eviction when cache exceeds size limit
- **Lazy loading**: Files loaded only when accessed
- **Cache invalidation**: Manual invalidation for file updates

### Benchmarks

Target performance (with caching enabled):
- Context loading: <100ms for typical workspace
- Memory usage: <50MB cache size limit
- File discovery: <10ms for standard workspace layout

## Testing

The package includes comprehensive test coverage:

```bash
go test ./internal/workspace/...
```

### Test Categories

- **Security filtering**: Verify MEMORY.md restricted to main sessions
- **File discovery**: Test automatic discovery of context files
- **Caching**: Verify TTL expiration and size limits
- **Error handling**: Graceful handling of missing files
- **Configuration**: Validate config parsing and defaults

## Error Handling

The system is designed to degrade gracefully:

- **Missing files**: Continue loading other available files
- **Read errors**: Log errors but don't fail the entire context load
- **Invalid configuration**: Provide sensible defaults
- **Cache errors**: Fall back to direct file system access

## Security Considerations

- **Path sanitization**: Prevents directory traversal attacks
- **File size limits**: Prevents memory exhaustion
- **Access control**: Session-based file filtering
- **No code execution**: Only loads text content, no file execution

## Monitoring

### Health Checks

```go
health := manager.Health()
fmt.Printf("Health: %+v\n", health)
```

### Cache Statistics

```go
stats := manager.GetProvider().GetCacheStats()
fmt.Printf("Cache utilization: %.1f%%\n", stats["utilization"].(float64)*100)
```

## Future Enhancements

- **File watching**: Automatic cache invalidation on file changes
- **Remote workspaces**: Support for remote/networked workspace directories
- **Compression**: Compress cached content to reduce memory usage
- **Metrics**: Detailed performance and usage metrics
- **Validation**: Content validation for workspace files

## Contributing

When making changes to the workspace context loader:

1. Ensure security model is preserved
2. Add tests for new functionality
3. Update configuration schema if needed
4. Maintain backward compatibility
5. Document new features in this README