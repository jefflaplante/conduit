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
netstat -tulpn | grep :18890
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

---

**Last Updated:** 2026-02-12  
**Service Version:** Conduit-Go 1.0.0  
**Target Environment:** Linux server with systemd