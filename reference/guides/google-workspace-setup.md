# Google Workspace Integration

Conduit can optionally integrate with Google Workspace (Gmail, Calendar) via the `gws` CLI.

## Prerequisites

### 1. Install gws CLI

**NPM (Recommended):**
```bash
npm install -g @googleworkspace/cli
```

**Nix:**
```bash
nix run github:googleworkspace/cli
```

**From source (requires Rust):**
```bash
cargo install --git https://github.com/googleworkspace/cli --locked
```

### 2. Authenticate

```bash
# Interactive OAuth login (opens browser)
gws auth login
```

This opens a browser for OAuth consent. Follow the prompts to authorize access.

### 3. Verify Installation

```bash
gws --version
gws gmail users messages list --params '{"userId":"me","maxResults":1}'
```

## Configuration

Add to your config.json:

```json
{
  "agent": {
    "email": {
      "address": "your-agent@example.com",
      "aliases": ["alias@example.com"],
      "display_name": "Agent Name"
    }
  },
  "tools": {
    "enabled_tools": ["google_workspace"],
    "services": {
      "google_workspace": {
        "gws_path": "gws",
        "user_id": "me"
      }
    }
  }
}
```

### Agent Email Integration

The `agent.email` configuration is used by the Google Workspace tool to:
- **Auto-populate "from" address** when sending emails
- **Validate aliases** to prevent sending from unauthorized addresses

See the [Agent Email Guide](agent-email.md) for complete documentation on email identity configuration.

### Tool Configuration Options

| Field | Default | Description |
|-------|---------|-------------|
| `gws_path` | `"gws"` | Path to gws binary. Use full path if not in PATH |
| `user_id` | `"me"` | Gmail/Calendar user ID (usually "me" for authenticated user) |

## Available Actions

### Email Actions

| Action | Required Args | Optional Args | Description |
|--------|---------------|---------------|-------------|
| `email_search` | `query` | `limit` | Search emails using Gmail query syntax |
| `email_read` | `message_id` | | Read a specific email by ID |
| `email_send` | `to`, `subject`, `body` | `cc`, `bcc`, `from_alias` | Send an email |
| `email_trash` | `message_id` | | Move email to trash |

### Calendar Actions

| Action | Required Args | Optional Args | Description |
|--------|---------------|---------------|-------------|
| `calendar_list` | | `limit`, `days`, `calendar_id` | List upcoming events |
| `calendar_create` | `title`, `start`, `end` | `description`, `location`, `calendar_id` | Create a calendar event |
| `calendar_delete` | `event_id` | `calendar_id` | Delete a calendar event |

## Example Usage

### Search Unread Emails
```json
{
  "action": "email_search",
  "query": "is:unread",
  "limit": 10
}
```

### Read an Email
```json
{
  "action": "email_read",
  "message_id": "18e1234567890abc"
}
```

### Send an Email
```json
{
  "action": "email_send",
  "to": "recipient@example.com",
  "subject": "Hello",
  "body": "This is a test email.",
  "from_alias": "alias@example.com"
}
```

### List Calendar Events
```json
{
  "action": "calendar_list",
  "days": 7,
  "limit": 10
}
```

### Create Calendar Event
```json
{
  "action": "calendar_create",
  "title": "Team Meeting",
  "start": "2024-03-15T10:00:00-07:00",
  "end": "2024-03-15T11:00:00-07:00",
  "description": "Weekly sync",
  "location": "Conference Room A"
}
```

## Headless/Server Setup

For servers without a browser:

### Option 1: Export Credentials

1. On a machine with a browser:
   ```bash
   gws auth login
   gws auth export
   ```

2. On the server:
   ```bash
   export GOOGLE_WORKSPACE_CLI_TOKEN="..."
   ```

### Option 2: Service Account

1. Create a service account in Google Cloud Console
2. Download the JSON credentials file
3. Set the environment variable:
   ```bash
   export GOOGLE_WORKSPACE_CLI_CREDENTIALS_FILE=/path/to/service-account.json
   ```

Note: Service accounts require domain-wide delegation to access user data.

## Troubleshooting

### "gws CLI not found"

The tool gracefully handles missing gws installation:
- Returns helpful error with installation instructions
- Conduit continues to function normally
- The tool simply won't be available

### "Authentication required"

Run `gws auth login` to authenticate. For headless environments, use the token export or service account methods above.

### Gmail Search Query Syntax

The `query` parameter uses Gmail search syntax:
- `is:unread` — Unread messages
- `from:someone@example.com` — From specific sender
- `to:me` — Sent to you
- `subject:meeting` — Subject contains "meeting"
- `after:2024/03/01` — After date
- `has:attachment` — Has attachments
- `label:important` — Has specific label

Combine with AND (space) or OR:
- `is:unread from:boss@example.com`
- `is:unread OR is:starred`

## Security Considerations

1. **Agent Email Config**: The tool validates `from_alias` against the configured agent email addresses/aliases to prevent sending from unauthorized addresses.

2. **OAuth Tokens**: Store tokens securely. Use environment variables rather than hardcoding in config files.

3. **Service Accounts**: If using domain-wide delegation, limit the scopes to only what's needed.

## Links

- [gws CLI GitHub](https://github.com/googleworkspace/cli)
- [Gmail Search Operators](https://support.google.com/mail/answer/7190)
- [Google Calendar API](https://developers.google.com/calendar/api/v3/reference)
