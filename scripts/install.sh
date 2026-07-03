#!/bin/sh
# Phantom VPN server installer/uninstaller.
#
# Install:
#   curl -fsSL https://raw.githubusercontent.com/klion-gh/phantom/main/scripts/install.sh | sh
#
# Uninstall:
#   curl -fsSL https://raw.githubusercontent.com/klion-gh/phantom/main/scripts/install.sh | sh -s -- uninstall
#
# Config (env vars, all optional):
#   PHANTOM_DOMAIN   domain that already points at this server's IP (prompted if unset and a TTY is available)
#   PHANTOM_REPO     override the GitHub "owner/repo" release source (default baked in below)
#   PHANTOM_PURGE=1  on uninstall, also remove /var/lib/phantom (ACME cert cache) without prompting
#
# POSIX sh only - this is executed via `sh`, not necessarily bash.

set -e

REPO="${PHANTOM_REPO:-klion-gh/phantom}"
INSTALL_DIR=/opt/phantom
DATA_DIR=/var/lib/phantom
SERVICE_FILE=/etc/systemd/system/phantom.service
SERVICE_NAME=phantom

log()  { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$1" >&2; exit 1; }

require_root() {
    [ "$(id -u)" = "0" ] || die "must be run as root (try: curl ... | sudo sh)"
}

# Sets the global ARCH var directly rather than echoing it back through a
# `$(...)` command substitution - substitutions run in a subshell, so a
# `die` (exit 1) on an unsupported architecture would only terminate that
# subshell and silently leave ARCH empty instead of actually stopping the
# script.
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) die "unsupported architecture: $(uname -m)" ;;
    esac
}

# Reads a line from the real controlling terminal even when this script's own
# stdin is occupied by `curl | sh` piping the script itself - falls back to
# printing the env-var instruction if no TTY is reachable (e.g. fully
# unattended automation), rather than hanging forever on a `read` that can
# never succeed.
prompt() {
    prompt_text="$1"
    if [ -t 0 ] || [ -r /dev/tty ]; then
        printf '%s' "$prompt_text" > /dev/tty
        read -r reply < /dev/tty
        echo "$reply"
    else
        echo ""
    fi
}

do_uninstall() {
    log "Stopping and removing the $SERVICE_NAME service..."
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload

    rm -rf "$INSTALL_DIR"
    log "Removed $INSTALL_DIR (binary, server.yaml, server's private key and PSK)."

    if [ "$PHANTOM_PURGE" = "1" ]; then
        purge=y
    else
        purge=$(prompt "Also remove $DATA_DIR (ACME certificate cache)? [y/N] ")
    fi
    case "$purge" in
        y|Y|yes|YES) rm -rf "$DATA_DIR"; log "Removed $DATA_DIR." ;;
        *) log "Kept $DATA_DIR (re-installing with the same domain will reuse the cached certificate)." ;;
    esac

    log "Uninstall complete."
    exit 0
}

if [ "$1" = "uninstall" ] || [ "$1" = "remove" ]; then
    require_root
    do_uninstall
fi

# ---------------------------------------------------------------------------
# Install / upgrade
# ---------------------------------------------------------------------------

require_root
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v systemctl >/dev/null 2>&1 || die "systemd is required (systemctl not found)"

detect_arch
BASE_URL="https://github.com/$REPO/releases/latest/download"

mkdir -p "$INSTALL_DIR" "$DATA_DIR"

UPGRADE=0
if [ -f "$INSTALL_DIR/server.yaml" ]; then
    UPGRADE=1
    log "Existing installation found at $INSTALL_DIR - upgrading binary, keeping config and keys."
fi

log "Downloading phantom-server ($ARCH)..."
curl -fsSL "$BASE_URL/phantom-server-linux-$ARCH" -o "$INSTALL_DIR/phantom-server.new"
chmod 755 "$INSTALL_DIR/phantom-server.new"
mv "$INSTALL_DIR/phantom-server.new" "$INSTALL_DIR/phantom-server"

if [ "$UPGRADE" = "1" ]; then
    log "Restarting service with the new binary..."
    systemctl restart "$SERVICE_NAME"
    systemctl status "$SERVICE_NAME" --no-pager || true
    exit 0
fi

# --- Fresh install from here on ---

DOMAIN="${PHANTOM_DOMAIN:-}"
if [ -z "$DOMAIN" ]; then
    DOMAIN=$(prompt "Domain (must already have an A/AAAA record pointing at this server): ")
fi
[ -n "$DOMAIN" ] || die "no domain given - set PHANTOM_DOMAIN=yourdomain.example and re-run, or run this interactively over SSH"

log "Downloading phantom-keygen ($ARCH) to generate server keys..."
curl -fsSL "$BASE_URL/phantom-keygen-linux-$ARCH" -o /tmp/phantom-keygen
chmod 755 /tmp/phantom-keygen

KEYGEN_OUT=$(/tmp/phantom-keygen)
rm -f /tmp/phantom-keygen

# The three key/PSK lines are printf-aligned with a variable run of spaces
# after the colon (e.g. "Server Public Key:  <hex>" - two spaces - vs
# "Server Private Key: <hex>" - one space), so splitting on ": " left a stray
# leading space on the shorter labels' values. Extracting the hex run itself
# instead of relying on a fixed separator sidesteps that entirely.
SERVER_PRIV=$(echo "$KEYGEN_OUT" | grep '^Server Private Key:' | grep -oE '[0-9a-f]{64}')
SERVER_PUB=$(echo "$KEYGEN_OUT" | grep '^Server Public Key:' | grep -oE '[0-9a-f]{64}')
PSK=$(echo "$KEYGEN_OUT" | grep '^PSK:' | grep -oE '[0-9a-f]{64}')

[ -n "$SERVER_PRIV" ] && [ -n "$SERVER_PUB" ] && [ -n "$PSK" ] || die "failed to parse keygen output"

cat > "$INSTALL_DIR/server.yaml" <<EOF
listen: ":8443"
domain: "$DOMAIN"
acme_email: ""
acme_cache_dir: "$DATA_DIR/acme"
private_key: "$SERVER_PRIV"
psk: "$PSK"
decoy_site_dir: ""
log_level: "info"
EOF
log "Wrote $INSTALL_DIR/server.yaml"

if command -v ufw >/dev/null 2>&1; then
    ufw allow 80/tcp  >/dev/null 2>&1 || true
    ufw allow 8443/tcp >/dev/null 2>&1 || true
fi

cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Phantom VPN Server
After=network.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/phantom-server -config $INSTALL_DIR/server.yaml
WorkingDirectory=$INSTALL_DIR
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1
systemctl restart "$SERVICE_NAME"

sleep 2
if ! systemctl is-active --quiet "$SERVICE_NAME"; then
    warn "Service did not start - check: journalctl -u $SERVICE_NAME -n 50"
    exit 1
fi

CLIENT_YAML="server: \"$DOMAIN:8443\"
domain: \"$DOMAIN\"
fingerprint: \"chrome120\"
psk: \"$PSK\"
server_public_key: \"$SERVER_PUB\"
listen: \"127.0.0.1:1080\"
listen_http: \"127.0.0.1:1081\"
pool_size: 4
log_level: \"info\""

echo "$CLIENT_YAML" > "$INSTALL_DIR/client.yaml.example"

log "Installed and running. Certificate issuance happens lazily on the first real connection - watch:"
echo "    journalctl -u $SERVICE_NAME -f"
echo ""
log "client.yaml (paste this into the desktop client or the Android app, also saved at $INSTALL_DIR/client.yaml.example):"
echo ""
echo "$CLIENT_YAML"
