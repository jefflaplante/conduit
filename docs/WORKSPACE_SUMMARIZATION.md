# Workspace Context Summarization

## Overview

Conduit uses a priority-based system for assembling system prompts. When using models with smaller context windows (< 128K tokens), lower-priority sections may not fit within the token budget.

**The Problem**: Project Context files (SOUL.md, USER.md, AGENTS.md, TOOLS.md) are Priority 4 - the lowest priority. Without summarization, these files are **dropped entirely** when the context budget is exceeded. This degrades the agent experience because:

- Personality (SOUL.md) defines the agent's voice, tone, and character
- User preferences (USER.md) contain important constraints and requirements
- Operational rules (AGENTS.md) include mandatory restrictions and guidelines
- Tool guidance (TOOLS.md) helps the agent use tools correctly

**The Solution**: AI-powered summarization compresses workspace files intelligently, preserving essential content while fitting within smaller context windows.

## Default Behavior (Summarization Disabled)

When `workspace.summary` is not configured or `enabled: false`:

```
┌─────────────────────────────────────────────────────────────────┐
│                    SMALL CONTEXT MODEL (< 128K)                  │
├─────────────────────────────────────────────────────────────────┤
│ Priority 1 (Critical)     → Always included                      │
│ Priority 2 (Delivery)     → Included if budget allows            │
│ Priority 3 (Enhancement)  → Included if budget allows            │
│ Priority 4 (Project Ctx)  → DROPPED ENTIRELY if over budget      │
└─────────────────────────────────────────────────────────────────┘
```

**Consequences when disabled**:
- Agent loses personality defined in SOUL.md
- User preferences in USER.md are ignored
- Operational rules in AGENTS.md not enforced
- Tool guidance in TOOLS.md unavailable
- Responses become generic and less contextually appropriate

**When this is acceptable**:
- Using large-context models (128K+ tokens) exclusively
- Workspace files are very small (under budget anyway)
- Testing/development where personality doesn't matter
- Cost-sensitive deployments where AI summarization calls are undesirable

## Enabled Behavior (Summarization Active)

When `workspace.summary.enabled: true`:

```
┌─────────────────────────────────────────────────────────────────┐
│                    SMALL CONTEXT MODEL (< 128K)                  │
├─────────────────────────────────────────────────────────────────┤
│ Priority 1 (Critical)     → Always included (full)               │
│ Priority 2 (Delivery)     → Included if budget allows (full)     │
│ Priority 3 (Enhancement)  → Included if budget allows (full)     │
│ Priority 4 (Project Ctx)  → AI-SUMMARIZED to fit budget          │
│                             (personality preserved)              │
└─────────────────────────────────────────────────────────────────┘
```

**Benefits when enabled**:
- Agent maintains distinct personality from SOUL.md
- User preferences preserved (constraints, requirements)
- Mandatory rules from AGENTS.md enforced
- Tool usage guidance available
- Summaries cached for efficiency (7-day TTL, hash-based invalidation)

**Trade-offs**:
- Additional AI API calls (uses Haiku by default - fast and cheap)
- Cache storage on disk (~few KB per file)
- Initial summarization latency (cached after first call)

## Configuration

### Minimal Configuration (Enable with Defaults)

```json
{
  "workspace": {
    "context_dir": "./workspace",
    "summary": {
      "enabled": true
    }
  }
}
```

This uses all defaults:
- Model: `claude-haiku-4-5-20251001`
- Target compression: 25% of original size
- Cache TTL: 7 days
- Cache directory: `.summaries` in workspace
- Fallback to truncation if AI fails

### Full Configuration (All Options)

```json
{
  "workspace": {
    "context_dir": "./workspace",
    "summary": {
      "enabled": true,
      "model": "claude-haiku-4-5-20251001",
      "target_ratio": 0.25,
      "cache_dir": ".summaries",
      "cache_ttl_hours": 168,
      "fallback_to_truncate": true,
      "file_configs": {
        "SOUL.md": {
          "ratio": 0.40,
          "preserve_keys": ["personality", "tone", "voice", "style"]
        },
        "USER.md": {
          "ratio": 0.30,
          "preserve_keys": ["preferences", "constraints", "requirements"]
        },
        "AGENTS.md": {
          "ratio": 0.25,
          "preserve_keys": ["rules", "restrictions", "mandatory", "never"]
        },
        "TOOLS.md": {
          "ratio": 0.20,
          "preserve_keys": ["usage", "commands", "examples"]
        }
      }
    }
  }
}
```

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `false` | Enable AI summarization |
| `model` | string | `claude-haiku-4-5-20251001` | AI model for summarization |
| `target_ratio` | float | `0.25` | Default compression ratio (0.25 = 25% of original) |
| `cache_dir` | string | `.summaries` | Directory for cached summaries |
| `cache_ttl_hours` | int | `168` | Cache time-to-live (168 = 7 days) |
| `fallback_to_truncate` | bool | `true` | Use simple truncation if AI fails |
| `file_configs` | object | (see above) | Per-file override settings |

### Per-File Configuration

Each file in `file_configs` can have:

| Option | Type | Description |
|--------|------|-------------|
| `ratio` | float | Override target compression ratio for this file |
| `preserve_keys` | array | Keywords/concepts to emphasize when summarizing |

**Recommended ratios by file type**:
- SOUL.md: 0.40 (40%) - Personality is critical, keep more
- USER.md: 0.30 (30%) - Preferences need detail
- AGENTS.md: 0.25 (25%) - Rules can be condensed
- TOOLS.md: 0.20 (20%) - Often verbose, compress more

## How It Works

### Summarization Flow

```
buildWorkspaceContextSection()
         │
         ▼
  Is context window < 128K?  ───No───► Use full content
         │
        Yes
         │
         ▼
  Is summaryManager enabled? ───No───► Use full content (may be dropped)
         │
        Yes
         │
         ▼
  For each workspace file:
         │
         ├─► File < 500 bytes? ───Yes───► Use original (too small)
         │
         ▼
  Check cache (filename + hash)
         │
         ├─► Cache hit? ───Yes───► Return cached summary
         │
         ▼
  Call AI to summarize
         │
         ├─► AI success? ───Yes───► Cache and return summary
         │
         ▼
  fallback_to_truncate?
         │
         ├─► Yes ───► Truncate to target length
         │
         └─► No ────► Return original content
```

### Cache Invalidation

Summaries are cached by `filename:sha256(content)`:
- Editing a workspace file automatically invalidates its cached summary
- No manual cache management needed
- Cache persists across restarts (disk-backed)
- Expired entries (> TTL) are cleaned up automatically

### Summarization Prompts

The AI receives file-type-specific instructions:

**SOUL.md** (personality):
```
PRESERVE: Core personality traits, communication style, tone markers, voice characteristics
CONDENSE: Verbose explanations, extensive examples, repeated concepts
```

**AGENTS.md** (rules):
```
PRESERVE: Mandatory rules, restrictions, do/don't lists, safety guidelines
CONDENSE: Extended explanations, optional suggestions, contextual background
```

## Monitoring

Check summarization stats via the session_status tool or gateway metrics:

```json
{
  "summary_manager": {
    "enabled": true,
    "model": "claude-haiku-4-5-20251001",
    "target_ratio": 0.25,
    "cache_hits": 42,
    "cache_misses": 3,
    "ai_calls": 3,
    "fallbacks": 0,
    "hit_rate": 0.93
  }
}
```

## Best Practices

1. **Enable for production** if using multiple model sizes (Haiku, Sonnet, Opus)
2. **Use Haiku** for summarization - fast, cheap, good at compression
3. **Tune ratios** based on your file sizes and content density
4. **Set preserve_keys** for concepts critical to your agent's behavior
5. **Monitor hit rate** - should be >90% after initial warmup

## Troubleshooting

### Summaries too short/losing information
- Increase `target_ratio` for that file (e.g., 0.40 instead of 0.25)
- Add more `preserve_keys` to emphasize important concepts

### AI calls on every request
- Check cache directory exists and is writable
- Verify files aren't changing frequently (hash invalidation)
- Check TTL hasn't expired

### Fallback truncation happening
- Check AI provider connectivity
- Verify model name is correct
- Check API rate limits

### Agent personality lost
- Ensure `enabled: true` in config
- Check SOUL.md ratio isn't too low (recommend 0.40)
- Verify summarization is actually running (check logs for "Summarized SOUL.md")
