# PRD: System Prompt Refactor

**Author:** Jules
**Date:** 2026-03-24
**Status:** Draft
**Scope:** `internal/ai/prompt_builder.go`, `internal/ai/prompt_sections.go`, `internal/ai/constants.go`

## Problem Statement


## Goals

1. Improve grounding quality by restructuring section order (data first, instructions last)
2. Eliminate duplicated rules to reclaim ~500-800 chars of context budget
3. Add a role statement to the identity so behavioral priors are set before SOUL.md loads
4. Define explicit stopping conditions for agentic tool loops
5. Reframe negative instructions as positive directives where possible

- Rewriting the `promptSection` priority/budget system (it's good)

---

### Change 1: Restructure Section Order — Data Before Instructions


**Current order (simplified):**
```
P1: Identity (name + Operating Principles)
P1: Tooling (tool descriptions)
P1: Tool Integrity (behavioral rules)
P1: Silent Replies
P1: Runtime
P1: Safety
P2: Project Context (SOUL.md, USER.md, MEMORY.md — the grounding data)
P2: Memory sections
P3: Tool guidance sections
P4: Nice-to-have sections
```

**Proposed order:**
```
P1: Identity (brief — name + role, 2-3 sentences max)
P1: Runtime (factual — time, host, model, channel)
P2: Project Context (SOUL.md, USER.md, MEMORY.md — grounding data, moved UP)
P2: Tooling (tool descriptions — reference data)
P2: Memory sections
P3: Tool Strategy & Guidance (how to use tools)
P3: Behavioral Rules (consolidated Operating Principles + Tool Integrity)
P3: Error Handling
P3: Safety
P4: Silent Replies, Reactions, nice-to-have
```


**Implementation note:** The current system sorts by `(priority, order)`. We may need to either:
- Adjust `order` values within P1/P2 to get the desired sequence, OR
- Split Identity into two sections: a brief P1 identity line and a P3 behavioral rules section

**Risk:** Medium. Reordering could affect behavior on edge cases. Requires testing across model sizes (Opus, Sonnet, Haiku, Ghost).

**Validation:** Run the same 10-15 prompts before/after and compare response quality, especially for tasks that require grounding in MEMORY.md or USER.md context.

---

### Change 2: Enrich Identity with Role Statement


**Proposed identity:**
```
You are Jules — a sardonic, competent personal assistant and home automation agent for Jeff LaPlante.


---

### Change 3: Consolidate Duplicated Rules

**Problem:** The "never fabricate" rule appears in four places:
1. Operating Principles: *"Never fabricate. Always call tools for real data..."*
2. Tool Integrity section: *"CRITICAL: Never fabricate tool results or actions..."* (5 bullet points)
3. Error Handling: *"If a tool is unavailable or fails, say so. Never substitute a fabricated result."*
4. SOUL.md "The Scar" section (workspace file, not our concern here)

The "when uncertain" rule appears twice:
1. Operating Principles: *"When uncertain, say so."*
2. Error Handling: *"When uncertain about system state, verify before acting."*

**Proposed consolidation:**
- **Tool Integrity** is the canonical home for fabrication rules. Keep the full version there.
- **Operating Principles** gets a one-line reference: *"Verify before claiming. (See Tool Integrity.)"*
- **Error Handling** drops the fabrication line entirely — it's redundant.
- **Uncertainty** lives in Error Handling (it's about system state). Operating Principles drops its version.

**Savings:** ~400-600 chars reclaimed. Meaningful for Ghost/Qwen budget.

**Risk:** Low. Consolidation, not removal. The rule still exists in one authoritative place.

---

### Change 4: Reframe Negative Instructions as Positive Directives

**Problem:** Tool Integrity section is heavy on "Never" statements. Anthropic guideline #7: *"Tell Claude what TO DO, not what NOT to do."*

**Current:**
```
- Never fabricate tool results or actions.
- Never say "I checked and it shows X" unless you actually made the tool call in this turn.
- Never say "I spawned/delegated/launched X" unless you see the tool result confirming it.
```

**Proposed:**
```
- Always call the tool before reporting its results. Show the output.
- Always confirm tool execution succeeded before claiming an action was completed.
- Always include proof — log lines, tool output, return values. Receipts or it didn't happen.
- If you cannot call a tool, say so explicitly rather than approximating.
```

The one "never" worth keeping: *"Never fabricate tool results."* — this is the nuclear rule and negative framing is appropriate for hard safety boundaries.

**Risk:** Low. Same semantics, better framing.

---

### Change 5: Add Agent Stopping Conditions


**Proposed addition** (append to Tool Strategy section):
```
## Stopping Conditions
Stop working and respond to the user when:
1. You have a clear, verified answer to their question
2. You've completed the requested action and confirmed the result
3. You need user input, approval, or clarification to continue
4. You've hit an unrecoverable error (report it, don't spin)
5. You've made 3+ tool calls without meaningful progress (reassess approach)
```

**Risk:** Low. Additive, ~300 chars.

---

### Change 6: Condense Silent Replies Section

**Problem:** 5 lines + 3 examples for a single behavior. Burns ~400 chars.

**Current:**
```
## Silent Replies
When you have nothing to say, respond with ONLY: NO_REPLY
⚠️Rules:
- It must be your ENTIRE message — nothing else
- Never append it to an actual response
- Never wrap it in markdown or code blocks
❌ Wrong: "Here's help... NO_REPLY"
❌ Wrong: "NO_REPLY"  (in code block)
✅ Right: NO_REPLY
```

**Proposed:**
```
## Silent Replies
When you have nothing to say, respond with ONLY: NO_REPLY
It must be your entire message — no other text, no markdown wrapping, no code blocks.
```

**Risk:** Low. If the model starts wrapping NO_REPLY in code blocks, we can add one example back.

---

### Change 7: Fix Minor Bugs

#### 7a: Hardcoded model fallback
**File:** `prompt_builder.go`, `buildRuntimeInfo()`
```go
model := "anthropic/claude-sonnet-4-20250514"
```
Should use `config.DefaultModel()` or the model alias map. Stale string will mislead the model about its own identity.

#### 7b: Double-space in runtime OS field
The runtime line renders as `os=linux  (amd64)` with a double space. There's an empty string being formatted. Remove the phantom `%s` or the extra space.

#### 7c: Stale TODO
```go
// TODO: Update URLs when new domains are ready
```
In `buildDocsSection()`. Either resolve it or remove it if the URLs are final.

---

## Estimated Impact

| Metric | Before | After (est.) |
|--------|--------|--------------|
| Duplicated rule chars | ~1,200 | ~200 |
| Context budget reclaimed | — | ~800-1,000 chars |
| Grounding data position | Middle (P2) | Early (P2 but higher order) |
| Agent stopping guidance | None | Explicit 5-point criteria |
| Identity role framing | Generic | Specific in 1 sentence |

## Testing Plan

1. **Regression:** Run existing prompt builder tests after each change
2. **Budget verification:** `BuildDebug()` output for Ghost-sized context — verify P4 sections still shed correctly
3. **A/B quality check:** Same 10 prompts across Opus/Sonnet/Haiku, compare grounding accuracy (especially questions about Jeff's family, preferences, home setup)
4. **Edge cases:** Verify NO_REPLY behavior, tool integrity adherence, and agent loop termination still work correctly
5. **Small model test:** Ghost/Qwen with budget-constrained prompt — verify critical sections survive

## Implementation Order

1. **Change 7** (bug fixes) — trivial, no behavioral impact, good warmup
2. **Change 2** (role statement) — one-line, low risk, immediate benefit
3. **Change 5** (stopping conditions) — additive, low risk
4. **Change 6** (condense Silent Replies) — small, low risk
5. **Change 3** (consolidate duplicates) — medium effort, needs careful audit
6. **Change 4** (positive framing) — rewrite, needs review
7. **Change 1** (restructure order) — highest impact, highest risk, do last with full testing

---

## Open Questions

1. Should we introduce a `sortOrder` field separate from `priority` to decouple budget inclusion from render order? Currently both are controlled by `priority` + `order`.
2. Do we want to A/B test Change 1 formally (log response quality scores) or just eyeball it?
3. The "Claude Code" identity line (`You are Claude Code, Anthropic's official CLI for Claude.`) — is this required by Anthropic's API terms, or can we drop it in favor of pure Jules identity?

---

*This is a refinement pass, not a rewrite. The prompt system is good; we're making it better.*
