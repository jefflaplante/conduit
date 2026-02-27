# Terminal UI & SSH Access

Conduit Go includes a built-in terminal chat client (TUI) and SSH server for remote access.

## Terminal UI (TUI)

The TUI is a full-featured terminal chat client powered by [BubbleTea](https://github.com/charmbracelet/bubbletea). It connects to the running gateway over WebSocket.

### Quick Start

```bash
# 1. Start the gateway (in one terminal)
./bin/gateway server

# 2. Create an auth token
./bin/gateway token create --client-name "tui"
# Save the token output

# 3. Launch the TUI (in another terminal)
./bin/gateway tui --token "claw_v1_..."
```

On subsequent runs, the token is saved to `~/.conduit/tui.json` and reused automatically:

```bash
./bin/gateway tui
```

### Key Bindings

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+T` | Open new session tab |
| `Ctrl+W` | Close current tab |
| `Ctrl+Left/Right` | Switch between tabs |
| `Tab` | Toggle sidebar |
| `Shift+Tab` | Cycle sidebar panels (Session / Tools / Status) |
| `PageUp/PageDown` | Scroll chat history |
| `Ctrl+C` | Quit |

### Slash Commands

All commands work the same as in Telegram:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands and key bindings |
| `/reset` | Clear conversation history for the current session |
| `/status` | Show session info (key, message count, model) |
| `/model` | View current model and available aliases |
| `/model <alias>` | Switch model (haiku, sonnet, opus, opus46, default) |
| `/stop` | Cancel the current AI operation |
| `/quit` | Exit the TUI |

### TUI Features

- **Streaming responses** - Token-by-token streaming with inline tool activity indicators
- **Multi-session tabs** - Multiple conversations in tabs
- **Shared sessions** - Continue conversations started on Telegram or SSH
- **Sidebar panels** - Session details, tool activity, and connection status
- **Session origin badges** - Shows `[TG]`, `[TUI]`, `[SSH]` for each session

### TUI Flags

```bash
./bin/gateway tui [flags]

Flags:
  --url string     Gateway WebSocket URL (default "ws://localhost:18789/ws")
  --token string   Authentication token (saved to ~/.conduit/tui.json)
```

## SSH Access

The gateway can serve the TUI over SSH using [Wish](https://github.com/charmbracelet/wish), allowing you to chat with the AI from any machine with an SSH client.

### Setup

```bash
# 1. Initialize SSH key infrastructure
./bin/gateway ssh-keys init

# 2. Add your SSH public key
./bin/gateway ssh-keys add ~/.ssh/id_ed25519.pub

# 3. Create a gateway token for the SSH server
./bin/gateway token create --client-name "ssh-server"
```

### Running the SSH Server

**Standalone** (separate process from the gateway):

```bash
./bin/gateway ssh-server \
  --listen ":2222" \
  --gateway-token "claw_v1_..."
```

**Integrated** (starts with the gateway):

Add to your config JSON:

```json
{
  "ssh": {
    "enabled": true,
    "listen_addr": ":2222",
    "host_key_path": "~/.conduit/ssh_host_key",
    "authorized_keys_path": "~/.conduit/authorized_keys"
  }
}
```

Then start the gateway normally -- the SSH server starts alongside it.

### Authentication Note

The SSH server connects to the gateway over WebSocket and must authenticate like any other client. If you omit `--gateway-token`, it will look for a saved token in `~/.conduit/tui.json`. Without a token, every SSH session will fail to connect with a "bad handshake" error.

### Connecting

```bash
ssh -p 2222 yourname@localhost
```

The SSH username becomes your user identity for session scoping. You get the same TUI with all the same key bindings and slash commands.

### Managing SSH Keys

```bash
# List authorized keys with fingerprints
./bin/gateway ssh-keys list

# Add a key from a file
./bin/gateway ssh-keys add ~/.ssh/id_ed25519.pub

# Add a key inline
./bin/gateway ssh-keys add "ssh-ed25519 AAAA... user@host"

# Remove a key by fingerprint
./bin/gateway ssh-keys remove "SHA256:abc123..."
```

### SSH Server Flags

```bash
./bin/gateway ssh-server [flags]

Flags:
  --listen string            SSH listen address (default ":2222")
  --host-key string          Path to SSH host key (auto-generated on first run)
  --authorized-keys string   Path to authorized_keys file
  --gateway-url string       Gateway WebSocket URL (default: derived from --config port)
  --gateway-token string     Gateway authentication token (falls back to ~/.conduit/tui.json)
```

## Shared Sessions

Sessions are shared across all clients:

- A conversation started on **Telegram** appears in the TUI session list
- You can **switch to it** and continue from where you left off
- The TUI shows the **origin** of each session (`[TG]`, `[TUI]`, `[SSH]`)
- **Cross-device continuity** - Start on your phone, continue on your terminal

This enables seamless multi-device workflows where you can interact with the same AI context from wherever is most convenient.
