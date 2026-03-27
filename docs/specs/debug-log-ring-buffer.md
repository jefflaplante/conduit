# Spec: Debug Log Ring Buffer & Log Hygiene

**Author:** Jules  
**Date:** 2026-03-27  
**Status:** Draft  
**Ticket:** N/A (security/privacy improvement)

---

## Problem

Conduit logs tool names, full argument payloads, and LLM metadata via `log.Printf` at INFO level. These appear in `journalctl -u conduit` and contain:

- Full bash commands executed by the LLM
- File contents being written or read
- Search queries (web, memory)
- Thinking indicators
- Tool argument maps with potentially private data

This is a **security and privacy concern** — anyone with journal access (or log aggregation) sees everything the LLM does and thinks.

Meanwhile, there's no way for the LLM (me) to introspect recent tool executions without asking the user to grep logs.

## Goals

1. **Remove sensitive data from journal logs.** Tool args, results, and LLM thinking should NOT appear at INFO level.
2. **Preserve operational visibility.** Tool names, durations, success/failure, and error messages stay in the journal — just not the payloads.
3. **Capture everything in a private ring buffer.** The LLM can dump the buffer on request via a tool call. It never touches disk.
4. **Respect existing config.** `debug.verbose_logging: true` re-enables the old noisy behavior for development/troubleshooting.

## Non-Goals

- Structured logging (slog migration) — separate effort
- Persistent debug log (file/DB) — ring buffer is intentionally ephemeral
- Rate limiting or sampling of log entries

---

## Current State (as of 2026-03-27)

### Files that exist but are NOT wired in

| File | Status |
|---|---|
| `internal/tools/debuglog/ringbuffer.go` | ✅ Ring buffer implementation, 6 passing tests |
| `internal/tools/debuglog/ringbuffer_test.go` | ✅ Tests pass |
| `internal/tools/core/debuglog.go` | ✅ `DebugLog` tool with dump/clear/status — but not registered |

### Sensitive log lines (all unconditional `log.Printf`)

| File | Line | What it logs | Privacy risk |
|---|---|---|---|
| `execution.go:182` | `Executing tool: %s with args: %v` | Full tool args | **HIGH** — bash commands, file contents, queries |
| `execution.go:225` | `Tool execution failed: tool=%s error=%v` | Error details | LOW — useful for ops |
| `execution.go:299` | `HandleToolCallFlow called with %d tool calls` | Count only | NONE |
| `execution.go:301` | `Tool call %d: %s` | Tool name only | NONE |
| `execution.go:316` | `Tool chain depth limit reached` | Depth count | NONE |
| `execution.go:57,72` | `ThinkingIndicator` messages | Thinking status | LOW |
| `streaming.go:~189` | `Stop reason: %s` | Stop reason | LOW |
| `streaming.go:~114` | `Streaming request: model=%s` | Model name | NONE |

### `LoggingMiddleware` (execution.go:479-498)

- Defined but **never wired** in production (`gateway.go` doesn't call `AddMiddleware`)
- Only used in `examples/tool-execution/main.go`
- `BeforeExecution` logs full args: `Executing tool: %s with args: %v`
- Duplicates what `executeSingle` already logs

### Config available but unused

```go
// internal/config/config.go
type DebugConfig struct {
    LogMessageContent bool `json:"log_message_content,omitempty"`
    VerboseLogging    bool `json:"verbose_logging,omitempty"`
}
```

`VerboseLogging` and `LogMessageContent` exist in config but are **read by nothing**.

### Where the ExecutionEngine is constructed

```go
// internal/gateway/gateway.go:226
executionEngine := tools.NewExecutionEngine(toolsRegistry, 4, 60*time.Second, maxToolChains)
```

Config (`cfg`) is in scope here. The engine does not currently receive config or a ring buffer.

---

## Design

### 1. Wire ring buffer into ExecutionEngine

**Change `NewExecutionEngine` signature:**

```go
func NewExecutionEngine(
    registry ToolRegistry,
    maxParallel int,
    timeout time.Duration,
    maxChains int,
    debugBuffer *debuglog.RingBuffer, // NEW — nil-safe
    verboseLogging bool,               // NEW
) *ExecutionEngine
```

Store both on the struct:

```go
type ExecutionEngine struct {
    // ... existing fields ...
    debugBuffer    *debuglog.RingBuffer
    verboseLogging bool
}
```

### 2. Modify `executeSingle` (execution.go:182)

**Before:**
```go
log.Printf("[ExecutionEngine] Executing tool: %s with args: %v", call.Name, call.Args)
```

**After:**
```go
// Always log tool name (no args) to journal
log.Printf("[ExecutionEngine] Executing tool: %s", call.Name)

// Capture full details in ring buffer (private, in-memory only)
if e.debugBuffer != nil {
    e.debugBuffer.Add(debuglog.ToolStart(call.Name, call.Args))
}

// Only log args to journal when verbose mode is on
if e.verboseLogging {
    log.Printf("[ExecutionEngine] Tool args: %v", call.Args)
}
```

Similarly, after execution completes:

```go
if e.debugBuffer != nil {
    if err != nil {
        e.debugBuffer.Add(debuglog.ToolError(call.Name, execResult.Duration, err.Error()))
    } else {
        summary := truncateResult(result, 500)
        e.debugBuffer.Add(debuglog.ToolComplete(call.Name, execResult.Duration, summary))
    }
}
```

### 3. Modify thinking indicator logs (execution.go:57,72)

**Before:**
```go
log.Printf("[ThinkingIndicator] Emitting thinking status: %s", msg)
log.Printf("[ThinkingIndicator] Tick: %s", msg)
```

**After:**
```go
// Remove from journal entirely (noise)
// Capture in ring buffer via tool event callback if desired
```

The thinking indicator is pure noise in the journal. Remove both `log.Printf` calls. The ring buffer captures thinking events via the existing `ToolEventCallback` mechanism (which is already wired to send "thinking" events).

### 4. Capture LLM request/response metadata in ring buffer

**In `streaming.go`**, the log lines are less sensitive but still useful in the buffer:

```go
// streaming.go — already low-risk, keep as-is:
log.Printf("[Anthropic] Streaming request: model=%s, isOAuth=%v", modelToUse, a.isOAuth)

// Stop reason — demote to verbose only:
if e.verboseLogging {
    log.Printf("[Streaming] Stop reason: %s", stopReason)
}
```

For the ring buffer: the `ToolEventCallback` in `executeSingle` already fires `start`/`complete`/`error` events. The `HandleToolCallFlow` method should add LLM request/response entries to the buffer around its `provider.GenerateResponse` calls. This requires passing the buffer into the adapter, or using the existing context-based callback.

**Recommended approach:** Extend the existing `ToolEventCallback` to also record to the ring buffer. In `gateway.go`, when constructing the execution engine, install a callback that writes to the shared buffer. This keeps the buffer wiring centralized.

### 5. Register the DebugLog tool

**In `registry.go` `registerAllTools()`**, add:

```go
// Debug log inspection tool (always available, buffer may be nil → returns empty)
allTools = append(allTools, core.NewDebugLogTool(r.services, r.services.DebugBuffer))
```

**Add `DebugBuffer` to `ToolServices`:**

```go
// internal/tools/types/types.go
type ToolServices struct {
    // ... existing fields ...
    DebugBuffer *debuglog.RingBuffer
}
```

### 6. Instantiate and wire in gateway.go

```go
// internal/gateway/gateway.go — after config is loaded, before execution engine

// Create shared debug ring buffer
debugBuffer := debuglog.NewRingBuffer(debuglog.DefaultCapacity)

// Pass to execution engine
executionEngine := tools.NewExecutionEngine(
    toolsRegistry, 4, 60*time.Second, maxToolChains,
    debugBuffer,
    cfg.Debug.VerboseLogging,
)

// Make available to tools
toolServices.DebugBuffer = debugBuffer
```

### 7. LoggingMiddleware disposition

**Remove or gut `LoggingMiddleware`.** It's dead code in production:
- Never wired in `gateway.go`
- Duplicates `executeSingle` logging
- Only example code uses it

Options:
- **Option A:** Delete it entirely (cleanest)
- **Option B:** Keep it but make it use the ring buffer instead of `log.Printf`

**Recommendation: Option A.** The ring buffer + `executeSingle` changes cover everything `LoggingMiddleware` did. If someone wants the example to work, they can use `MetricsMiddleware` (which doesn't log sensitive data).

### 8. Result truncation helper

Add a helper to `execution.go` for summarizing tool results before storing in the buffer:

```go
func truncateResult(result *ToolResult, maxLen int) string {
    if result == nil {
        return "<nil>"
    }
    content := result.Content
    if len(content) > maxLen {
        return content[:maxLen] + "… [truncated]"
    }
    return content
}
```

This prevents the ring buffer from bloating with full file contents or large API responses.

---

## Files to Change

| File | Change | Risk |
|---|---|---|
| `internal/tools/execution.go` | Demote log lines, add buffer writes, remove LoggingMiddleware, add truncation helper | **Medium** — core execution path |
| `internal/gateway/gateway.go` | Instantiate ring buffer, pass to engine + services | **Low** — construction only |
| `internal/tools/types/types.go` | Add `DebugBuffer` field to `ToolServices` | **Low** — additive |
| `internal/tools/registry.go` | Register `DebugLogTool` | **Low** — additive |
| `internal/ai/streaming.go` | Demote stop_reason log to verbose-only | **Low** — log level change |

### Files that already exist and need NO changes

| File | Status |
|---|---|
| `internal/tools/debuglog/ringbuffer.go` | ✅ Ready |
| `internal/tools/debuglog/ringbuffer_test.go` | ✅ 6 tests passing |
| `internal/tools/core/debuglog.go` | ✅ Ready (just needs buffer wired in) |

---

## Testing

1. **Existing ring buffer tests** — already pass, no changes needed
2. **Build verification** — `go build ./cmd/gateway/` must succeed
3. **Manual verification:**
   - Start Conduit with `debug.verbose_logging: false` (default)
   - Execute a tool (e.g., Read a file)
   - `journalctl -u conduit -n 20` should show tool name but NOT args
   - Call `DebugLog(action="dump")` — should show full args + result summary
4. **Verbose mode verification:**
   - Set `debug.verbose_logging: true` in config
   - Restart, execute a tool
   - `journalctl` should now show args (old behavior)
5. **Ring buffer overflow** — fill beyond 500 entries, verify oldest are evicted

---

## Rollback

If anything breaks: revert the `log.Printf` changes in `execution.go` and `streaming.go` to restore old behavior. The ring buffer and tool are additive and can be left in place harmlessly.

---

## 6. MQTT Log Hygiene

The MQTT subsystem is the noisiest source of journal spam — especially during reconnect cycles with large retained messages. Every retained message, every subscription, and every prune tick logs at INFO.

### MQTT Log Lines Inventory

| File | Line | Current log | Disposition |
|---|---|---|---|
| `client.go:63` | `[MQTT] Connected to %s` | Keep INFO — operational milestone |
| `client.go:71` | `[MQTT] Connection lost: %v` | Keep INFO — error, needs visibility |
| `client.go:78` | `[MQTT] Reconnecting to %s...` | **Demote to DEBUG** — fires every 2 min during reconnect loops |
| `client.go:89` | `[MQTT] Retained message: %s (%d bytes)` | **Demote to DEBUG** — floods journal on connect (100+ messages) |
| `client.go:134` | `[MQTT] Failed to subscribe to %s: %v` | Keep INFO — error |
| `client.go:136` | `[MQTT] Subscribed to %s (QoS %d)` | **Demote to DEBUG** — logs every reconnect × every topic |
| `client.go:164` | `[MQTT] Publishing to %s (QoS %d, ...)` | **Demote to DEBUG** — operational detail |
| `client.go:175` | `[MQTT] Publish to %s cancelled: %v` | Keep INFO — error/timeout |
| `client.go:179` | `[MQTT] Publish to %s failed: %v` | Keep INFO — error |
| `client.go:182` | `[MQTT] Published to %s — broker ACK received` | **Demote to DEBUG** — success detail |
| `client.go:207` | `[MQTT] Disconnected` | Keep INFO — lifecycle event |
| `service.go:55` | `[MQTT] Failed to parse bridge/devices: %v` | Keep INFO — error |
| `service.go:76` | `[MQTT] Pruned %d old events` | **Demote to DEBUG** — fires every 60s, pure noise |
| `service.go:149` | `[MQTT] Publish rejected: publish_allowed is false` | Keep INFO — security event |
| `device_registry.go:79` | `[MQTT] Device registry updated: %d devices` | **Demote to DEBUG** — fires every reconnect |
| `event_buffer.go:131` | `[MQTT] Topic cap reached...` | Keep INFO — capacity warning (already rate-limited) |

### Implementation

The MQTT package doesn't currently have access to `VerboseLogging` config. Two options:

**Option A (preferred): Pass `verboseLogging bool` to `NewService`/`NewClient`**

```go
// client.go
type Client struct {
    // ...
    verbose bool
}

// Helper method
func (c *Client) debugf(format string, args ...interface{}) {
    if c.verbose {
        log.Printf(format, args...)
    }
}
```

Then replace `log.Printf(...)` with `c.debugf(...)` on the demoted lines. Same pattern for `Service` and `DeviceRegistry`.

**Option B: Package-level debug flag**

```go
// mqtt/log.go
var VerboseLogging bool

func debugf(format string, args ...interface{}) {
    if VerboseLogging {
        log.Printf(format, args...)
    }
}
```

Set from `gateway.go` after reading config: `mqtt.VerboseLogging = cfg.Debug.VerboseLogging`

Option B is simpler and avoids threading the flag through constructors. Either works.

### Summary

- **7 lines demoted** from INFO → DEBUG (only log when `verbose_logging: true`)
- **8 lines kept** at INFO (errors, connection state changes, security events)
- Net effect: During a healthy MQTT reconnect cycle, journal goes from ~20 lines per cycle to 2 (connected + connection lost)

---

## Open Questions

1. **Should tool *results* go in the buffer?** Currently specced as truncated summaries (500 chars). Full results could bloat memory. Could add a `result_detail` filter that captures full results for the last N calls only.
2. **Should the ring buffer capacity be configurable?** Currently hardcoded at 500. Could add `debug.ring_buffer_capacity` to config. Low priority.
3. **Should `LogMessageContent` gate anything?** It exists in config alongside `VerboseLogging` but this spec only uses `VerboseLogging`. Could map `LogMessageContent` to control whether tool *results* appear in journal (vs just args). Or just remove it as unused.
