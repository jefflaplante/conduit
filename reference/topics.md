# Topics (topic switcher)

Topics give each project its own session inside one chat (conduit-43vk). A
chat has an **active-topic pointer**; the pointer picks the session that
incoming messages go to:

```
chat → topic pointer → session
```

Each topic owns a session with its own transcript, compaction and token
budget, so parallel projects no longer share one growing transcript. The
chat's ordinary session stays the hub (**home**). Home is not a topic: with
no pointer, a chat behaves exactly as it did before topics existed. Brain
(global memory) still spans every topic.

## Commands

All replies end with `📍 Current: <slug|home>`.

| Command | Effect |
|---------|--------|
| `/topic` | List topics: slug, name, status, last activity, ~tokens, `★` = current, `stale` after 7 idle days |
| `/topic <slug>` | Switch the chat to the topic, creating it if new. A parked (or archived) topic is reactivated. Replies `Topic: <name> — new` or the topic's summary and last activity |
| `/topic home`, `/home` | Clear the pointer: back to the home session |
| `/topic park <slug>` | Summarize, mark parked; chats pointing at it go home |
| `/topic close <slug>` | Final summary (also stored in the topic session's context as `topic_summary`), archive; chats pointing at it go home |
| `/topic rename <slug> <new name or slug>` | Set the display name; the slug becomes the normalized new text. A new slug moves every chat pointer with it and the old slug stops resolving |
| `/topic persona <slug> [text]` | Set the topic persona; no text clears it |
| `/topic stitch <from> <to>` | Prepend `<from>`'s summary to `<to>`'s topic prefix (kept; `<from>` is unchanged) |

Slugs are normalized to lowercase-hyphen, at most 32 characters
(`Solar Panels` → `solar-panels`). Subcommand names (`home`, `list`, `park`,
`close`, `rename`, `persona`, `stitch`, `help`) are reserved. Any other
`/topic` form prints usage.

Inside a topic, other commands act on the topic's session: `/reset` clears
that topic's transcript only (the chat stays in the topic), `/status`,
`/context`, `/compact`, `/model` and `/stop` likewise.

## What the model sees

On every turn of a topic session the prompt gets a small dynamic section
(after the cache breakpoint, so switching never invalidates the cached
prompt):

```
## Topic
From topic <other> (<name>): <summary>        ← stitched context, if any
Topic: <name> — <summary>. Last activity: 3h ago.
<persona>                                     ← if set
```

Summaries are 2–3 sentences, generated on park, close and switch-away (only
when the topic has new messages). They follow the `/reset` reflection path:
SPAR session metrics when reflection is enabled, then a short model call
over the last 40 messages (bounded input, 15 s timeout). Without AI, or on
failure, a template is used (`N messages so far. Last request: "…"`).
Full transcripts are never injected.

## Outbound tagging

Messages from a topic session to its chat are prefixed with `[slug] `:
replies (streamed and not), errors, busy/progress notices, wake replies and
proactive `Message` tool sends. Home messages are untagged.

## AI tools

`topic_list`, `topic_create`, `topic_switch`, `topic_rename`, `topic_park`,
`topic_close`, `topic_stitch`, `topic_persona` wrap the same functions as the
commands and act on the chat of the current turn. Enable them in
`tools.enabled_tools` like any tool. See the
[Tools Reference](tools-reference.md#topics).

## Behavior notes

- Distinct topics are distinct session keys, so their turns run in parallel
  (the per-session turn lock). Switching away while a topic's turn runs is
  safe: the turn finishes and its reply is still tagged.
- If the pointer names a topic that was parked, archived or deleted, the
  next message goes home and the chat is told once.
- Crons, sub-agents and `SessionsSend` address session keys directly and
  bypass the pointer. Approvals bind to the session that asked: reply to an
  approval prompt before switching topics.
- Storage: `topics` and `topic_pointers` tables in the gateway DB (migration
  11). The pointer key is `<channel>:<chat id>`. Topic session keys start
  with `topic_`; they share the chat's user/channel (so wakes reach the
  chat) but are never picked as the chat's home session, and session cleanup
  never prunes them.
- WebSocket and the TUI client address sessions by key and have their own
  session switching; topics apply to channel adapters (Telegram, TUI
  channel).
