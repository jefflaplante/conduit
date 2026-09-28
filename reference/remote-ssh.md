# Remote SSH Integration

Conduit can execute commands on remote hosts via SSH with security tiers, audit logging, persistent sessions, port forwarding tunnels, and SCP file transfers. The SSH tool supports single-host execution, fan-out across host groups, and Ansible inventory loading.

## Overview

```
Config (remote_ssh)
        │
        ▼
┌─────────────────┐
│  SSHTool         │  ← action-based dispatch (16 actions)
│  (internal/      │
│   tools/ssh)     │
└────────┬────────┘
         │
    ┌────┴─────────────────────────┐
    │                              │
    ▼                              ▼
┌──────────────┐   ┌────────────────────┐
│ SecurityEngine│   │  Connection Pool    │
│ classify →    │   │  per-host pooling,  │
│ approve/block │   │  fan-out executor   │
└──────────────┘   └────────┬───────────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
         SessionMgr    TunnelMgr     AuditLogger
         (persistent   (local port   (JSONL log,
          shells)       forwarding)   redaction)
```

## Enabling Remote Execution

The tool runs real commands on real hosts (conduit-enf0), so it is off
unless all of the following are true:

1. **Tagged build.** The tool is only compiled with the `with_ssh` build tag:
   `make build-custom TOOLS="ssh"` (just SSH), `make build-full` (all
   optional tools), or `go build -tags with_ssh ./cmd/gateway`. A default
   `make build` binary contains no SSH tool at all.
2. **Tool enabled.** `"Ssh"` is in `tools.enabled_tools`.
3. **Config enabled.** A top-level `remote_ssh` block (not under `tools`)
   with `"enabled": true`, a `security.default_tier`, and at least one host.
4. **Host keys known.** Every host (and jump host) key is in the known_hosts
   file (`pool.known_hosts_file`, default `~/.ssh/known_hosts`) with the
   default `strict_host_key_checking: "yes"`. Verify fingerprints out of
   band, then e.g. `ssh-keyscan -p 22 10.0.1.10 >> ~/.ssh/known_hosts`.
   `"accept-new"` trusts a first-seen key (and still refuses a changed one);
   `"no"` is refused, since host keys are always verified.
5. **Credentials.** Unencrypted private key files (`identity_file`, or
   `~/.ssh/id_ed25519`/`id_rsa`/`id_ecdsa`) and/or keys in the ssh-agent at
   `SSH_AUTH_SOCK`. Passphrase-protected keys are not supported; use the
   agent for those. Keep host names, users and key paths out of the checked-in
   config with `${ENV_VAR}` expansion if you prefer.
6. **An approval channel.** Dangerous-tier operations wait for the owner's
   `YES <code>` on an interactive channel (Telegram, TUI, WebSocket); without
   one they are refused (see [Human Approval](#human-approval)).

Minimal enablement (durations omitted so defaults apply; see the note below):

```json
{
  "tools": { "enabled_tools": ["Ssh"] },
  "remote_ssh": {
    "enabled": true,
    "hosts": [
      {
        "name": "nas",
        "hostname": "${NAS_SSH_HOST}",
        "user": "${NAS_SSH_USER}",
        "identity_file": "${CONDUIT_SSH_KEY}",
        "security_tier": "dangerous"
      }
    ],
    "security": {
      "default_tier": "dangerous",
      "require_approval": ["dangerous", "blocked"],
      "approval_timeout": 600000000000
    },
    "pool": {
      "known_hosts_file": "~/.conduit/ssh_known_hosts",
      "strict_host_key_checking": "yes"
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
```

The `remote_ssh` block is decoded onto zero values, not merged with the
built-in defaults, so anything you omit is empty. Two safety defaults are
re-applied when absent (`null`): `require_approval` (`["dangerous",
"blocked"]`) and the hard blocks (`blocked_patterns` and
`allowed_commands.blocked`); an explicit `[]` still means none. Omitting
`allowed_commands.read`/`modify` makes every command fall to `default_tier`
(so even `ls` needs approval) — copy the lists below to relax that.

> **Durations are nanoseconds.** Duration fields (`approval_timeout`,
> `idle_timeout`, `connect_timeout`, `session_idle_timeout`, ...) are Go
> `time.Duration` values and must be JSON integers in nanoseconds
> (`600000000000` = 10m). The config loader rejects strings like `"5m"`
> today; the examples on this page that show strings are illustrative only.

Connections are dialled lazily on first use (nothing connects at startup),
pooled per host (`pool.*` limits) and closed when the gateway shuts down.
`status` shows pool usage; the tool's SelfTest with `CheckDependencies`
dials each enabled host once (no command runs) to report reachability and
host-key problems.

## Configuration

Add a `remote_ssh` section to your config JSON:

```json
{
  "remote_ssh": {
    "enabled": true,
    "hosts": [
      {
        "name": "web-prod-1",
        "hostname": "10.0.1.10",
        "user": "deploy",
        "identity_file": "~/.ssh/id_ed25519",
        "groups": ["web-servers", "production"],
        "security_tier": "modify",
        "tags": {"env": "prod", "role": "web"}
      },
      {
        "name": "db-server",
        "hostname": "10.0.1.20",
        "user": "deploy",
        "identity_file": "~/.ssh/id_ed25519",
        "groups": ["databases"],
        "security_tier": "read"
      }
    ],
    "host_groups": [
      {
        "name": "web-servers",
        "description": "Production web tier",
        "pattern": "web-prod-*",
        "security_tier": "modify",
        "max_parallel": 3
      }
    ],
    "defaults": {
      "port": 22,
      "user": "deploy",
      "identity_file": "~/.ssh/id_ed25519",
      "connect_timeout": "30s"
    },
    "security": {
      "default_tier": "dangerous",
      "require_approval": ["dangerous", "blocked"],
      "allow_subshells": false,
      "allow_pipes": true,
      "max_command_length": 10000,
      "allowed_commands": {
        "read": ["ls", "cat", "ps", "df", "free", "uptime", "whoami", "hostname", "journalctl"],
        "modify": ["touch", "mkdir", "cp", "mv", "git", "docker", "kubectl"],
        "dangerous": ["rm", "kill", "systemctl", "apt-get"],
        "blocked": ["rm -rf /", "dd", "mkfs", "shutdown", "reboot"]
      },
      "blocked_patterns": [
        "rm\\s+(-[rf]+\\s+)*/$",
        ">\\s*/dev/[sh]d[a-z]",
        "curl.*\\|\\s*(ba)?sh"
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
      "default_shell": "/bin/sh"
    }
  }
}
```

### Host Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | **required** | Unique host identifier (alphanumeric, hyphens, underscores) |
| `hostname` | string | **required** | DNS name or IP address |
| `port` | int | `22` | SSH port |
| `user` | string | from defaults | SSH username |
| `identity_file` | string | from defaults | Path to SSH private key (supports `~`) |
| `groups` | string[] | `[]` | Host group memberships |
| `tags` | object | `{}` | Arbitrary key-value metadata |
| `security_tier` | string | — | Per-host tier override: `read`, `modify`, `dangerous`, `blocked` |
| `enabled` | bool | `true` | Whether this host can be targeted |
| `jump_host` | string | — | Bastion host name for proxied connections |
| `connect_timeout` | duration | from defaults | Connection timeout override |

### Host Group Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | **required** | Group identifier |
| `description` | string | — | Purpose of the group |
| `pattern` | string | — | Glob pattern to auto-match host names (e.g., `web-prod-*`) |
| `security_tier` | string | — | Minimum security tier for commands on this group |
| `max_parallel` | int | from pool config | Max concurrent executions within this group |

Hosts belong to a group if they list the group name in their `groups` array or match the group's `pattern` glob.

### Security Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `default_tier` | string | `"dangerous"` | Tier for unclassified commands (must be `dangerous` or `blocked`) |
| `require_approval` | string[] | `["dangerous", "blocked"]` | Tiers that require human approval (an absent list uses the default; `[]` disables) |
| `allowed_commands.read` | string[] | ~40 commands | Read-only commands (ls, ps, df, etc.) |
| `allowed_commands.modify` | string[] | ~15 commands | State-changing commands (touch, mkdir, git, docker, etc.) |
| `allowed_commands.dangerous` | string[] | ~15 commands | Potentially harmful commands (rm, kill, systemctl, etc.) |
| `allowed_commands.blocked` | string[] | ~15 patterns | Never-execute commands (rm -rf /, dd, mkfs, etc.) |
| `blocked_patterns` | string[] | ~10 regexes | Regex patterns that always block commands |
| `allow_subshells` | bool | `false` | Permit `$()` and backtick command substitution |
| `allow_pipes` | bool | `true` | Permit pipe chains |
| `max_command_length` | int | `10000` | Maximum command string length |
| `approval_timeout` | duration (ns) | `0` = gateway default (5m) | How long an SSH approval prompt stays valid; capped at 1h |

### Pool Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_connections_per_host` | int | `5` | Max connections per host |
| `max_total_connections` | int | `50` | Max total pool connections |
| `idle_timeout` | duration | `5m` | Close idle connections after this |
| `connect_timeout` | duration | `30s` | Default connection timeout |
| `health_check_interval` | duration | `1m` | Connection health check interval |
| `known_hosts_file` | string | `~/.ssh/known_hosts` | Path to SSH known_hosts file (must exist for `yes`) |
| `strict_host_key_checking` | string | `"yes"` | `yes` (known hosts only) or `accept-new` (trust on first use, refuse changed keys); `no` is refused |

### Audit Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable audit logging |
| `log_path` | string | `"logs/ssh_audit.jsonl"` | JSONL audit log path |
| `log_commands` | bool | `true` | Record all executed commands |
| `log_output` | bool | `true` | Record command output |
| `max_output_capture` | int | `65536` | Max output capture in bytes (64KB) |
| `retention_days` | int | `90` | Audit log retention |
| `redact_secrets` | bool | `true` | Attempt to redact sensitive data |

### Session Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_concurrent_sessions` | int | `5` | Max active persistent sessions |
| `session_idle_timeout` | duration | `10m` | Auto-close idle sessions |
| `default_shell` | string | `"/bin/sh"` | Shell for persistent sessions |

## Actions

All actions use the `Ssh` tool with an `action` parameter.

### exec

Execute a one-shot command on a single host.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"exec"` |
| `host` | string | Yes | Target host name |
| `command` | string | Yes | Command to execute |
| `timeout` | int | No | Timeout in seconds (default: 30) |

```json
{"action": "exec", "host": "web-prod-1", "command": "df -h"}
{"action": "exec", "host": "db-server", "command": "ps aux | grep postgres", "timeout": 60}
```

### exec_group

Fan-out a command to all hosts in a group. The strictest security tier across all hosts in the group is applied.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"exec_group"` |
| `group` | string | Yes | Target host group name |
| `command` | string | Yes | Command to execute |
| `timeout` | int | No | Timeout in seconds (default: 30) |
| `max_parallel` | int | No | Override max parallel executions |

```json
{"action": "exec_group", "group": "web-servers", "command": "uptime"}
{"action": "exec_group", "group": "web-servers", "command": "df -h", "max_parallel": 2}
```

### hosts

List all configured hosts and host groups.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"hosts"` |

```json
{"action": "hosts"}
```

### status

Show connection pool statistics, security configuration, session count, and tunnel count.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"status"` |

```json
{"action": "status"}
```

### session_start

Start a persistent shell session on a host. Sessions maintain state (environment variables, working directory) between commands.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"session_start"` |
| `host` | string | Yes | Target host name |

```json
{"action": "session_start", "host": "web-prod-1"}
```

Returns a `session_id` for use with `session_send` and `session_close`.

### session_send

Send a command to an existing persistent session.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"session_send"` |
| `session_id` | string | Yes | Session ID from session_start |
| `command` | string | Yes | Command to execute |
| `timeout` | int | No | Timeout in seconds (default: 30) |

```json
{"action": "session_send", "session_id": "abc123", "command": "cd /var/log"}
{"action": "session_send", "session_id": "abc123", "command": "tail -20 app.log"}
```

### session_close

Close a persistent session.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"session_close"` |
| `session_id` | string | Yes | Session ID to close |

```json
{"action": "session_close", "session_id": "abc123"}
```

### session_list

List all active persistent sessions.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"session_list"` |

```json
{"action": "session_list"}
```

### tunnel_create

Create a local port forwarding tunnel through an SSH host. Tunnels bind only to `127.0.0.1` for security.
A tunnel exposes the remote network to every local process, so it is
**dangerous-tier**: refused on hosts whose `security_tier` is `read` or
`modify`, and approval-gated under the default `require_approval`. The
tunnel holds one pooled connection until it is closed.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"tunnel_create"` |
| `host` | string | Yes | SSH host to tunnel through |
| `local_port` | int | No | Local port to bind (0 for auto-assign, must be >= 1024) |
| `remote_host` | string | Yes | Remote host to forward to (typically `"localhost"`) |
| `remote_port` | int | Yes | Remote port to forward to |

```json
{"action": "tunnel_create", "host": "db-server", "local_port": 3307, "remote_host": "localhost", "remote_port": 3306}
{"action": "tunnel_create", "host": "cache-server", "local_port": 0, "remote_host": "localhost", "remote_port": 6379}
```

After creation, connect to `127.0.0.1:<local_port>` to reach the remote service.

### tunnel_close

Close an active tunnel.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"tunnel_close"` |
| `tunnel_id` | string | Yes | Tunnel ID to close |

```json
{"action": "tunnel_close", "tunnel_id": "abc-123-def"}
```

### tunnel_list

List all active tunnels with connection stats.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"tunnel_list"` |

```json
{"action": "tunnel_list"}
```

### scp_upload

Upload a local file to a remote host. Classified as **dangerous-tier**
(it writes arbitrary content to an arbitrary remote path, e.g.
`authorized_keys` or a cron file): approval-gated under the default
`require_approval`, and refused on hosts whose `security_tier` is `read` or
`modify`. `local_path` must be inside the tools sandbox; the approval is
bound to the resolved file and its size. The remote path is shell-quoted.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"scp_upload"` |
| `host` | string | Yes | Target host name |
| `local_path` | string | Yes | Local file path |
| `remote_path` | string | Yes | Remote destination path |

```json
{"action": "scp_upload", "host": "web-prod-1", "local_path": "/tmp/data.json", "remote_path": "/var/www/html/data.json"}
```

Only single files are supported (no directories).

### scp_download

Download a file from a remote host. Classified as read-tier (allowed on any
host tier; gated only if `read` is in `require_approval`). `remote_path` is
checked against `blocked_patterns` (so `/etc/shadow` is refused) and
shell-quoted; `local_path` must be inside the tools sandbox.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"scp_download"` |
| `host` | string | Yes | Target host name |
| `remote_path` | string | Yes | Remote file path |
| `local_path` | string | Yes | Local destination path |

```json
{"action": "scp_download", "host": "web-prod-1", "remote_path": "/var/log/app.log", "local_path": "/tmp/app.log"}
```

## Security Model

Every command is classified by the SecurityEngine before execution. The engine extracts the base command, checks it against the configured tier lists, and applies additional checks.

### Tiers

| Tier | Description | Default Behavior |
|------|-------------|------------------|
| **read** | Read-only commands (ls, ps, df, cat) | Allowed |
| **modify** | State-changing but generally safe (touch, mkdir, git) | Allowed |
| **dangerous** | Could cause harm (rm, kill, systemctl) | Requires approval |
| **blocked** | Never execute (rm -rf /, dd, mkfs, shutdown) | Rejected |

Unknown commands default to the `default_tier` setting, which must be `dangerous` or `blocked` for safety.

### Human Approval

Operations in a `require_approval` tier (`exec`, `exec_group`,
`session_send`, `scp_upload`, `scp_download`, `tunnel_create`) never run
inside the tool call (conduit-w3l7, conduit-enf0). The exact operation is
frozen (host or resolved host list, session, command, timeout, paths,
forward target) and an approval prompt goes to the owner in the chat the
turn came from. It stays valid for `security.approval_timeout` (default:
the gateway's 5 minutes; capped at 1 hour):

```
APPROVAL NEEDED [K7M2QX]
Run SSH command on web-1 (deploy@10.0.0.5:22)

Host: web-1 (deploy@10.0.0.5:22)
Command: systemctl restart nginx
Timeout: 30s
Risk: dangerous tier (classified as dangerous command)

Reply "YES K7M2QX" to approve or "NO K7M2QX" to cancel. Expires in 5m0s (14:05:31).
Only you can approve this; the agent cannot.
```

The model gets a "NOT RUN YET, awaiting the owner's approval" result; the
frozen command runs once, only on `YES <code>`, and its output is posted back
to the chat (audit entries carry `approved_by: "human-approval"`). Turns with
no live human (cron, heartbeat, sub-agents, MCP) fail closed, as does a
gateway without an approval channel. Secret-looking values in the command
(`PASSWORD=...`, `--token ...`, URL passwords, `Authorization:` headers) are
masked in the prompt; the real command still runs.

### Classification Flow

1. Check command length against `max_command_length`
2. Check against `blocked_patterns` (regex list)
3. Detect subshells (`$()`, backticks) -- blocked if `allow_subshells` is false
4. Detect pipe chains -- blocked if `allow_pipes` is false
5. For pipes, classify each segment; the worst tier wins
6. Look up the base command in tier lists (read, modify, dangerous, blocked)
7. Check for dangerous arguments (e.g., `rm -rf`, `chmod 777`, `git push --force`)
8. Check against blocked command strings
9. Apply per-host tier cap: if the command tier exceeds the host's `security_tier`, the command is blocked

### Per-Host Tier Enforcement

Each host can set a `security_tier` that caps the maximum command tier allowed. For example, a host with `"security_tier": "read"` will only accept read-tier commands -- any modify, dangerous, or blocked command is rejected regardless of classification.

For group execution, the strictest tier across all hosts in the group is applied.

Non-command operations have fixed tiers and follow the same cap and
approval rules: `scp_upload` and `tunnel_create` are dangerous,
`scp_download` is read. A host that should accept uploads or tunnels needs
`"security_tier": "dangerous"` (or no tier).

### Host Key Verification

Every connection (pool, persistent sessions, jump hosts, and therefore SCP
and tunnels) verifies the server's host key against `pool.known_hosts_file`.
An unknown host fails with the key's SHA256 fingerprint and an `ssh-keyscan`
hint; a changed key fails with `HOST KEY MISMATCH`. There is no insecure
mode: `strict_host_key_checking: "no"` makes every connection fail (and the
tool's SelfTest report `failed`).

### Audit Logging

When audit is enabled, every command execution is logged to a JSONL file with:

- Host, command, security tier, approval status
- Exit code, duration, stdout/stderr (bounded by `max_output_capture`)
- Timestamps and error details
- Secret redaction when `redact_secrets` is enabled

## Persistent Sessions

Sessions maintain a shell process on a remote host across multiple commands. This preserves:

- Environment variables (`export FOO=bar` persists)
- Working directory (`cd /var/log` persists)
- Shell state (aliases, functions set during the session)

### Limits

- Max 5 concurrent sessions (configurable via `max_concurrent_sessions`)
- Sessions auto-close after 10 minutes of idle time (configurable via `session_idle_timeout`)
- Each command in a session still goes through security classification

### Typical Workflow

```
session_start(host="web-prod-1")     -> session_id="abc123"
session_send(session_id="abc123", command="cd /var/log")
session_send(session_id="abc123", command="grep ERROR app.log | tail -20")
session_send(session_id="abc123", command="export CONTEXT=debug")
session_send(session_id="abc123", command="./run-diagnostics.sh")
session_close(session_id="abc123")
```

## Tunnels

Tunnels create local port forwards through SSH connections, useful for accessing remote databases, caches, or internal services.

### Security

- Tunnels bind only to `127.0.0.1` (localhost) -- never exposed to the network
- Local ports must be >= 1024 (no privileged ports)
- Set `local_port` to 0 for auto-assignment

### Typical Workflow

```
tunnel_create(host="db-server", local_port=3307, remote_host="localhost", remote_port=3306)
  -> tunnel_id="xyz-789", local endpoint 127.0.0.1:3307

# Application connects to 127.0.0.1:3307, traffic forwards to db-server:3306

tunnel_list()   -> shows active connections, bytes in/out
tunnel_close(tunnel_id="xyz-789")
```

## Package Layout

```
internal/tools/ssh/
├── tool.go             # SSHTool with action dispatch (16 actions)
├── security.go         # SecurityEngine: classify, approve/block
├── session.go          # SessionManager: persistent shell sessions
├── tunnel.go           # TunnelManager: local port forwarding
├── pool.go             # Connection pool with per-host limits
├── fanout.go           # FanoutExecutor for group commands
├── inventory.go        # Ansible inventory loader (INI, YAML, dynamic)
├── scp.go              # SCP upload/download
├── audit.go            # JSONL audit logger with redaction
└── *_test.go           # Tests

internal/config/
├── remote_ssh.go       # RemoteSSHConfig and all sub-config types
└── remote_ssh_test.go
```
