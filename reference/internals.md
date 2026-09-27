# Internal Subsystems Reference

This document covers internal subsystems that are not part of the main gateway request path.

The former experimental packages `internal/learning`, `internal/orchestration`, `internal/nli` and `internal/plugins` were never wired into the gateway and were removed in the 2026-09 dead-code cleanup (conduit-31jg.38). Multi-agent work uses the sessions tools (`SessionsSpawn`, `SessionsSend`); extensibility uses Skills (`SKILL.md`) and MCP servers.

---

## Briefing (Session Summarization)

**Package:** `internal/briefing/`

The briefing subsystem generates structured summaries of session activity by analyzing conversation messages. Given a session ID and its message history, it produces a `Briefing` that includes a high-level summary, key decisions made during the conversation, files changed, tools used (with counts), open questions, and suggested next steps. Briefings are persisted as JSON files and can be listed and loaded from a directory.

The summarization is entirely heuristic-based: it uses keyword matching to identify decisions ("decided to", "let's use"), file changes ("wrote to", "edited"), and next steps ("todo", "need to", "follow up"). Open questions are detected by scanning the last 10 messages for sentences ending with a question mark. No LLM calls are made during briefing generation.

### Key Types

| Type | Description |
|------|-------------|
| `BriefingGenerator` | Stateless generator. Call `Generate(sessionID, messages)` to produce a `Briefing`. |
| `Briefing` | Full summary: ID, session ID, timestamp, summary text, key decisions, files changed, tools used, open questions, next steps, duration, message count. |
| `BriefingSummary` | Lightweight representation for list views (ID, session ID, timestamp, truncated summary, duration). |
| `Message` | Input message type (mirrors `sessions.Message` without importing it): ID, role, content, timestamp, metadata map. |
| `ToolUsage` | Tool name and invocation count. |

### Key Functions

- `Generate(sessionID, messages)` -- Produces a full briefing from message history.
- `Save(briefing, dir)` -- Persists a briefing as `{briefingID}.json` in the given directory.
- `Load(path)` -- Reads a briefing from a JSON file.
- `ListBriefings(dir)` -- Returns summaries of all briefings in a directory, sorted most-recent-first.

### Summary Generation Details

The summary combines message counts (user vs. assistant), the first user message (truncated to 200 chars) as the opening topic, and the last assistant message as the closing state. Tool usage is counted from message metadata (`tool_name` key) and from content pattern matching against known tool names. Files changed are extracted by looking for write/edit/create patterns followed by path-like strings.

### Integration

Exposed through the `conduit briefing` CLI (`generate`, `show`, `list`; cmd/gateway/briefing.go). It is not triggered automatically by the gateway. The `Message` type is structurally compatible with `sessions.Message`, so adaptation is straightforward.

### Configuration

No configuration options. The generator is stateless and has no tunable parameters.

### Status

**Experimental.** Fully implemented with tests. The heuristic approach produces reasonable summaries for structured tool-heavy sessions but may miss nuance in free-form conversations.
