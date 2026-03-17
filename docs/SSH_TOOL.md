# SSH Tool

The SSH tool enables Conduit to execute commands on remote hosts via SSH. It supports one-shot execution, fan-out across host groups, persistent sessions, port forwarding tunnels, SCP file transfer, and Ansible inventory integration -- all governed by a tiered security classification system with audit logging.

## Table of Contents

- [Quick Start](#quick-start)
- [Configuration](#configuration)
  - [Enabling the Tool](#enabling-the-tool)
  - [Defining Hosts](#defining-hosts)
  - [Host Groups](#host-groups)
  - [Connection Defaults](#connection-defaults)
  - [Connection Pool](#connection-pool)
  - [Security](#security)
  - [Audit Logging](#audit-logging)
  - [Persistent Sessions](#persistent-sessions)
- [Actions Reference](#actions-reference)
  - [exec](#exec)
  - [exec_group](#exec_group)
  - [hosts](#hosts)
  - [status](#status)
  - [session_start](#session_start)
  - [session_send](#session_send)
  - [session_close](#session_close)
  - [session_list](#session_list)
  - [tunnel_create](#tunnel_create)
  - [tunnel_close](#tunnel_close)
  - [tunnel_list](#tunnel_list)
  - [scp_upload](#scp_upload)
  - [scp_download](#scp_download)
  - [inventory_load](#inventory_load)
  - [inventory_list](#inventory_list)
  - [inventory_refresh](#inventory_refresh)
- [Security Model](#security-model)
  - [Command Tiers](#command-tiers)
  - [Classification Logic](#classification-logic)
  - [Blocked Patterns](#blocked-patterns)
  - [Subshells and Pipes](#subshells-and-pipes)
- [Ansible Inventory](#ansible-inventory)
  - [INI Format](#ini-format)
  - [YAML Format](#yaml-format)
  - [Dynamic Inventory](#dynamic-inventory)
  - [Precedence Rules](#precedence-rules)
- [Audit Logging](#audit-logging-1)
  - [Log Format](#log-format)
  - [Querying Logs](#querying-logs)
- [Examples](#examples)
  - [Minimal Configuration](#minimal-configuration)
  - [Production Configuration](#production-configuration)
  - [Common Workflows](#common-workflows)

---

## Quick Start

1. Add `"Ssh"` to your `enabled_tools` list in your config JSON
2. Add a `remote_ssh` section under `tools` with at least one host and security settings
3. Start Conduit -- the AI agent can now run commands on your remote hosts

Minimal config to get started:

```json
{
  "tools": {
    "enabled_tools": ["Ssh"],
    "remote_ssh": {
      "enabled": true,
      "hosts": [
        {
          "name": "my-server",
          "hostname": "192.168.1.100",
          "user": "deploy",
          "identity_file": "~/.ssh/id_ed25519"
        }
      ],
      "security": {
        "default_tier": "dangerous"
      },
      "audit": {
        "enabled": true,
        "log_path": "logs/ssh_audit.jsonl",
        "log_commands": true,
        "log_output": true,
        "redact_secrets": true
      }
    }
  }
}
```

---

## Configuration

The SSH tool is configured under `tools.remote_ssh` in your Conduit config JSON.

### Enabling the Tool

Two things are required:

1. Include `"Ssh"` in the `tools.enabled_tools` array
2. Set `tools.remote_ssh.enabled` to `true`

The tool is disabled by default for safety.

### Defining Hosts

Each host in the `hosts` array defines a target server:

```json
{
  "name": "web-prod-1",
  "hostname": "web1.example.com",
  "port": 22,
  "user": "deploy",
  "identity_file": "~/.ssh/id_ed25519",
  "groups": ["production", "web"],
  "tags": {"env": "prod", "role": "web"},
  "security_tier": "read",
  "enabled": true,
  "jump_host": "bastion@jump.example.com:22",
  "connect_timeout": "30s"
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | *required* | Unique identifier (alphanumeric, hyphens, underscores) |
| `hostname` | string | *required* | DNS name or IP address |
| `port` | int | 22 | SSH port |
| `user` | string | from defaults | SSH username |
| `identity_file` | string | from defaults | Path to SSH private key (absolute or `~/...`) |
| `groups` | []string | `[]` | Host group memberships |
| `tags` | map | `{}` | Arbitrary key-value metadata |
| `security_tier` | string | — | Override security tier: `read`, `modify`, `dangerous`, `blocked` |
| `enabled` | bool | `true` | Whether this host can be targeted |
| `jump_host` | string | — | Bastion host in `user@host:port` format |
| `connect_timeout` | duration | 30s | Connection timeout override |

### Host Groups

Host groups enable fan-out execution across multiple servers:

```json
{
  "host_groups": [
    {
      "name": "web-servers",
      "description": "Production web tier",
      "pattern": "web-prod-*",
      "security_tier": "read",
      "max_parallel": 10
    }
  ]
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | *required* | Group identifier |
| `description` | string | — | Human-readable purpose |
| `pattern` | string | — | Glob pattern to match host names (e.g., `web-prod-*`) |
| `security_tier` | string | — | Minimum security tier enforced for this group |
| `max_parallel` | int | 5 | Max concurrent executions within this group |

Hosts belong to a group if they list the group name in their `groups` array or if their `name` matches the group's `pattern` glob.

### Connection Defaults

Default values applied to hosts that don't specify their own:

```json
{
  "defaults": {
    "port": 22,
    "user": "deploy",
    "identity_file": "~/.ssh/id_ed25519",
    "connect_timeout": "30s"
  }
}
```

### Connection Pool

Controls how SSH connections are managed:

```json
{
  "pool": {
    "max_connections_per_host": 5,
    "max_total_connections": 50,
    "idle_timeout": "5m",
    "connect_timeout": "30s",
    "health_check_interval": "1m",
    "known_hosts_file": "~/.ssh/known_hosts",
    "strict_host_key_checking": "yes"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_connections_per_host` | int | 5 | Max connections per individual host |
| `max_total_connections` | int | 50 | Max connections across all hosts |
| `idle_timeout` | duration | 5m | Close idle connections after this period |
| `connect_timeout` | duration | 30s | Timeout for new connections |
| `health_check_interval` | duration | 1m | How often to verify connection health |
| `known_hosts_file` | string | — | Path to SSH known_hosts file |
| `strict_host_key_checking` | string | `"yes"` | Host key verification: `"yes"`, `"no"`, or `"accept-new"` |

### Security

The security section is the most critical part of the configuration. The `default_tier` field is **required** and must be `"dangerous"` or `"blocked"` -- you cannot set it to `"read"` or `"modify"` for safety reasons.

```json
{
  "security": {
    "default_tier": "dangerous",
    "require_approval": ["dangerous", "blocked"],
    "allow_subshells": false,
    "allow_pipes": true,
    "max_command_length": 10000,
    "approval_timeout": "5m",
    "allowed_commands": {
      "read": ["ls", "cat", "ps", "df", "uptime", "whoami", "hostname"],
      "modify": ["touch", "mkdir", "cp", "mv", "git", "docker"],
      "dangerous": ["rm", "kill", "systemctl", "apt-get"],
      "blocked": ["rm -rf /", "shutdown", "reboot", "dd", "mkfs"]
    },
    "blocked_patterns": [
      "rm\\s+(-[rf]+\\s+)*/$",
      ">\\s*/dev/[sh]d[a-z]",
      "curl.*\\|\\s*(ba)?sh"
    ]
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `default_tier` | string | *required* | Tier for unrecognized commands (`"dangerous"` or `"blocked"`) |
| `require_approval` | []string | `["dangerous", "blocked"]` | Tiers that require human approval |
| `allow_subshells` | bool | `false` | Allow `$()` and backtick command substitution |
| `allow_pipes` | bool | `true` | Allow pipe chains (`cmd1 \| cmd2`) |
| `max_command_length` | int | 10000 | Maximum command string length in bytes |
| `approval_timeout` | duration | 5m | How long approval requests remain valid |
| `allowed_commands` | object | see below | Commands whitelisted at each tier |
| `blocked_patterns` | []string | see below | Regex patterns that always block commands |

See [Security Model](#security-model) for full details on how commands are classified.

### Audit Logging

Audit logging is enabled by default and records all SSH operations to a JSONL file:

```json
{
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
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Whether audit logging is active |
| `log_path` | string | *required when enabled* | Path to the JSONL audit log file |
| `log_commands` | bool | `true` | Record executed commands |
| `log_output` | bool | `true` | Record command output |
| `max_output_capture` | int | 65536 (64KB) | Max bytes of output per command |
| `retention_days` | int | 90 | Days to retain logs (0 = keep forever) |
| `redact_secrets` | bool | `true` | Attempt to redact sensitive data |

### Persistent Sessions

Controls behavior of persistent shell sessions:

```json
{
  "sessions": {
    "max_concurrent_sessions": 5,
    "session_idle_timeout": "10m",
    "default_shell": "/bin/sh",
    "output_boundary_marker": "___CONDUIT_OUTPUT_BOUNDARY___"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_concurrent_sessions` | int | 5 | Max active persistent sessions |
| `session_idle_timeout` | duration | 10m | Close idle sessions after this period |
| `default_shell` | string | `/bin/sh` | Shell used for persistent sessions |
| `output_boundary_marker` | string | `___CONDUIT_OUTPUT_BOUNDARY___` | Internal delimiter for command output |

---

## Actions Reference

The SSH tool uses an `action` parameter to select the operation. All parameters are passed as a flat JSON object.

### exec

Execute a single command on a remote host.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"exec"` |
| `host` | string | yes | Target host name |
| `command` | string | yes | Command to execute |
| `timeout` | int | no | Timeout in seconds (default: 30) |

The command is classified by the security engine before execution. Blocked commands are rejected. The result includes stdout, stderr, exit code, and duration.

### exec_group

Execute a command across all hosts in a group (fan-out).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"exec_group"` |
| `group` | string | yes | Target host group name |
| `command` | string | yes | Command to execute |
| `timeout` | int | no | Timeout in seconds (default: 30) |
| `max_parallel` | int | no | Override max concurrent executions |

Runs the command on all enabled hosts in the group, bounded by `max_parallel` concurrency. Returns aggregated results with per-host output and a summary of succeeded/failed hosts.

### hosts

List all configured SSH hosts with their connection details and group memberships.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"hosts"` |

### status

Show the current state of the connection pool, active persistent sessions, and active tunnels.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"status"` |

### session_start

Start a persistent shell session on a host. The session maintains state (environment variables, working directory) across multiple commands.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"session_start"` |
| `host` | string | yes | Target host name |

Returns a `session_id` to use with subsequent `session_send` calls.

### session_send

Send a command to an existing persistent session.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"session_send"` |
| `session_id` | string | yes | Session ID from `session_start` |
| `command` | string | yes | Command to execute |
| `timeout` | int | no | Timeout in seconds (default: 30) |

### session_close

Close a persistent session and release its resources.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"session_close"` |
| `session_id` | string | yes | Session ID to close |

### session_list

List all active persistent sessions with their host, creation time, last used time, and command count.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"session_list"` |

### tunnel_create

Create a local SSH port forwarding tunnel. The local end always binds to `127.0.0.1` (localhost only) for security.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"tunnel_create"` |
| `host` | string | yes | SSH host to tunnel through |
| `local_port` | int | no | Local port to bind (0 = auto-assign, must be >= 1024) |
| `remote_host` | string | yes | Remote host to forward to (typically `"localhost"`) |
| `remote_port` | int | yes | Remote port to forward to |

Returns the tunnel ID and assigned local port.

### tunnel_close

Close an active tunnel.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"tunnel_close"` |
| `tunnel_id` | string | yes | Tunnel ID to close |

### tunnel_list

List all active tunnels with their local/remote endpoints and traffic stats.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"tunnel_list"` |

### scp_upload

Upload a local file to a remote host via SCP.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"scp_upload"` |
| `host` | string | yes | Target host name |
| `local_path` | string | yes | Path to local file |
| `remote_path` | string | yes | Destination path on remote host |

Classified as a "modify" tier operation.

### scp_download

Download a file from a remote host to a local path via SCP.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"scp_download"` |
| `host` | string | yes | Target host name |
| `remote_path` | string | yes | Path to file on remote host |
| `local_path` | string | yes | Destination path locally |

Classified as a "read" tier operation.

### inventory_load

Load hosts from an Ansible inventory file. Auto-detects INI or YAML format based on file extension.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"inventory_load"` |
| `path` | string | yes | Path to the inventory file |
| `type` | string | no | Set to `"dynamic"` for executable inventory scripts |

### inventory_list

List hosts from loaded inventories, optionally filtered by group.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"inventory_list"` |
| `group` | string | no | Filter by group name |

### inventory_refresh

Reload all previously loaded inventory sources.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | yes | `"inventory_refresh"` |

---

## Security Model

The SSH tool enforces a multi-layered security model that classifies every command before execution.

### Command Tiers

Commands are classified into four security tiers, from least to most restrictive:

| Tier | Meaning | Default Behavior |
|------|---------|------------------|
| **read** | Read-only, information-gathering commands | Allowed automatically |
| **modify** | State-changing but generally safe | Allowed automatically |
| **dangerous** | Could cause harm but may be necessary | Requires approval |
| **blocked** | Should never be executed | Always rejected |

**Default read commands:** `ls`, `cat`, `head`, `tail`, `grep`, `find`, `which`, `whereis`, `ps`, `top`, `htop`, `df`, `du`, `free`, `uptime`, `uname`, `whoami`, `hostname`, `id`, `groups`, `date`, `cal`, `pwd`, `env`, `printenv`, `echo`, `wc`, `sort`, `uniq`, `file`, `stat`, `lsof`, `netstat`, `ss`, `ip`, `ifconfig`, `dig`, `nslookup`, `host`, `ping`, `traceroute`, `curl`, `wget`, `journalctl`, `dmesg`, `last`, `lastlog`, `w`, `who`

**Default modify commands:** `touch`, `mkdir`, `cp`, `mv`, `ln`, `chmod`, `chown`, `tar`, `gzip`, `gunzip`, `zip`, `unzip`, `git`, `npm`, `yarn`, `pip`, `go`, `cargo`, `make`, `docker`, `docker-compose`, `kubectl`

**Default dangerous commands:** `rm`, `rmdir`, `kill`, `killall`, `pkill`, `systemctl`, `service`, `init`, `apt`, `apt-get`, `yum`, `dnf`, `pacman`, `brew`, `useradd`, `userdel`, `usermod`, `groupadd`, `groupdel`, `crontab`, `at`

### Classification Logic

When a command is submitted, the security engine evaluates it through these steps:

1. **Length check** -- reject if command exceeds `max_command_length` (default 10KB)
2. **Blocked pattern matching** -- check against regex `blocked_patterns`; reject on match
3. **Subshell detection** -- if `$()` or backticks are found and `allow_subshells` is false, reject
4. **Pipe chain analysis** -- if pipes are present and `allow_pipes` is true, classify each pipe segment and use the highest (most restrictive) tier
5. **I/O redirection detection** -- adds a warning if `>`, `>>`, or `<` are present
6. **Command prefix stripping** -- handles `sudo`, `timeout`, `env`, `nice`, `nohup` etc. to find the base command
7. **Tier lookup** -- match base command against `allowed_commands` tiers
8. **Argument escalation** -- certain arguments upgrade the tier (e.g., `rm -r` escalates from modify to dangerous, `chmod 777` escalates)
9. **Default tier** -- unrecognized commands fall to `default_tier` (must be `"dangerous"` or `"blocked"`)

### Blocked Patterns

The default configuration includes regex patterns that always block dangerous command forms:

- `rm -rf /` (recursive root deletion)
- Writing to raw block devices (`> /dev/sda`)
- `dd` targeting disk devices
- `mkfs` (filesystem formatting)
- Fork bombs
- Recursive `chmod 777 /`
- Piping `curl` or `wget` output to `sh` or `bash`
- Reading `/etc/shadow`
- Modifying `/etc/passwd`

You can add custom patterns in the `blocked_patterns` array.

### Subshells and Pipes

- **Subshells** (`$(command)` and `` `command` ``) are disabled by default (`allow_subshells: false`) because they can hide dangerous commands inside seemingly innocent ones
- **Pipes** are enabled by default (`allow_pipes: true`) because they're common and useful; the security engine classifies each segment of the pipe chain and applies the most restrictive tier

---

## Ansible Inventory

The SSH tool can import hosts from Ansible inventory files, letting you reuse existing infrastructure definitions.

### INI Format

Standard Ansible INI format with `[group]` sections:

```ini
[webservers]
web1.example.com ansible_user=deploy
web2.example.com ansible_user=deploy ansible_port=2222

[dbservers]
db1.example.com ansible_host=10.0.0.1 ansible_user=postgres
db2.example.com ansible_host=10.0.0.2 ansible_user=postgres

# Group of groups
[production:children]
webservers
dbservers
```

### YAML Format

Ansible YAML inventory with nested structure:

```yaml
all:
  children:
    webservers:
      hosts:
        web1.example.com:
          ansible_user: deploy
          ansible_port: 22
        web2.example.com:
          ansible_user: deploy
          ansible_port: 2222
    dbservers:
      hosts:
        db1.example.com:
          ansible_host: 10.0.0.1
          ansible_user: postgres
```

### Dynamic Inventory

Executable scripts that output JSON in Ansible's dynamic inventory format:

```bash
#!/bin/bash
cat <<EOF
{
  "webservers": {
    "hosts": ["web1.example.com", "web2.example.com"]
  },
  "_meta": {
    "hostvars": {
      "web1.example.com": {
        "ansible_user": "deploy",
        "ansible_port": 22
      }
    }
  }
}
EOF
```

Load dynamic inventories with `type: "dynamic"`:
```json
{"action": "inventory_load", "path": "/path/to/inventory.sh", "type": "dynamic"}
```

### Supported Ansible Variables

| Ansible Variable | Maps To |
|-----------------|---------|
| `ansible_host` | Hostname (IP or DNS) |
| `ansible_user` | SSH username |
| `ansible_port` | SSH port |
| `ansible_ssh_private_key_file` | SSH private key path |

### Precedence Rules

- **Config hosts always take precedence over inventory hosts.** If a host appears in both the config file and an inventory file, the config settings win.
- Multiple inventory files can be loaded; hosts are merged by name and groups are accumulated.
- Group hierarchies via `[group:children]` (INI) or `children` keys (YAML) are fully supported.

---

## Audit Logging

### Log Format

Audit logs are stored in JSONL (JSON Lines) format -- one JSON object per line:

```json
{"timestamp":"2026-03-17T15:30:45Z","session_id":"sess-abc123","user_id":"user-456","host":"web-prod-1","command":"ls -la","security_tier":"read","approved":true,"exit_code":0,"duration":"150ms","stdout":"total 24\ndrwxr-xr-x...","stderr":""}
```

Fields: `timestamp`, `session_id`, `user_id`, `host`, `command`, `security_tier`, `approved`, `approved_by`, `exit_code`, `duration`, `stdout`, `stderr`, `error`, `timed_out`.

Output exceeding `max_output_capture` is truncated with a `...[truncated]` marker.

### Querying Logs

JSONL format works well with standard tools:

```bash
# All commands on a specific host
grep '"host":"web-prod-1"' logs/ssh_audit.jsonl

# Failed commands
jq 'select(.exit_code != 0)' logs/ssh_audit.jsonl

# Dangerous commands
jq 'select(.security_tier == "dangerous")' logs/ssh_audit.jsonl

# Most frequently executed commands
jq -r '.command' logs/ssh_audit.jsonl | sort | uniq -c | sort -rn | head -10

# Failed commands with error messages
jq 'select(.exit_code != 0) | {host, command, exit_code, error}' logs/ssh_audit.jsonl
```

Logs can also be forwarded to syslog, ELK, Splunk, or other aggregation systems via `tail -f` or file-based ingest.

---

## Examples

### Minimal Configuration

A single host with default security settings:

```json
{
  "tools": {
    "enabled_tools": ["Ssh"],
    "remote_ssh": {
      "enabled": true,
      "hosts": [
        {
          "name": "my-server",
          "hostname": "192.168.1.100",
          "user": "admin",
          "identity_file": "~/.ssh/id_ed25519"
        }
      ],
      "security": {
        "default_tier": "dangerous"
      },
      "audit": {
        "enabled": true,
        "log_path": "logs/ssh_audit.jsonl",
        "log_commands": true,
        "log_output": true,
        "redact_secrets": true
      }
    }
  }
}
```

### Production Configuration

Multiple hosts with groups, bastion access, strict security, and full audit:

```json
{
  "tools": {
    "enabled_tools": ["Ssh"],
    "remote_ssh": {
      "enabled": true,
      "hosts": [
        {
          "name": "web-prod-1",
          "hostname": "web1.example.com",
          "user": "deploy",
          "identity_file": "~/.ssh/id_ed25519",
          "groups": ["production", "web"],
          "security_tier": "read"
        },
        {
          "name": "web-prod-2",
          "hostname": "web2.example.com",
          "user": "deploy",
          "identity_file": "~/.ssh/id_ed25519",
          "groups": ["production", "web"],
          "security_tier": "read"
        },
        {
          "name": "db-prod",
          "hostname": "10.0.1.50",
          "user": "dbadmin",
          "identity_file": "~/.ssh/db_key",
          "groups": ["production", "database"],
          "security_tier": "read",
          "jump_host": "bastion@jump.example.com:22"
        }
      ],
      "host_groups": [
        {
          "name": "production",
          "description": "All production servers",
          "security_tier": "read",
          "max_parallel": 10
        },
        {
          "name": "web",
          "description": "Web tier",
          "pattern": "web-prod-*",
          "max_parallel": 5
        },
        {
          "name": "database",
          "description": "Database servers",
          "max_parallel": 1
        }
      ],
      "defaults": {
        "port": 22,
        "connect_timeout": "30s"
      },
      "security": {
        "default_tier": "blocked",
        "require_approval": ["dangerous", "blocked"],
        "allow_subshells": false,
        "allow_pipes": true,
        "max_command_length": 10000,
        "blocked_patterns": [
          "rm\\s+(-[rf]+\\s+)*/$",
          ">\\s*/dev/[sh]d[a-z]",
          "curl.*\\|\\s*(ba)?sh",
          "wget.*\\|\\s*(ba)?sh"
        ]
      },
      "pool": {
        "max_connections_per_host": 5,
        "max_total_connections": 50,
        "idle_timeout": "5m",
        "connect_timeout": "30s",
        "health_check_interval": "1m",
        "strict_host_key_checking": "yes"
      },
      "audit": {
        "enabled": true,
        "log_path": "logs/ssh_audit.jsonl",
        "log_commands": true,
        "log_output": true,
        "max_output_capture": 65536,
        "retention_days": 90,
        "redact_secrets": true
      },
      "sessions": {
        "max_concurrent_sessions": 5,
        "session_idle_timeout": "10m",
        "default_shell": "/bin/bash"
      }
    }
  }
}
```

### Common Workflows

**Check uptime across all web servers:**
```
"Run uptime on the web group"
→ action=exec_group, group="web", command="uptime"
```

**Debug a production issue with a persistent session:**
```
"Start a session on web-prod-1"
→ action=session_start, host="web-prod-1"

"Check the application logs"
→ action=session_send, session_id="...", command="tail -100 /var/log/app/error.log"

"Check disk space"
→ action=session_send, session_id="...", command="df -h"

"Done investigating"
→ action=session_close, session_id="..."
```

**Create a tunnel to a database behind a bastion:**
```
"Create a tunnel to the production database on port 5432"
→ action=tunnel_create, host="db-prod", local_port=5433, remote_host="localhost", remote_port=5432

(Connect your local DB client to localhost:5433)

"Close the database tunnel"
→ action=tunnel_close, tunnel_id="..."
```

**Import an existing Ansible inventory:**
```
"Load our Ansible inventory"
→ action=inventory_load, path="/etc/ansible/hosts"

"Show me all the webservers"
→ action=inventory_list, group="webservers"

"Check disk space on all webservers"
→ action=exec_group, group="webservers", command="df -h"
```

**Transfer a config file to a server:**
```
"Upload the new nginx config"
→ action=scp_upload, host="web-prod-1", local_path="./nginx.conf", remote_path="/etc/nginx/nginx.conf"
```
