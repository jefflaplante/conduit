# Workspace Context Loader

The workspace package loads workspace context files (SOUL.md, USER.md, AGENTS.md, MEMORY.md, daily memory, ...) for injection into the agent's system prompt, filtered by per-session security rules. It also hosts the optional AI summarizer for small-context models and the beads summary refresher.

## How it is wired

The gateway builds one `WorkspaceContext` at startup and hands it to the agent's prompt builder:

```go
// internal/gateway/gateway.go
wc := workspace.NewWorkspaceContextWithLookback(
    cfg.Workspace.ContextDir,
    cfg.Workspace.Files.Memory.DailyLookbackDays,
)
```

For each prompt, the prompt builder (`internal/agent/prompt_workspace.go`) derives a `SecurityContext` from the session and loads the permitted files:

```go
bundle, err := wc.LoadContext(ctx, workspace.SecurityContext{
    SessionType: "main",   // "main", "shared" (group chats) or "isolated"
    ChannelID:   session.ChannelID,
    UserID:      session.UserID,
    SessionID:   session.Key,
})
// bundle.Files maps relative path ("SOUL.md", "memory/2026-02-09.md") to content
```

`NewWorkspaceContext(dir)` is the same with the default lookback (`DefaultDailyLookbackDays` = 2).

## Components

- **WorkspaceContext** (`context.go`): file discovery, loading and the `LoadContext` entry point. Also exposes `GetWorkspaceDir`, `InvalidateCache`, `ClearCache` and `GetCacheStats`.
- **SecurityManager** (`security.go`): pattern-based access rules keyed on `SecurityContext.SessionType`.
- **FileCache** (`files.go`): in-memory content cache (5-minute TTL, 50 MB cap, LRU eviction).
- **SummaryManager / SummaryCache / SummaryAIExecutor** (`summarizer.go`, `summary_cache.go`, `summary_executor.go`, `summary_types.go`): optional AI summarization of context files for small-context models, cached on disk.
- **Beads** (`beads.go`): `QueryActiveBeads` / `RefreshBeadsBrainEntry` keep an active-work summary in brain LTM.

## Files loaded

Core files from the workspace root, when present: `SOUL.md`, `USER.md`, `AGENTS.md`, `TOOLS.md`, `IDENTITY.md`, `MEMORY.md`, `HEARTBEAT.md`, `BOOTSTRAP.md`.

Daily memory: `memory/YYYY-MM-DD.md` for the last N days, today included, where N is `workspace.files.memory.daily_lookback_days`. `0` disables daily memory injection; a negative value falls back to the default of 2.

## Security model

| File | main | shared / isolated |
|------|------|-------------------|
| SOUL.md, USER.md, AGENTS.md, TOOLS.md, IDENTITY.md | yes | yes |
| MEMORY.md, HEARTBEAT.md, BOOTSTRAP.md | yes | no |
| memory/*.md | yes | yes |

The prompt builder treats a session as `shared` when its channel ID looks like a group chat; otherwise it is `main`.

## Configuration

Workspace settings live in `config.WorkspaceConfig` (`internal/config/workspace.go`). The keys the loader consumes are:

```json
{
  "workspace": {
    "context_dir": "./workspace",
    "files": {
      "memory": { "daily_lookback_days": 2 }
    },
    "summary": {
      "enabled": false,
      "model": "claude-haiku-4-5-20251001",
      "target_ratio": 0.25,
      "cache_dir": ".summaries",
      "cache_ttl_hours": 168,
      "fallback_to_truncate": true,
      "file_configs": {
        "SOUL.md": { "ratio": 0.40, "preserve_keys": ["personality", "tone"] }
      }
    }
  }
}
```

The summarizer is built by the gateway (`internal/gateway/gateway_init.go`) only when `summary.enabled` is true, and the prompt builder uses it only for models whose context window is below the large-context threshold (`ShouldSummarize`).

## Error handling

Loading degrades gracefully: missing files are skipped, read errors are logged and the file is dropped, and an empty bundle simply yields no project-context section in the prompt.

## Testing

```bash
go test ./internal/workspace/...
```
