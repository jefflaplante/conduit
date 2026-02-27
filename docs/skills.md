# Skills System Documentation

Conduit Go includes a comprehensive skill system that extends AI capabilities through modular, reusable components. This system is fully compatible with the TypeScript Conduit skill format while providing enhanced performance and security through native Go implementation.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Skill Discovery](#skill-discovery)
- [SKILL.md Format](#skillmd-format)
- [Creating Skills](#creating-skills)
- [Configuration](#configuration)
- [Tool Integration](#tool-integration)
- [Security & Sandboxing](#security--sandboxing)
- [Performance & Caching](#performance--caching)
- [Examples](#examples)
- [Troubleshooting](#troubleshooting)

## Overview

The Conduit Go skills system provides:

- **Auto-Discovery** - Automatically finds and loads skills from configured directories
- **TypeScript Compatibility** - Full backward compatibility with existing TypeScript Conduit skills
- **Process Isolation** - Secure execution with configurable timeouts and resource limits
- **Native Performance** - Go-based implementation with efficient caching
- **Tool Integration** - Skills generate tools that integrate seamlessly with the AI system
- **Requirement Validation** - Automatic checking of dependencies, environment variables, and prerequisites

### Key Benefits

| Feature | TypeScript Version | Go Version | Improvement |
|---------|-------------------|------------|-------------|
| Discovery Speed | ~500ms | ~50ms | **90% faster** |
| Memory Usage | 20MB per skill | 2MB per skill | **90% less** |
| Execution Timeout | Basic | Configurable | **Better control** |
| Caching | None | TTL-based | **Performance boost** |
| Error Handling | Basic | Comprehensive | **Better reliability** |

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Skills System                            │
│                                                             │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                Skill Manager                           │  │
│  │    • Discovery coordination                            │  │
│  │    • Lifecycle management                              │  │
│  │    • Caching with TTL                                  │  │
│  │    • Configuration management                          │  │
│  └───────────────────┬───────────────────────────────────┘  │
│                      │                                       │
│      ┌───────────────┼───────────────┐                      │
│      ▼               ▼               ▼                      │
│  ┌─────────┐   ┌─────────┐   ┌─────────────┐                │
│  │Discovery│   │ Loader  │   │  Executor   │                │
│  │         │   │         │   │             │                │
│  │• Search │   │• Parse  │   │• Subprocess │                │
│  │  paths  │   │  YAML   │   │• Timeout    │                │
│  │• Find   │   │• Load   │   │• Security   │                │
│  │  skills │   │  content│   │• Results    │                │
│  └─────────┘   └─────────┘   └─────────────┘                │
│                      │                                       │
│                      ▼                                       │
│  ┌───────────────────────────────────────────────────────┐  │
│  │               Tool Integration                         │  │
│  │    • Generate SkillToolInterface instances             │  │
│  │    • Action extraction from skill content              │  │
│  │    • Integration with broader tool ecosystem           │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### Core Components

1. **Skill Manager** (`internal/skills/manager.go`) - Orchestrates discovery, caching, and execution
2. **Discovery Engine** (`internal/skills/discovery.go`) - Finds skills in configured search paths
3. **Loader** (`internal/skills/loader.go`) - Parses SKILL.md files and validates structure
4. **Executor** (`internal/skills/executor.go`) - Handles secure skill execution
5. **Tool Integration** (`internal/skills/integration.go`) - Generates tools for the AI system
6. **Validator** (`internal/skills/validator.go`) - Checks requirements and dependencies

## Skill Discovery

### Search Paths

Skills are discovered from multiple search paths in priority order:

1. **TypeScript Bundled Skills** - `/usr/local/lib/conduit/skills/`
   - Pre-built skills from the TypeScript version
   - Full compatibility maintained

2. **Local User Skills** - `~/.local/share/conduit/skills/`
   - Custom user-defined skills
   - Project-specific capabilities

3. **System-Wide Skills** - `/opt/conduit/skills/`
   - System administrator installed skills
   - Shared across all users

### Discovery Process

```go
// Discovery flow
1. Manager.Initialize() → discovery.DiscoverSkills()
2. For each search path:
   a. Find directories containing SKILL.md
   b. Load and parse SKILL.md files
   c. Validate skill structure and requirements
   d. Cache results with TTL
3. Generate tool interfaces for valid skills
4. Register with tool system
```

### Configuration

```json
{
  "skills": {
    "search_paths": [
      "/usr/local/lib/conduit/skills",
      "~/.local/share/conduit/skills",
      "/opt/conduit/skills",
      "./skills"
    ],
    "cache_ttl_minutes": 30,
    "discovery_timeout_seconds": 10,
    "max_concurrent_discoveries": 5
  }
}
```

## SKILL.md Format

Skills use a standardized YAML frontmatter format followed by markdown documentation.

### Basic Structure

```yaml
---
name: skill-name
description: Brief description of what this skill does
metadata:
  conduit:
    emoji: 🚀
    version: "1.0.0"
    author: "Your Name"
    requires:
      bins: ["curl", "jq"]
      files: ["/etc/hosts"]
      env: ["API_KEY", "SECRET"]
---

# Skill Name

Detailed documentation about the skill...

## Usage

How to use the skill...

## Examples

Example interactions...
```

### Complete Schema

```yaml
---
name: string                    # Required: Unique skill identifier
description: string             # Required: Brief description for AI context
metadata:                      # Optional: Additional metadata
  conduit:
    emoji: string              # Optional: Emoji for UI representation
    version: string            # Optional: Semantic version
    author: string             # Optional: Author information
    tags: [string]             # Optional: Classification tags
    requires:                  # Optional: Requirements validation
      bins: [string]           # Required binary executables
      files: [string]          # Required files or directories
      env: [string]            # Required environment variables
      os: [string]             # Required operating systems
    config:                    # Optional: Skill-specific config
      timeout_seconds: number  # Custom execution timeout
      memory_limit_mb: number  # Memory limit for execution
      allowed_paths: [string]  # Sandbox path restrictions
---

# Skill Documentation

The markdown content provides:
- Detailed usage instructions
- Code examples
- Implementation details
- Troubleshooting guides
```

### Field Descriptions

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `name` | ✅ | string | Unique identifier (kebab-case recommended) |
| `description` | ✅ | string | Brief description for AI context (1-2 sentences) |
| `emoji` | ❌ | string | Single emoji for UI representation |
| `version` | ❌ | string | Semantic version (e.g., "1.2.3") |
| `author` | ❌ | string | Author name or organization |
| `tags` | ❌ | array | Classification tags (e.g., ["automation", "weather"]) |
| `requires.bins` | ❌ | array | Required command-line tools |
| `requires.files` | ❌ | array | Required files or directories |
| `requires.env` | ❌ | array | Required environment variables |
| `requires.os` | ❌ | array | Required operating systems ("linux", "darwin", "windows") |

## Creating Skills

### Step 1: Create Directory Structure

```bash
# Create skill directory
mkdir ~/.local/skills/my-weather-skill
cd ~/.local/skills/my-weather-skill

# Optional: Add supporting files
mkdir scripts references assets
```

### Step 2: Create SKILL.md

```yaml
---
name: my-weather
description: Get weather information for any location using wttr.in service
metadata:
  conduit:
    emoji: 🌤️
    version: "1.0.0"
    author: "Your Name"
    tags: ["weather", "information"]
    requires:
      bins: ["curl"]
      env: ["WTTR_FORMAT"]
---

# My Weather Skill

This skill provides current weather information and forecasts for any location worldwide using the wttr.in service.

## Features

- Current weather conditions
- 3-day forecast
- No API key required
- Global location support
- Customizable output format

## Usage

Ask for weather information naturally:

- "What's the weather like in Seattle?"
- "Show me the forecast for London"
- "Is it raining in Tokyo?"
- "Temperature in New York"

## Configuration

Set the `WTTR_FORMAT` environment variable to customize output:

```bash
export WTTR_FORMAT="%l:+%c+%t+%h+%w"
```

Format options:
- `%l` - Location
- `%c` - Weather condition
- `%t` - Temperature
- `%h` - Humidity
- `%w` - Wind

## Implementation

The skill makes HTTP requests to `wttr.in` with location parameters:

```bash
curl "wttr.in/Seattle?format=%l:+%c+%t"
```

## Examples

### Current Weather
**User**: "What's the weather in Portland?"
**Response**: "Portland: ⛅ 18°C, humidity 65%, wind 5 mph SW"

### Forecast
**User**: "Weather forecast for this weekend in Miami"
**Response**: Shows 3-day forecast with temperatures and conditions

## Troubleshooting

### Common Issues

1. **No internet connection** - Skill requires internet access to reach wttr.in
2. **Unknown location** - wttr.in will suggest similar locations
3. **Rate limiting** - Service may temporarily limit requests

### Error Messages

- `curl: command not found` - Install curl: `apt-get install curl`
- `Connection timeout` - Check internet connectivity
```

### Step 3: Add Supporting Files (Optional)

```bash
# Add executable scripts
echo '#!/bin/bash
curl "wttr.in/$1?format=%l:+%c+%t+%h+%w"' > scripts/get-weather.sh
chmod +x scripts/get-weather.sh

# Add reference documentation
echo "# Weather API Reference
wttr.in format codes and usage examples" > references/api-docs.md

# Add configuration examples
echo "# Example configs for different use cases" > references/config-examples.md
```

### Step 4: Test the Skill

```bash
# Test skill discovery
cd $PROJECT_DIR
./bin/gateway --config config.json --test-skills

# Check skill validation
./bin/gateway --validate-skill ~/.local/skills/my-weather-skill
```

## Configuration

### Gateway Configuration

Skills are configured in the main gateway configuration file:

```json
{
  "skills": {
    "enabled": true,
    "search_paths": [
      "/usr/local/lib/conduit/skills",
      "~/.local/share/conduit/skills",
      "~/.local/skills",
      "/opt/conduit/skills"
    ],
    "cache_ttl_minutes": 30,
    "discovery_timeout_seconds": 10,
    "execution": {
      "timeout_seconds": 300,
      "memory_limit_mb": 128,
      "allowed_paths": [
        "~/workspace",
        "/tmp",
        "."
      ]
    },
    "validation": {
      "check_requirements": true,
      "strict_mode": false,
      "allowed_os": ["linux", "darwin"]
    },
    "security": {
      "sandbox_enabled": true,
      "network_access": true,
      "file_system_access": "restricted"
    }
  }
}
```

### Configuration Options

#### Discovery Settings

- `enabled` - Enable/disable the entire skills system
- `search_paths` - Array of directories to search for skills
- `cache_ttl_minutes` - How long to cache discovered skills
- `discovery_timeout_seconds` - Timeout for skill discovery process
- `max_concurrent_discoveries` - Parallel discovery limit

#### Execution Settings

- `timeout_seconds` - Default execution timeout for skills
- `memory_limit_mb` - Memory limit for skill processes
- `allowed_paths` - File system paths accessible to skills

#### Validation Settings

- `check_requirements` - Validate skill requirements on discovery
- `strict_mode` - Fail on any requirement validation error
- `allowed_os` - Restrict skills to specific operating systems

#### Security Settings

- `sandbox_enabled` - Enable process sandboxing
- `network_access` - Allow skills to make network requests
- `file_system_access` - Control file system access ("none", "restricted", "full")

### Environment Variables

Skills can reference environment variables for configuration:

```bash
# Common skill environment variables
export WEATHER_API_KEY="your_key_here"
export DISCORD_BOT_TOKEN="your_token"
export HOME_ASSISTANT_URL="http://homeassistant.local:8123"
export HOME_ASSISTANT_TOKEN="your_ha_token"

# System paths
export SKILLS_PATH="~/.local/skills:/opt/conduit/skills"
export SKILL_CACHE_TTL="1800"  # 30 minutes in seconds
```

## Tool Integration

Skills automatically generate tools that integrate with the AI system. The integration process creates `SkillToolInterface` instances that the AI can invoke.

### Tool Generation Process

```go
// From internal/skills/integration.go
1. Skill discovered and loaded
2. Extract actions from skill content
3. Generate tool interface with:
   - Name: skill name
   - Description: skill description
   - Parameters: extracted from skill content
   - Execute: calls skill executor
4. Register with tool registry
5. Available to AI for invocation
```

### Tool Interface

Each skill generates a tool with this interface:

```go
type SkillToolInterface struct {
    SkillName   string
    Description string
    Parameters  map[string]interface{}
}

func (sti *SkillToolInterface) Execute(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
    // Calls skill executor with provided arguments
    return skillManager.ExecuteSkill(ctx, sti.SkillName, "default", args)
}
```

### AI Invocation

The AI can invoke skills through natural language:

```
User: "What's the weather like in Seattle?"

AI Processing:
1. Identifies need for weather information
2. Searches available tools
3. Finds "weather" skill tool
4. Invokes: weather.Execute({"location": "Seattle"})
5. Returns formatted weather information
```

### Custom Actions

Skills can define custom actions by including action sections in their markdown:

```yaml
---
name: advanced-weather
description: Advanced weather skill with multiple actions
---

# Advanced Weather Skill

## Actions

### current
Get current weather conditions for a location.

**Parameters:**
- `location` (string, required): Location name or coordinates
- `units` (string, optional): Temperature units ("celsius", "fahrenheit")

### forecast
Get weather forecast for a location.

**Parameters:**
- `location` (string, required): Location name
- `days` (number, optional): Number of forecast days (1-7, default: 3)

### alerts
Get weather alerts for a location.

**Parameters:**
- `location` (string, required): Location name
- `severity` (string, optional): Alert severity filter ("minor", "moderate", "severe")
```

This creates three separate tools: `advanced-weather-current`, `advanced-weather-forecast`, and `advanced-weather-alerts`.

## Security & Sandboxing

The Go skills system includes comprehensive security measures to ensure safe execution of skills.

### Process Isolation

Each skill executes in an isolated subprocess with:

- **Timeout Protection** - Configurable execution timeouts prevent runaway processes
- **Memory Limits** - Process memory usage is monitored and limited
- **CPU Limits** - CPU usage can be constrained (Linux only)
- **File System Restrictions** - Access limited to allowed paths only
- **Network Policies** - Network access can be controlled or disabled

### Sandboxing Configuration

```json
{
  "skills": {
    "security": {
      "sandbox_enabled": true,
      "execution": {
        "timeout_seconds": 300,
        "memory_limit_mb": 128,
        "cpu_limit_percent": 50,
        "allowed_paths": [
          "~/workspace",
          "/tmp/conduit-skills",
          "/usr/bin",
          "/bin"
        ],
        "denied_paths": [
          "~/.ssh",
          "/etc/passwd",
          "/root"
        ],
        "network_access": "restricted",
        "environment_isolation": true
      }
    }
  }
}
```

### Requirement Validation

Skills undergo validation before execution:

```go
// Validation checks
1. Required binaries exist and are executable
2. Required files/directories exist and are accessible
3. Required environment variables are set
4. Operating system compatibility
5. Minimum system requirements met
```

### Security Best Practices

1. **Principle of Least Privilege** - Skills receive minimal necessary permissions
2. **Input Validation** - All skill inputs are validated and sanitized
3. **Output Sanitization** - Skill outputs are cleaned before returning to AI
4. **Audit Logging** - All skill executions are logged with timestamps and parameters
5. **Regular Updates** - Skills should be updated regularly for security patches

### Dangerous Operations

Some operations require special consideration:

```yaml
---
name: dangerous-skill
description: Skill that performs potentially dangerous operations
metadata:
  conduit:
    warnings:
      - "This skill modifies system files"
      - "Requires root privileges"
      - "Can affect system stability"
    permissions:
      - "file-write"
      - "system-modify"
      - "network-access"
---
```

## Performance & Caching

The Go skills system includes comprehensive caching and performance optimizations.

### Caching Layers

1. **Skill Discovery Cache** - Caches discovered skills with TTL
2. **Metadata Cache** - Caches parsed SKILL.md content
3. **Validation Cache** - Caches requirement validation results
4. **Execution Result Cache** - Caches skill execution results (optional)

### Cache Configuration

```json
{
  "skills": {
    "cache": {
      "discovery_ttl_minutes": 30,
      "metadata_ttl_minutes": 60,
      "validation_ttl_minutes": 120,
      "execution_ttl_minutes": 5,
      "max_cache_size_mb": 50,
      "cleanup_interval_minutes": 15
    }
  }
}
```

### Performance Monitoring

```go
// Performance metrics available
- Skill discovery time
- Cache hit/miss ratios
- Execution time per skill
- Memory usage per execution
- Concurrent execution count
```

### Optimization Tips

1. **Use Caching** - Enable result caching for expensive operations
2. **Minimize Dependencies** - Fewer requirements = faster validation
3. **Optimize Scripts** - Use efficient algorithms in skill scripts
4. **Batch Operations** - Group related operations when possible
5. **Monitor Resources** - Track memory and CPU usage

## Examples

### Example 1: Simple Information Skill

**Directory Structure:**
```
skills/system-info/
├── SKILL.md
└── scripts/
    └── get-system-info.sh
```

**SKILL.md:**
```yaml
---
name: system-info
description: Get basic system information (OS, CPU, memory, disk usage)
metadata:
  conduit:
    emoji: 💻
    version: "1.0.0"
    requires:
      bins: ["uname", "df", "free"]
      os: ["linux", "darwin"]
---

# System Info Skill

Get basic system information including OS details, memory usage, and disk space.

## Usage

- "Show me system information"
- "What's the current memory usage?"
- "How much disk space is available?"

## Implementation

Uses standard Unix commands to gather system information:
- `uname -a` for OS details
- `free -h` for memory information
- `df -h` for disk usage
```

**scripts/get-system-info.sh:**
```bash
#!/bin/bash
echo "=== System Information ==="
echo "OS: $(uname -s) $(uname -r)"
echo "Architecture: $(uname -m)"
echo ""
echo "=== Memory Usage ==="
free -h
echo ""
echo "=== Disk Usage ==="
df -h /
```

### Example 2: API Integration Skill

**Directory Structure:**
```
skills/github-status/
├── SKILL.md
├── scripts/
│   └── check-github.py
└── references/
    └── github-api.md
```

**SKILL.md:**
```yaml
---
name: github-status
description: Check GitHub service status and recent incidents
metadata:
  conduit:
    emoji: 🐙
    version: "1.0.0"
    author: "Jeff LaPlante"
    tags: ["api", "status", "github"]
    requires:
      bins: ["python3", "curl"]
      env: ["GITHUB_TOKEN"]
    config:
      timeout_seconds: 30
      network_access: true
---

# GitHub Status Skill

Monitor GitHub service status and retrieve information about recent incidents or outages.

## Features

- Current GitHub service status
- Recent incident reports
- Component-specific status
- Historical uptime data

## Usage

- "What's GitHub's current status?"
- "Are there any GitHub outages?"
- "Check if GitHub Actions is working"
- "Show recent GitHub incidents"

## Configuration

Set your GitHub token (optional, increases rate limits):

```bash
export GITHUB_TOKEN="ghp_your_personal_access_token"
```

## Examples

**User**: "Is GitHub down?"
**Response**: "GitHub services are operational. All systems normal as of 2026-02-13 02:15 UTC."

**User**: "Any recent GitHub incidents?"
**Response**: "Last incident: API degradation on Feb 10, 14:30-15:45 UTC. Resolved. No current issues."
```

**scripts/check-github.py:**
```python
#!/usr/bin/env python3
import requests
import json
import os
from datetime import datetime

def get_github_status():
    """Get current GitHub status from status API."""
    url = "https://www.githubstatus.com/api/v2/status.json"
    headers = {}
    
    # Add auth token if available
    if os.getenv('GITHUB_TOKEN'):
        headers['Authorization'] = f"token {os.getenv('GITHUB_TOKEN')}"
    
    try:
        response = requests.get(url, headers=headers, timeout=10)
        response.raise_for_status()
        data = response.json()
        
        status = data.get('status', {})
        indicator = status.get('indicator', 'unknown')
        description = status.get('description', 'Unknown status')
        
        print(f"GitHub Status: {description}")
        print(f"Indicator: {indicator}")
        
        if indicator != 'none':
            print("⚠️  There may be service disruptions")
        else:
            print("✅ All systems operational")
            
        # Get recent incidents
        incidents_url = "https://www.githubstatus.com/api/v2/incidents.json"
        incidents_response = requests.get(incidents_url, headers=headers, timeout=10)
        incidents_data = incidents_response.json()
        
        recent_incidents = incidents_data.get('incidents', [])[:3]
        if recent_incidents:
            print("\n=== Recent Incidents ===")
            for incident in recent_incidents:
                name = incident.get('name', 'Unknown incident')
                status = incident.get('status', 'unknown')
                created_at = incident.get('created_at', '')
                print(f"• {name} - {status} ({created_at[:10]})")
        
    except requests.exceptions.RequestException as e:
        print(f"Error fetching GitHub status: {e}")
        return 1
    except json.JSONDecodeError as e:
        print(f"Error parsing GitHub status response: {e}")
        return 1
    
    return 0

if __name__ == "__main__":
    exit(get_github_status())
```

### Example 3: Multi-Action Skill

**Directory Structure:**
```
skills/docker-manager/
├── SKILL.md
├── scripts/
│   ├── list-containers.sh
│   ├── container-status.sh
│   └── container-logs.sh
└── references/
    └── docker-commands.md
```

**SKILL.md:**
```yaml
---
name: docker-manager
description: Manage Docker containers, images, and get system information
metadata:
  conduit:
    emoji: 🐳
    version: "1.0.0"
    tags: ["docker", "containers", "devops"]
    requires:
      bins: ["docker"]
      files: ["/var/run/docker.sock"]
    config:
      timeout_seconds: 60
---

# Docker Manager Skill

Manage Docker containers and get container runtime information.

## Actions

### list
List all containers with status information.

**Parameters:**
- `all` (boolean, optional): Include stopped containers (default: false)
- `format` (string, optional): Output format ("table", "json", default: "table")

### status
Get detailed status for a specific container.

**Parameters:**
- `container` (string, required): Container name or ID
- `follow` (boolean, optional): Follow log output (default: false)

### logs
Get logs for a specific container.

**Parameters:**
- `container` (string, required): Container name or ID
- `lines` (number, optional): Number of log lines to show (default: 50)
- `follow` (boolean, optional): Follow log output (default: false)

### stats
Get resource usage statistics for containers.

**Parameters:**
- `container` (string, optional): Specific container, or all if not specified

## Usage Examples

- "List all running containers"
- "Show status for nginx container"
- "Get logs for the last 100 lines from web-app"
- "Show resource usage for all containers"

## Security Note

This skill requires access to the Docker socket and should only be used in trusted environments.
```

## Troubleshooting

### Common Issues

#### Skill Not Discovered

**Symptoms:**
- Skill doesn't appear in available tools
- AI can't invoke the skill

**Solutions:**
1. Check skill directory path is in `search_paths`
2. Verify `SKILL.md` file exists and is readable
3. Validate YAML frontmatter syntax
4. Check file permissions (must be readable)
5. Review discovery logs for errors

**Debug Commands:**
```bash
# Test skill discovery
./bin/gateway --test-skills --verbose

# Validate specific skill
./bin/gateway --validate-skill /path/to/skill/

# Check discovery logs
./bin/gateway --config config.json --log-level debug | grep skills
```

#### Requirement Validation Failures

**Symptoms:**
- "Skill requirements not met" errors
- Skill discovered but not available

**Solutions:**
1. Install required binaries: `which curl` or `apt-get install curl`
2. Set required environment variables: `export API_KEY="value"`
3. Create required files/directories
4. Check operating system compatibility

**Debug Commands:**
```bash
# Check binary availability
which curl jq python3

# Verify environment variables
env | grep -E "(API_KEY|TOKEN)"

# Test file access
ls -la /path/to/required/file
```

#### Execution Timeouts

**Symptoms:**
- "Skill execution timed out" errors
- Slow skill responses

**Solutions:**
1. Increase `execution.timeout_seconds` in config
2. Optimize skill scripts for performance
3. Check network connectivity for API-dependent skills
4. Review resource limits (memory, CPU)

**Configuration:**
```json
{
  "skills": {
    "execution": {
      "timeout_seconds": 600,  // Increase from default 300
      "memory_limit_mb": 256   // Increase from default 128
    }
  }
}
```

#### Permission Errors

**Symptoms:**
- "Permission denied" errors
- File access failures

**Solutions:**
1. Check file permissions on skill directory
2. Ensure scripts are executable: `chmod +x script.sh`
3. Verify sandbox `allowed_paths` includes necessary directories
4. Run with appropriate user permissions

**Debug Commands:**
```bash
# Check permissions
ls -la skill-directory/
ls -la skill-directory/scripts/

# Make scripts executable
chmod +x skill-directory/scripts/*.sh

# Test script execution
cd skill-directory && ./scripts/test-script.sh
```

#### Memory or Resource Limits

**Symptoms:**
- "Memory limit exceeded" errors
- "Resource temporarily unavailable"

**Solutions:**
1. Increase `memory_limit_mb` in configuration
2. Optimize skill scripts to use less memory
3. Check system resources: `free -h`, `top`
4. Review concurrent execution limits

**Configuration:**
```json
{
  "skills": {
    "execution": {
      "memory_limit_mb": 512,
      "max_concurrent_executions": 3
    }
  }
}
```

### Debug Mode

Enable debug logging to troubleshoot skills:

```bash
# Run gateway with debug logging
./bin/gateway --config config.json --log-level debug --verbose

# Filter skill-related logs
./bin/gateway --config config.json --log-level debug 2>&1 | grep -i skill
```

### Skill Testing

Test skills in isolation:

```bash
# Test skill validation
cd /path/to/skill/
python3 -c "
import yaml
with open('SKILL.md') as f:
    content = f.read()
    frontmatter = content.split('---')[1]
    parsed = yaml.safe_load(frontmatter)
    print('Skill name:', parsed.get('name'))
    print('Description:', parsed.get('description'))
"

# Test script execution
cd skill-directory/
timeout 30s ./scripts/main-script.sh

# Test with restricted environment
env -i PATH=/usr/bin:/bin timeout 30s ./scripts/main-script.sh
```

### Performance Debugging

Monitor skill performance:

```bash
# Monitor execution times
./bin/gateway --config config.json --metrics-enabled 2>&1 | grep "skill_execution_duration"

# Check cache performance
./bin/gateway --config config.json --cache-stats

# Monitor resource usage
./bin/gateway --config config.json --resource-monitoring &
top -p $!
```

### Getting Help

1. **Check Logs** - Review gateway logs for error messages
2. **Validate Configuration** - Use `--validate-config` flag
3. **Test Isolation** - Test skills outside the gateway
4. **Community Support** - Check GitHub issues and documentation
5. **Debug Mode** - Enable verbose logging for detailed information

For additional support, see the main Conduit documentation or file issues on the GitHub repository.

---

*Complete skills documentation for Conduit Go Gateway - Updated 2026-02-13*