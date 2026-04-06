# Google Workspace Integration

Conduit can interact with Gmail and Google Calendar through the `google_workspace` tool, which wraps the `gws` CLI (Google Workspace CLI). This enables the AI agent to search and read emails, send messages, manage calendar events, and trash unwanted mail.

## Prerequisites

1. **Install the gws CLI:**

   ```bash
   npm install -g @googleworkspace/cli
   ```

2. **Authenticate with your Google account:**

   ```bash
   gws auth login
   ```

   This opens a browser-based OAuth flow. The resulting credentials are stored locally by gws.

3. **Enable the tool** in your Conduit config by adding `"google_workspace"` to the `enabled_tools` list.

## Configuration

The tool reads its settings from `tools.services.google_workspace` in your config JSON. Both fields are optional -- the tool works with sensible defaults if the section is omitted entirely.

```json
{
  "tools": {
    "enabled_tools": ["google_workspace"],
    "services": {
      "google_workspace": {
        "gws_path": "/usr/local/bin/gws",
        "user_id": "me"
      }
    }
  }
}
```

### Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `gws_path` | string | `"gws"` | Path to the gws binary. Use this if gws is not on your PATH. |
| `user_id` | string | `"me"` | Gmail/Calendar user ID. `"me"` refers to the authenticated user. |

### Agent Email Identity

To control which address appears in the `From:` header when sending email, configure the agent email identity:

```json
{
  "agent": {
    "email": {
      "address": "agent@example.com",
      "aliases": ["notifications@example.com"],
      "display_name": "Conduit Agent"
    }
  }
}
```

The `from_alias` parameter on `email_send` is validated against `address` and `aliases`. If no `from_alias` is provided, the configured `address` is used as the sender.

## Actions

### email_search

Search Gmail using Gmail query syntax.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `action` | string | Yes | | Must be `"email_search"` |
| `query` | string | Yes | | Gmail search query (e.g. `is:unread`, `from:someone@example.com`) |
| `limit` | int | No | 10 | Maximum messages to return |

```json
{"action": "email_search", "query": "is:unread", "limit": 5}
{"action": "email_search", "query": "from:boss@company.com subject:urgent"}
```

### email_read

Read a specific email by message ID. Returns full message content including headers and body.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Must be `"email_read"` |
| `message_id` | string | Yes | Message ID (obtained from `email_search`) |

```json
{"action": "email_read", "message_id": "18abc123def456"}
```

### email_send

Compose and send an email. Builds an RFC 2822 message and sends it via the Gmail API.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Must be `"email_send"` |
| `to` | string | Yes | Recipient email address |
| `subject` | string | Yes | Email subject line |
| `body` | string | Yes | Plain-text email body |
| `cc` | string | No | CC recipients, comma-separated |
| `bcc` | string | No | BCC recipients, comma-separated |
| `from_alias` | string | No | Send from an alias instead of the primary address (must be in configured aliases) |

```json
{
  "action": "email_send",
  "to": "recipient@example.com",
  "subject": "Weekly Report",
  "body": "Here is the weekly status update.",
  "cc": "manager@example.com"
}
```

### email_trash

Move an email to the trash.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Must be `"email_trash"` |
| `message_id` | string | Yes | Message ID to trash |

```json
{"action": "email_trash", "message_id": "18abc123def456"}
```

### calendar_list

List upcoming calendar events within a time window.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `action` | string | Yes | | Must be `"calendar_list"` |
| `calendar_id` | string | No | `"primary"` | Calendar ID to query |
| `days` | int | No | 7 | Number of days to look ahead |
| `limit` | int | No | 10 | Maximum events to return |

Events are returned sorted by start time with `singleEvents: true` (recurring events expanded).

```json
{"action": "calendar_list"}
{"action": "calendar_list", "days": 14, "limit": 20}
```

### calendar_create

Create a new calendar event.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Must be `"calendar_create"` |
| `title` | string | Yes | Event title/summary |
| `start` | string | Yes | Start time in ISO 8601 format (e.g. `2024-03-15T10:00:00-07:00`) |
| `end` | string | Yes | End time in ISO 8601 format |
| `description` | string | No | Event description |
| `location` | string | No | Event location |
| `calendar_id` | string | No | Calendar ID (default `"primary"`) |

```json
{
  "action": "calendar_create",
  "title": "Team Standup",
  "start": "2026-04-07T09:00:00-07:00",
  "end": "2026-04-07T09:30:00-07:00",
  "location": "Conference Room B",
  "description": "Weekly sync"
}
```

### calendar_delete

Delete a calendar event by ID.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `action` | string | Yes | | Must be `"calendar_delete"` |
| `event_id` | string | Yes | | Event ID to delete |
| `calendar_id` | string | No | `"primary"` | Calendar containing the event |

```json
{"action": "calendar_delete", "event_id": "abc123xyz"}
```

## Troubleshooting

### gws CLI not found

The tool checks for gws availability before every call. If you see `gws_not_available`:

1. Verify gws is installed: `gws --version`
2. If installed in a non-standard location, set `gws_path` in your config
3. Make sure the user running Conduit has gws on their PATH

### Authentication errors

If gws returns auth errors:

1. Re-authenticate: `gws auth login`
2. Ensure the Google account has Gmail and Calendar API access enabled
3. Check that the OAuth consent screen includes the required scopes

### Invalid from_alias

The `email_send` action validates the `from_alias` parameter against the addresses configured in `agent.email.address` and `agent.email.aliases`. If validation fails, add the alias to your config or omit the parameter to use the default address.

### SelfTest

Run the tool's built-in diagnostic to verify everything is working:

```bash
bin/conduit tools selftest google_workspace
```

This checks gws CLI availability and reports configuration status. A `failed` result means gws is not installed; `ok` means the CLI is present and the tool is ready to use.
