# SSH Audit Logging

This document provides examples for configuring SSH audit logging in Conduit.

## Configuration

Add the `audit` section to your SSH remote configuration:

```json
{
  "tools": {
    "remote_ssh": {
      "enabled": true,
      "hosts": [
        {
          "name": "web-prod-1",
          "hostname": "web1.example.com",
          "port": 22,
          "user": "admin",
          "security_tier": "modify"
        }
      ],
      "audit": {
        "enabled": true,
        "log_path": "logs/ssh_audit.jsonl",
        "log_commands": true,
        "log_output": true,
        "max_output_capture": 65536,
        "retention_days": 90,
        "redact_secrets": true
      }
    }
  }
}
```

## Configuration Options

### audit.enabled
- Type: `boolean`
- Default: `true`
- Description: Controls whether audit logging is active

### audit.log_path
- Type: `string`
- Required when enabled: `yes`
- Description: Path to the JSONL audit log file

### audit.log_commands
- Type: `boolean`
- Default: `true`
- Description: Records all executed commands

### audit.log_output
- Type: `boolean`
- Default: `true`
- Description: Records command output (bounded by max_output_capture)

### audit.max_output_capture
- Type: `integer`
- Default: `65536` (64KB)
- Description: Maximum bytes of output to capture per command

### audit.retention_days
- Type: `integer`
- Default: `90`
- Description: Number of days to retain audit logs (0 = keep forever)

### audit.redact_secrets
- Type: `boolean`
- Default: `true`
- Description: Attempts to redact sensitive data from logs

## Audit Log Format

Audit logs are stored in JSONL (JSON Lines) format, with one JSON object per line:

```json
{"timestamp":"2026-03-17T15:30:45Z","session_id":"sess-abc123","user_id":"user-456","host":"web-prod-1","command":"ls -la","security_tier":"read","approved":true,"exit_code":0,"duration":"150ms","stdout":"total 24\ndrwxr-xr-x 3 user user 4096 Mar 17 15:30 .\n","stderr":""}
```

### Fields

- `timestamp`: ISO 8601 timestamp of command execution
- `session_id`: SSH session identifier (for persistent sessions)
- `user_id`: User who executed the command
- `host`: Target host name
- `command`: Executed command
- `security_tier`: Security classification (read, modify, dangerous, blocked)
- `approved`: Whether command was approved by security checks
- `approved_by`: User who approved the command (if applicable)
- `exit_code`: Command exit code
- `duration`: Command execution duration
- `stdout`: Standard output (truncated if exceeds max_output_capture)
- `stderr`: Standard error (truncated if exceeds max_output_capture)
- `error`: Error message if execution failed
- `timed_out`: Whether the command timed out

## Output Truncation

When output exceeds `max_output_capture`, it is truncated with a marker:

```json
{"stdout":"[first 65536 bytes of output]...[truncated]"}
```

## Retention Management

Audit logs are automatically cleaned up based on `retention_days`:

- Logs older than the retention period are removed
- Cleanup runs periodically (implementation-specific)
- Setting `retention_days: 0` disables automatic cleanup

## Querying Audit Logs

Since logs are in JSONL format, you can use standard tools to query them:

```bash
# Find all commands on a specific host
grep '"host":"web-prod-1"' logs/ssh_audit.jsonl

# Find failed commands
jq 'select(.exit_code != 0)' logs/ssh_audit.jsonl

# Find dangerous commands
jq 'select(.security_tier == "dangerous")' logs/ssh_audit.jsonl

# Commands from a specific user
jq 'select(.user_id == "user-456")' logs/ssh_audit.jsonl

# Commands in the last hour
jq --arg since "$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%S)" \
   'select(.timestamp > $since)' logs/ssh_audit.jsonl
```

## Security Considerations

1. **Permissions**: Ensure audit log files have appropriate permissions (600 or 640)
2. **Disk Space**: Monitor disk usage, especially with large `max_output_capture` values
3. **Sensitive Data**: Even with `redact_secrets` enabled, review logs for sensitive information
4. **Backup**: Include audit logs in your backup strategy
5. **Immutability**: Audit logs use append-only writes; consider using immutable storage

## Integration with Monitoring

You can integrate audit logs with log aggregation systems:

```bash
# Send to syslog
tail -f logs/ssh_audit.jsonl | logger -t ssh-audit

# Send to ELK stack
filebeat -c filebeat.yml

# Send to Splunk
splunk add monitor logs/ssh_audit.jsonl -sourcetype _json
```

## Example Queries

### Most frequently executed commands
```bash
jq -r '.command' logs/ssh_audit.jsonl | sort | uniq -c | sort -rn | head -10
```

### Average command duration by host
```bash
jq -r '"\(.host) \(.duration)"' logs/ssh_audit.jsonl | \
  awk '{a[$1]+=$2; c[$1]++} END {for(i in a) print i, a[i]/c[i]}'
```

### Failed commands with error messages
```bash
jq 'select(.exit_code != 0) | {host, command, exit_code, error}' logs/ssh_audit.jsonl
```
