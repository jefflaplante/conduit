# Workspace Context Summarization

## Quick Recommendation

**Should you enable this?**

| Your Setup | Recommendation | Why |
|------------|----------------|-----|
| Claude models only (Haiku/Sonnet/Opus) | **No** | All have 200K context - summarization never triggers |
| Mixed models (Claude + GPT-4 + local) | **Yes** | Graceful degradation on smaller models |
| Small models only (8K-32K context) | **Yes** | Preserves personality that would otherwise be dropped |
| Cost-sensitive, large models | **No** | Avoid extra Haiku API calls |

**TL;DR**: If you exclusively use Claude models, don't bother. The feature only activates for models with context windows under 128K tokens.

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

## Token Budget Analysis

Understanding when summarization actually helps:

### Typical Prompt Sizes

| Component | Size |
|-----------|------|
| Core system prompt (Priority 1-3) | ~5,000 chars (~1,250 tokens) |
| Workspace files (Priority 4) | ~4,000 chars (~1,000 tokens) |
| **Full prompt** | ~9,000 chars (~2,250 tokens) |
| **Summarized workspace** | ~1,200 chars (~300 tokens) |

### Budget by Model Size

| Context Window | Budget (15%) | Core Prompt | + Full Workspace | + Summarized | Verdict |
|----------------|--------------|-------------|------------------|--------------|---------|
| 8K | ~1,228 tokens | 1,250 | 2,250 ❌ | 1,550 ⚠️ | Tight fit |
| 16K | ~2,400 tokens | 1,250 | 2,250 ✅ | 1,550 ✅ | Summarization helps |
| 32K | ~4,800 tokens | 1,250 | 2,250 ✅ | 1,550 ✅ | Either works |
| 64K | ~9,600 tokens | 1,250 | 2,250 ✅ | 1,550 ✅ | Either works |
| 128K+ | ~19,200+ | 2,250 | 2,250 ✅ | N/A | Full prompt, no summarization |

### Key Insight

- **8K models**: Core prompt already near budget. Summarization helps marginally (some personality vs none)
- **16K-64K models**: Summarization provides meaningful benefit - full personality in smaller footprint
- **128K+ models**: No summarization needed - everything fits comfortably

## Does Summarization Help Large Context Models?

**No.** For models with 128K+ context windows:

| Factor | Full Context | Summarized |
|--------|--------------|------------|
| Information | Complete | Lossy (AI decides what's "important") |
| Latency | Direct | +Haiku API call on cache miss |
| Cost | Just the prompt tokens | +Haiku tokens for summarization |
| Nuance | All details preserved | May lose subtle guidance |
| Model attention | Handles long context fine | N/A |

**Why "less is more" doesn't apply here:**

1. Workspace files (~4KB) are tiny relative to 200K context (0.02%)
2. Claude models are specifically trained for long context
3. "Lost in the middle" phenomenon applies to very long documents, not well-structured system prompts
4. AI summarization introduces its own biases about what matters

## Best Practices

1. **Don't enable for Claude-only deployments** - all Claude models have 200K context
2. **Enable for mixed-model deployments** - graceful degradation on smaller models
3. **Use Haiku** for summarization - fast, cheap, good at compression
4. **Tune ratios** based on your file sizes and content density
5. **Set preserve_keys** for concepts critical to your agent's behavior
6. **Monitor hit rate** - should be >90% after initial warmup

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
