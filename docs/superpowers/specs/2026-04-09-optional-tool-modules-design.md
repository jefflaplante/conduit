# Optional Tool Modules Design

**Date:** 2026-04-09  
**Status:** Draft  
**Author:** Jeff LaPlante + Claude

## Context

Conduit has accumulated several "plugin-like" tools (Datadog, Kubernetes, PagerDuty, MQTT, SSH, UniFi, SRE) that feel more like add-ons than core platform functionality. Currently, all tools compile into every binary regardless of whether they're used.

**Problems this solves:**
- Binary size includes unused tool code
- Larger attack surface than necessary for focused deployments
- No clear architectural boundary between core and optional
- Cannot build deployment-specific variants (SRE-focused, IoT-focused, etc.)

**Goals:**
- Reduce binary size by excluding unused tools at compile time
- Enable deployment flexibility with build variants
- Establish clean boundaries between core and optional tools
- Prepare for future community-contributed external modules

## Design

### Approach: Build Tags + Future Module Readiness

Use Go build tags for current optional tools, with a registration interface designed to also support external Go modules in the future.

**Two-layer control:**
- **Build tags** control whether code is compiled into the binary
- **Config `enabled` flags** control whether compiled tools are active at runtime

### Architecture

```
┌─────────────────────────────────────────┐
│              Registry                    │
│  ┌─────────────────────────────────────┐│
│  │ Core Tools (always compiled)        ││
│  │ read, write, edit, bash, brain...   ││
│  └─────────────────────────────────────┘│
│  ┌─────────────────────────────────────┐│
│  │ Optional Tools (build-tag gated)    ││
│  │ datadog, k8s, pagerduty, mqtt...    ││
│  └─────────────────────────────────────┘│
│  ┌─────────────────────────────────────┐│
│  │ External Tools (future modules)     ││
│  │ via same OptionalToolFactory        ││
│  └─────────────────────────────────────┘│
└─────────────────────────────────────────┘
```

**Registration flow:**
1. `init()` functions run at startup (only for compiled-in tools)
2. They call `registry.RegisterOptional(name, factory)` to register a factory
3. When `SetServices()` is called, the registry instantiates optional tools
4. Config `enabled` flags still control whether a registered tool is usable

### Optional Tool Factory Interface

```go
// internal/tools/types/optional.go
type OptionalToolFactory func(services *ToolServices, cfg *config.Config) (Tool, error)
```

```go
// internal/tools/registry.go additions
var optionalFactories = make(map[string]types.OptionalToolFactory)

func RegisterOptional(name string, factory types.OptionalToolFactory) {
    optionalFactories[name] = factory
}

func (r *Registry) HasTool(name string) bool {
    _, exists := r.tools[name]
    return exists
}

func (r *Registry) ListAvailableOptionalTools() []string
```

### Build Tags

**Convention:** `with_<toolname>` matching the package name.

| Tool | Build Tag | Package |
|------|-----------|---------|
| Datadog | `with_datadog` | `internal/tools/datadog` |
| Kubernetes | `with_k8s` | `internal/tools/k8s` |
| PagerDuty | `with_pagerduty` | `internal/tools/pagerduty` |
| SRE | `with_sre` | `internal/tools/sre` |
| MQTT | `with_mqtt` | `internal/tools/mqtt` |
| SSH | `with_ssh` | `internal/tools/ssh` |
| UniFi | `with_unifi` | `internal/tools/unifi` |

### File Structure

Each optional tool gets a registration file pair:

```
internal/tools/datadog/
├── tool.go              # //go:build with_datadog
├── client.go            # //go:build with_datadog
├── logs.go              # //go:build with_datadog
├── ...                  # //go:build with_datadog (all implementation files)
├── register.go          # //go:build with_datadog — calls RegisterOptional
└── register_stub.go     # //go:build !with_datadog — empty init()
```

**register.go example:**
```go
//go:build with_datadog

package datadog

import "conduit/internal/tools"

func init() {
    tools.RegisterOptional("datadog", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
        if !cfg.Datadog.Enabled {
            return nil, nil // Compiled but disabled via config
        }
        return NewDatadogTool(services, &cfg.Datadog)
    })
}
```

**register_stub.go example:**
```go
//go:build !with_datadog

package datadog

func init() {
    // Tool not compiled - no registration
}
```

**Dependency handling (SRE tool):**
```go
//go:build with_sre

func init() {
    tools.RegisterOptional("sre", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
        if !tools.HasTool("Datadog") || !tools.HasTool("PagerDuty") {
            return nil, fmt.Errorf("SRE tool requires with_datadog and with_pagerduty build tags")
        }
        // ...
    })
}
```

### Makefile Targets

```makefile
# Build tags for optional tools
OPTIONAL_TOOLS := datadog k8s pagerduty sre mqtt ssh unifi
SRE_TOOLS := datadog pagerduty sre
IOT_TOOLS := mqtt unifi

# Convert tool names to build tags
tags_for = $(shell echo $(1) | tr ' ' '\n' | sed 's/^/with_/' | tr '\n' ',' | sed 's/,$$//')

# Default: core only (minimal binary)
build:
	go build -o bin/conduit ./cmd/gateway

# Full build: all optional tools
build-full:
	go build -tags "$(call tags_for,$(OPTIONAL_TOOLS))" -o bin/conduit ./cmd/gateway

# SRE-focused: datadog + pagerduty + sre
build-sre:
	go build -tags "$(call tags_for,$(SRE_TOOLS))" -o bin/conduit-sre ./cmd/gateway

# IoT-focused: mqtt + unifi
build-iot:
	go build -tags "$(call tags_for,$(IOT_TOOLS))" -o bin/conduit-iot ./cmd/gateway

# Custom: specify tools directly
build-custom:
	go build -tags "$(call tags_for,$(TOOLS))" -o bin/conduit ./cmd/gateway

# Production variants
build-prod:
	go build -ldflags="-s -w" -o bin/conduit ./cmd/gateway

build-prod-full:
	go build -ldflags="-s -w" -tags "$(call tags_for,$(OPTIONAL_TOOLS))" -o bin/conduit ./cmd/gateway
```

### Config Interaction

**Config structs stay in place.** The `internal/config/` package doesn't get build tags. Config files parse the same way regardless of build variant.

**Validation with warnings:**
```go
func (c *Config) Validate() error {
    // Warn (don't error) if tool enabled but not compiled
    if c.Datadog.Enabled && !registry.HasTool("Datadog") {
        log.Printf("Warning: datadog.enabled=true but tool not compiled (missing with_datadog tag)")
    }
}
```

**Runtime discovery via /health or /tools:**
```json
{
  "core_tools": ["read", "write", "edit", "bash", "brain"],
  "optional_tools_compiled": ["datadog", "k8s"],
  "optional_tools_enabled": ["datadog"]
}
```

### Testing Strategy

**Unit tests follow their tool's build tag:**
```go
// internal/tools/datadog/tool_test.go
//go:build with_datadog

package datadog
```

**Makefile test targets:**
```makefile
test:           # Core only
test-full:      # All optional tools
test-datadog:   # Specific tool
```

**CI matrix tests multiple build configurations:** core, full, sre.

### Safety Model

**Why this design cannot segfault:**
1. If a tool isn't compiled, its `init()` never runs, so the factory is never registered
2. Registry lookup returns errors, not nil pointers
3. Config structs always exist (not build-tagged)
4. No direct code paths assume tools exist

### Future: External Modules

The `OptionalToolFactory` interface supports external Go modules:

```go
// github.com/someone/conduit-alertmanager/tool.go
package alertmanager

import "conduit/internal/tools/types"

func New(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
    return &AlertManagerTool{...}, nil
}
```

```go
// cmd/gateway/plugins_alertmanager.go
//go:build with_alertmanager

import alertmanager "github.com/someone/conduit-alertmanager"

func init() {
    tools.RegisterOptional("alertmanager", alertmanager.New)
}
```

External tools use `tools.services` map for config:
```json
{
  "tools": {
    "services": {
      "alertmanager": {
        "api_url": "https://alertmanager.example.com"
      }
    }
  }
}
```

**Future work (not in scope):**
- Tool SDK package to avoid `internal/` imports
- Tool scaffolding CLI
- Community tool registry

## Critical Files to Modify

- `internal/tools/types/types.go` — Add `OptionalToolFactory` type
- `internal/tools/registry.go` — Add optional registration, `HasTool()`, `ListAvailableOptionalTools()`
- `internal/tools/datadog/*.go` — Add build tags, create register.go/register_stub.go
- `internal/tools/k8s/*.go` — Same pattern
- `internal/tools/pagerduty/*.go` — Same pattern
- `internal/tools/sre/*.go` — Same pattern with dependency check
- `internal/tools/mqtt/*.go` — Same pattern
- `internal/tools/ssh/*.go` — Same pattern
- `internal/tools/unifi.go` — Move to `internal/tools/unifi/` directory, same pattern
- `internal/config/config.go` — Add warning for enabled-but-not-compiled tools
- `Makefile` — Add build-full, build-sre, build-iot, build-custom, test variants

## Verification

1. **Build variants produce different sizes:**
   ```bash
   make build && ls -la bin/conduit        # Core only
   make build-full && ls -la bin/conduit   # Should be larger
   ```

2. **Missing tools return clean errors:**
   ```bash
   make build  # Core only
   ./bin/conduit tools list  # Should not show datadog
   # AI calling Datadog tool should get "tool not found" error
   ```

3. **Config warnings for mismatched builds:**
   ```bash
   make build  # Core only
   # With datadog.enabled=true in config
   ./bin/conduit  # Should log warning about missing tool
   ```

4. **Tests pass for each build variant:**
   ```bash
   make test        # Core tests pass
   make test-full   # All tests pass
   ```

5. **SRE tool fails gracefully without dependencies:**
   ```bash
   go build -tags with_sre -o bin/conduit ./cmd/gateway
   # Should fail at startup: "SRE tool requires with_datadog and with_pagerduty"
   ```
