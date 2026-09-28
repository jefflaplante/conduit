# Conduit-Go Deployment Guide

## Variables

Throughout this guide, replace these placeholders with values for your environment:

| Variable | Description | Example |
|---|---|---|
| `$PROJECT_DIR` | Source code / git checkout directory | `/home/dev/projects/conduit` |
| `$INSTALL_DIR` | Runtime installation directory | `/opt/conduit` |
| `$BUILD_USER` | User that builds and deploys the binary | `deploy` |
| `$SERVICE_USER` | User that the systemd service runs as | `conduit` |

## Standard Deployment Process

### Prerequisites
- Sudoers permissions configured in `/etc/sudoers.d/conduit`:
  ```bash
  # Conduit-Go binary installation (local self-contained approach)
  $BUILD_USER ALL = ($SERVICE_USER) NOPASSWD: /usr/bin/install -m 755 $PROJECT_DIR/conduit $INSTALL_DIR/bin/conduit

  # Service file in-place editing
  $BUILD_USER ALL = (root) NOPASSWD: /usr/bin/tee /etc/systemd/system/conduit.service

  # Systemd operations for conduit service only
  $BUILD_USER ALL = (root) NOPASSWD: /bin/systemctl daemon-reload
  $BUILD_USER ALL = (root) NOPASSWD: /bin/systemctl enable conduit.service
  $BUILD_USER ALL = (root) NOPASSWD: /bin/systemctl disable conduit.service
  $BUILD_USER ALL = (root) NOPASSWD: /bin/systemctl start conduit.service
  $BUILD_USER ALL = (root) NOPASSWD: /bin/systemctl stop conduit.service
  $BUILD_USER ALL = (root) NOPASSWD: /bin/systemctl restart conduit.service
  $BUILD_USER ALL = (root) NOPASSWD: /bin/systemctl status conduit.service
  ```

### Deployment Steps

#### 1. Build the Binary
```bash
cd $PROJECT_DIR
export PATH=$PATH:/usr/local/go/bin:~/go/bin
go build -o conduit ./cmd/gateway
```

#### 2. Install to Service Directory
```bash
sudo -u $SERVICE_USER install -m 755 $PROJECT_DIR/conduit $INSTALL_DIR/bin/conduit
```

#### 3. Restart Service
```bash
sudo systemctl restart conduit.service
```

#### 4. Verify Deployment
```bash
sudo systemctl status conduit.service
```

**Expected output:**
- Active (running)
- ExecStart: `$INSTALL_DIR/bin/conduit server --config $INSTALL_DIR/config.json`
- Telegram bot connected
- Gateway started on configured port

### Service Configuration

**Service File Location:** `/etc/systemd/system/conduit.service`

**Key Configuration:**
- **User/Group:** `$SERVICE_USER`
- **Working Directory:** `$INSTALL_DIR`
- **Binary:** `$INSTALL_DIR/bin/conduit`
- **Config:** `$INSTALL_DIR/config.json`
- **Environment File:** `~/.conduit-secrets.env`
- **Read/Write Paths:** `$INSTALL_DIR/`

### Service File Updates

When updating the service file:

#### 1. Update Template First
Edit `$PROJECT_DIR/deploy/conduit.service` with changes

#### 2. Apply to System
```bash
cat $PROJECT_DIR/deploy/conduit.service | sudo tee /etc/systemd/system/conduit.service > /dev/null
```

#### 3. Reload and Restart
```bash
sudo systemctl daemon-reload
sudo systemctl restart conduit.service
```

### PID File

`conduit stop|restart|status` and `conduit backup restore` find the running gateway through its PID file. The gateway writes it to `--pidfile` if given, else `$RUNTIME_DIRECTORY/conduit.pid`, else `{data_dir}/conduit.pid` (`CONDUIT_DATA_DIR`, then config `data_dir`, then `~/.conduit`). It no longer uses `/tmp/conduit.pid`: with `PrivateTmp=true` that file was invisible to the CLI.

The service templates set `RuntimeDirectory=conduit`, so systemd creates `/run/conduit` (owned by the service user, removed on stop) and the gateway writes `/run/conduit/conduit.pid`. The CLI runs outside the unit and has no `$RUNTIME_DIRECTORY`, so without `--pidfile` it searches `/run/conduit/conduit.pid`, `{data_dir}/conduit.pid`, then the legacy `/tmp/conduit.pid`, and acts on the first one naming a live process.

For an existing unit, add a drop-in instead of replacing the file:
```bash
sudo mkdir -p /etc/systemd/system/conduit.service.d
printf '[Service]\nRuntimeDirectory=conduit\nRuntimeDirectoryMode=0755\n' | \
  sudo tee /etc/systemd/system/conduit.service.d/runtime-dir.conf
sudo systemctl daemon-reload && sudo systemctl restart conduit.service
```

### Automation Commands

#### Full Deploy (Code + Restart)
```bash
cd $PROJECT_DIR && \
export PATH=$PATH:/usr/local/go/bin:~/go/bin && \
go build -o conduit ./cmd/gateway && \
sudo -u $SERVICE_USER install -m 755 conduit $INSTALL_DIR/bin/conduit && \
sudo systemctl restart conduit.service && \
sudo systemctl status conduit.service
```

#### Deploy with Git Commit
```bash
cd $PROJECT_DIR && \
git add . && \
git commit -m "Deploy: $(date '+%Y-%m-%d %H:%M:%S')" && \
export PATH=$PATH:/usr/local/go/bin:~/go/bin && \
go build -o conduit ./cmd/gateway && \
sudo -u $SERVICE_USER install -m 755 conduit $INSTALL_DIR/bin/conduit && \
sudo systemctl restart conduit.service
```

### Troubleshooting

#### Service Won't Start
```bash
# Check service status
sudo systemctl status conduit.service

# Check logs
sudo journalctl -u conduit.service -n 50

# Check port conflicts
netstat -tulpn | grep :18789
```

#### Permission Issues
```bash
# Verify binary permissions
ls -la $INSTALL_DIR/bin/conduit

# Should show: -rwxr-xr-x 1 $SERVICE_USER $SERVICE_USER
```

#### Build Issues
```bash
# Verify Go installation
export PATH=$PATH:/usr/local/go/bin:~/go/bin
go version

# Clean build
cd $PROJECT_DIR
go clean
go build -o conduit ./cmd/gateway
```

### Architecture

**Self-Contained Deployment:**
```
$INSTALL_DIR/
├── bin/
│   └── conduit              # Binary (deployed here)
├── config.json              # Configuration
├── .conduit-secrets.env     # Environment variables
├── workspace/               # Agent workspace
└── logs/                    # Application logs
```

**Development:**
```
$PROJECT_DIR/
├── cmd/gateway/             # Source code
├── internal/                # Source code
├── deploy/conduit.service   # Service template
├── conduit                  # Built binary (temporary)
└── DEPLOYMENT.md            # This guide
```

### Security

- Service runs as a dedicated service user (not root)
- Limited sudo permissions for deployment only
- Self-contained in `$INSTALL_DIR/` directory
- No system-wide file pollution
- Systemd security hardening enabled

## Container

The `Containerfile` builds a static binary into an Alpine image that runs as uid/gid 1000 (`conduit`). `.github/workflows/container.yml` publishes multi-arch (amd64/arm64) images to `ghcr.io/jefflaplante/conduit`:

| Tag | Build tags |
|---|---|
| `latest` | core |
| `full` | datadog, k8s, pagerduty, sre, mqtt, ssh, unifi |
| `sre` | datadog, pagerduty, sre |
| `iot` | mqtt, unifi |

Before pushing, CI runs each variant with a throwaway env and requires `/health` to return 200, the DB and token secret to be created on `/data`, and `conduit status` to find the process.

### Image Layout

| Path | Purpose |
|---|---|
| `/usr/local/bin/conduit` | Binary (entrypoint) |
| `/etc/conduit/config.json` | Default config from `configs/container/conduit.json` (read-only) |
| `/data` (volume) | `data_dir` (`CONDUIT_DATA_DIR=/data`) and working directory: `gateway.db` with its search/brain DBs, `auth/token_secret`, `auth/mcp_token`, `.env`, `conduit.pid` |
| `/workspace` (volume) | Agent workspace and tool sandbox root |

The default config listens on 18789 (the port the image EXPOSEs and health-checks), reads `${ANTHROPIC_API_KEY}`, and enables file, search and web-fetch tools but not `Bash`. Only the braced `${VAR}` form is expanded (see `reference/guides/ENV_AND_SECRETS.md`). Secrets can also go in `/data/.env`.

### Run

```bash
podman run -d --name conduit \
  -p 127.0.0.1:18789:18789 \
  -e ANTHROPIC_API_KEY \
  -v conduit-data:/data -v conduit-workspace:/workspace \
  ghcr.io/jefflaplante/conduit:latest

curl -fsS http://localhost:18789/health
podman exec conduit conduit status
podman exec conduit conduit --config /etc/conduit/config.json token create --client-name my-client
```

Pass `--config /etc/conduit/config.json` to CLI commands run with `exec` that read the config (`token`, `pairing`, `backup`); the working directory is `/data`, and the default `--config config.json` would otherwise create a fresh default config there.

Named volumes inherit the image's ownership on first use. For a bind mount, the host directory must be writable by uid 1000: `chown 1000:1000 ./data`, or with rootless podman use `-v ./data:/data:U`.

To use your own config, mount it over the default and keep `"port": 18789`, `data_dir`/`database.path` under `/data`, and the workspace under `/workspace`:

```bash
-v ./config.json:/etc/conduit/config.json:ro
```

If you change the port, also change the published port and override the health check (`--health-cmd`, or `healthcheck:` in compose).

### Compose

`deploy/compose.yaml` runs the image with both named volumes, an env file, the health check, a 30s stop grace period (the gateway drains in-flight turns on SIGTERM for up to 27s), and dropped capabilities:

```bash
cp deploy/conduit.env.example deploy/conduit.env   # add ANTHROPIC_API_KEY
chmod 600 deploy/conduit.env
docker compose -f deploy/compose.yaml up -d        # or podman-compose
docker compose -f deploy/compose.yaml ps           # (healthy)
docker compose -f deploy/compose.yaml logs -f
```

`deploy/conduit.env` is gitignored by the `*.env` rule. `docker compose ... build` builds from the checkout instead of pulling.

### Backup

Everything stateful is on the `/data` volume. Stop the container before a restore: `conduit backup restore` refuses to run while the pidfile names a live process or the port answers.

```bash
docker compose -f deploy/compose.yaml stop
docker run --rm -v conduit_conduit-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/conduit-data.tgz -C /data .
docker compose -f deploy/compose.yaml start
```

Kubernetes/Helm is not provided; the reference host runs systemd.

---

**Last Updated:** 2026-09-28  
**Service Version:** Conduit-Go 1.0.0  
**Target Environment:** Linux server with systemd