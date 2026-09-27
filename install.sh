#!/usr/bin/env bash
# Build and deploy Conduit to the live instance, then restart it gracefully.
#
#   make install            # build + deploy (Makefile runs `make build` first)
#   ./install.sh --dry-run  # show what would happen
#   ./install.sh --rollback # reinstall the previous binary and restart
#
# Restart is SIGHUP to the service's MainPID: the gateway drains in-flight
# turns and exits 0, and systemd (Restart=always) relaunches the new binary.
# No sudo needed because the service runs as this user. No HTTP restart.
#
# Override with env: CONDUIT_HOME (default /home/jules/ocgo), SERVICE (conduit).
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONDUIT_HOME="${CONDUIT_HOME:-/home/jules/ocgo}"
SERVICE="${SERVICE:-conduit}"
BIN="$CONDUIT_HOME/bin/conduit"
CONFIG="$CONDUIT_HOME/config.json"
NEW="$REPO/bin/conduit"

DRY=0; ROLLBACK=0
for arg in "$@"; do
  case "$arg" in
    --dry-run)  DRY=1 ;;
    --rollback) ROLLBACK=1 ;;
    -h|--help)  sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

say()  { printf '==> %s\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
run()  { if ((DRY)); then printf '    [dry-run] %s\n' "$*"; else "$@"; fi; }

port() { jq -r '.port // 18789' "$CONFIG" 2>/dev/null || echo 18789; }
main_pid() { systemctl show -p MainPID --value "$SERVICE"; }

restart_and_verify() {
  local old_pid new_pid p i
  old_pid="$(main_pid)"
  if [[ "$old_pid" -gt 0 ]]; then
    say "Graceful restart: SIGHUP to PID $old_pid (drains in-flight turns, up to ~30s)"
    run kill -HUP "$old_pid"
  else
    say "Service not running; starting it (needs sudo)"
    run sudo systemctl start "$SERVICE"
  fi
  ((DRY)) && return 0

  say "Waiting for the new process..."
  for ((i = 0; i < 90; i++)); do
    new_pid="$(main_pid)"
    [[ "$new_pid" -gt 0 && "$new_pid" != "$old_pid" ]] && break
    sleep 1
  done
  [[ "$new_pid" -gt 0 && "$new_pid" != "$old_pid" ]] \
    || die "service did not come back (see: journalctl -u $SERVICE -n 50)"

  p="$(port)"
  for ((i = 0; i < 30; i++)); do
    if curl -fsS -o /dev/null "http://localhost:$p/health"; then
      say "Healthy: PID $new_pid, $("$BIN" version 2>/dev/null | head -1)"
      return 0
    fi
    sleep 1
  done
  die "PID $new_pid is up but /health on :$p is failing (journalctl -u $SERVICE -n 50; rollback: $0 --rollback)"
}

[[ -f "$CONFIG" ]] || die "config not found: $CONFIG"

if ((ROLLBACK)); then
  [[ -x "$BIN.prev" ]] || die "no previous binary at $BIN.prev"
  say "Rolling back to $BIN.prev"
  run install -m 0755 "$BIN.prev" "$BIN.new"
  run mv -f "$BIN.new" "$BIN"
  restart_and_verify
  exit 0
fi

# Build if invoked directly (make install has already built).
if [[ ! -x "$NEW" || -n "$(find "$REPO" -name '*.go' -newer "$NEW" -print -quit)" ]]; then
  say "Building"
  run make -C "$REPO" build
fi
((DRY)) || "$NEW" version >/dev/null || die "new binary failed to run: $NEW"

say "Installing $NEW -> $BIN"
run mkdir -p "$(dirname "$BIN")"
[[ -x "$BIN" ]] && run install -m 0755 "$BIN" "$BIN.prev"      # keep one for rollback
run install -m 0755 "$NEW" "$BIN.new"                            # then atomic swap: a
run mv -f "$BIN.new" "$BIN"                                      # running exe can't be overwritten

restart_and_verify

if [[ "$(systemctl show -p UMask --value "$SERVICE")" != "0077" ]]; then
  say "Note: $SERVICE UMask is not 0077; new files the gateway creates will be world-readable."
  echo "    Fix once: sudo mkdir -p /etc/systemd/system/$SERVICE.service.d && printf '[Service]\\nUMask=0077\\n' | sudo tee /etc/systemd/system/$SERVICE.service.d/umask.conf && sudo systemctl daemon-reload"
fi
