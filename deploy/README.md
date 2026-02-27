# Conduit-Go Deployment

## Quick Start

### 1. Build the binary

```bash
cd $PROJECT_DIR
mkdir -p bin
/usr/local/go/bin/go build -o bin/conduit ./cmd/gateway
```

### 2. Install the systemd service

```bash
# Copy service file (requires sudo)
sudo cp deploy/conduit.service /etc/systemd/system/

# Reload systemd
sudo systemctl daemon-reload

# Enable on boot
sudo systemctl enable conduit

# Start it
sudo systemctl start conduit
```

### 3. Check status

```bash
sudo systemctl status conduit
journalctl -u conduit -f  # Follow logs
```

## Configuration

The service expects:
- **Binary:** `$PROJECT_DIR/bin/conduit`
- **Config:** `$PROJECT_DIR/config.json`
- **Secrets:** `~/.conduit-secrets.env` (optional, for API keys)

Edit `config.json` before starting. The service runs as a dedicated service user.

## Management Commands

```bash
sudo systemctl start conduit    # Start
sudo systemctl stop conduit     # Stop (graceful)
sudo systemctl restart conduit  # Restart
sudo systemctl status conduit   # Status
journalctl -u conduit -n 100    # Last 100 log lines
journalctl -u conduit -f        # Follow logs live
```

## Updating

```bash
cd $PROJECT_DIR
git pull
/usr/local/go/bin/go build -o bin/conduit ./cmd/gateway
sudo systemctl restart conduit
```

## Security Notes

The service uses systemd security hardening:
- `NoNewPrivileges=true` - Can't gain privileges
- `ProtectSystem=strict` - Filesystem mostly read-only
- `ProtectHome=read-only` - Home dirs read-only except explicit paths
- `PrivateTmp=true` - Private /tmp namespace

Write access is limited to:
- `$PROJECT_DIR` (database, sessions)
- `./workspace` (workspace context files)
