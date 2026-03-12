# Agent Email Identity

This guide explains how to configure and use the agent email identity system in Conduit.

## Overview

The agent email identity system provides:
1. **Automatic prompt injection** — The agent's email address is included in the system prompt, so the agent knows its email identity
2. **Tool integration** — Tools like `google_workspace` can access the configured email to send messages from the correct address
3. **Alias validation** — When sending emails, the system validates that the "from" address is an authorized address/alias

## Configuration

Add email configuration under the `agent` section in your config JSON:

```json
{
  "agent": {
    "name": "Conduit",
    "email": {
      "address": "conduit@example.com",
      "aliases": ["assistant@example.com", "bot@example.com"],
      "display_name": "Conduit Assistant"
    }
  }
}
```

### Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `address` | string | `""` | Primary email address for the agent |
| `aliases` | array | `[]` | Additional email addresses the agent recognizes as its own |
| `display_name` | string | Agent name | Display name for outgoing emails |

### Behavior

- **When `address` is empty**: The email section is completely omitted from the system prompt. No email identity is established.
- **When `display_name` is empty**: Falls back to the agent's `name` field (e.g., "Conduit").
- **Aliases**: The agent recognizes messages sent to any of its aliases as addressed to itself.

## System Prompt Integration

When email is configured, the following section is automatically added to the system prompt (priority P2, alongside messaging configuration):

```
## Email
Your email address: conduit@example.com
Display name: Conduit Assistant
Aliases: assistant@example.com, bot@example.com
Use this address as your "from" identity when composing or referencing email. Recognize messages to any of these addresses as addressed to you.
```

This ensures the agent:
- Knows its email identity for composing messages
- Recognizes emails addressed to any of its addresses/aliases
- Uses the correct display name in signatures

## Tool Integration

### Accessing Email Config in Tools

Tools access the email configuration through the service layer:

```go
services := registry.GetServices()
if services != nil && services.ConfigMgr != nil {
    email := services.ConfigMgr.Agent.Email
    if email.Address != "" {
        // Use email.Address, email.Aliases, email.DisplayName
    }
}
```

### Google Workspace Integration

The `google_workspace` tool integrates with agent email in two ways:

1. **Auto-populating "from" address**: When sending email without specifying `from_alias`, the tool automatically uses the configured `address`.

2. **Alias validation**: When `from_alias` is specified, it's validated against both the primary address and all aliases. Invalid aliases are rejected:

```json
// This will fail if "unknown@example.com" is not configured
{
  "action": "email_send",
  "to": "recipient@example.com",
  "subject": "Hello",
  "body": "Message body",
  "from_alias": "unknown@example.com"
}
```

Error response:
```json
{
  "success": false,
  "error": "from_alias 'unknown@example.com' not in configured addresses/aliases"
}
```

### Example: Sending Email

```json
// Send from primary address (automatic)
{
  "action": "email_send",
  "to": "user@example.com",
  "subject": "Status Update",
  "body": "Everything is running smoothly."
}

// Send from a specific alias
{
  "action": "email_send",
  "to": "user@example.com",
  "subject": "Status Update",
  "body": "Everything is running smoothly.",
  "from_alias": "assistant@example.com"
}
```

## Use Cases

### 1. Personal AI Assistant

Configure the agent with your custom domain:

```json
{
  "agent": {
    "name": "Assistant",
    "email": {
      "address": "ai@yourdomain.com",
      "display_name": "AI Assistant"
    }
  }
}
```

### 2. Multi-Domain Support

Use aliases to handle multiple domains:

```json
{
  "agent": {
    "name": "Conduit",
    "email": {
      "address": "conduit@primary.com",
      "aliases": [
        "conduit@secondary.com",
        "assistant@primary.com"
      ],
      "display_name": "Conduit"
    }
  }
}
```

### 3. Team Shared Agent

Configure the agent to respond to a team mailbox:

```json
{
  "agent": {
    "name": "TeamBot",
    "email": {
      "address": "team-bot@company.com",
      "aliases": ["support@company.com"],
      "display_name": "Team Assistant"
    }
  }
}
```

## Security Considerations

1. **Alias validation prevents spoofing**: The agent can only send from addresses explicitly configured in `address` or `aliases`. It cannot impersonate arbitrary email addresses.

2. **OAuth scope alignment**: When using Google Workspace, ensure the OAuth scopes include permission to send as the configured addresses. Gmail's "Send As" settings must be configured for aliases.

3. **Config file security**: Since the email config contains identity information, protect your config files appropriately.

## Troubleshooting

### Email section not appearing in prompt

- Verify `address` is not empty in your config
- Check that the config file is being loaded (look for startup logs)
- Confirm there are no JSON syntax errors in the config

### "invalid_alias" error when sending

- Verify the `from_alias` matches exactly (case-sensitive) one of:
  - The primary `address`
  - One of the `aliases`
- Check for typos or extra whitespace

### Agent doesn't recognize emails to aliases

- Ensure aliases are properly configured as an array
- Verify JSON syntax: `"aliases": ["one@example.com", "two@example.com"]`

## Related Documentation

- [Configuration Reference](../configuration.md) — Full config documentation
- [Google Workspace Setup](google-workspace-setup.md) — Gmail/Calendar integration
