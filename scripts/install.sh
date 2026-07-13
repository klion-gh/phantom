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
#   PHANTOM_DOMAIN    domain that already points at this server's IP (prompted if unset and a TTY is available)
#   PHANTOM_REPO      override the GitHub "owner/repo" release source (default baked in below)
#   PHANTOM_PURGE=1   on uninstall, also remove /var/lib/phantom (ACME cert cache) without prompting
#   PHANTOM_CERT_FILE
#   PHANTOM_KEY_FILE  already have a certificate for this domain (e.g. from certbot, on a box
#                     where something else already owns port 80)? Set both to use it directly
#                     instead of Phantom's own ACME - prompted if unset and a TTY is available.
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

# Certificate: by default Phantom gets its own via ACME (needs port 80 free for the
# HTTP-01 challenge) - but on a box that's already running its own web server on
# 80/443 for something else, that challenge can't complete. If there's already a
# certificate for this domain from somewhere else (typically certbot, serving that
# other web server), point Phantom at it directly instead - see PROTOCOL.md §6.3.
CERT_FILE="${PHANTOM_CERT_FILE:-}"
KEY_FILE="${PHANTOM_KEY_FILE:-}"
if [ -z "$CERT_FILE" ] && [ -z "$KEY_FILE" ]; then
    use_static=$(prompt "Already have a certificate for $DOMAIN (e.g. via certbot)? Use it instead of ACME? [y/N] ")
    case "$use_static" in
        y|Y|yes|YES)
            CERT_FILE=$(prompt "Path to certificate/fullchain file: ")
            KEY_FILE=$(prompt "Path to private key file: ")
            ;;
    esac
fi
if [ -n "$CERT_FILE" ] || [ -n "$KEY_FILE" ]; then
    [ -n "$CERT_FILE" ] && [ -n "$KEY_FILE" ] || die "both PHANTOM_CERT_FILE and PHANTOM_KEY_FILE are needed to skip ACME - leave both unset to use ACME instead"
    [ -f "$CERT_FILE" ] || die "cert file not found: $CERT_FILE"
    [ -f "$KEY_FILE" ] || die "key file not found: $KEY_FILE"
    log "Using existing certificate $CERT_FILE - Phantom's own ACME/port 80 responder will stay off."
fi

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

{
    echo "listen: \":8443\""
    echo "domain: \"$DOMAIN\""
    echo "acme_email: \"\""
    echo "acme_cache_dir: \"$DATA_DIR/acme\""
    if [ -n "$CERT_FILE" ]; then
        echo "cert_file: \"$CERT_FILE\""
        echo "key_file: \"$KEY_FILE\""
    fi
    echo "private_key: \"$SERVER_PRIV\""
    echo "psk: \"$PSK\""
    echo "decoy_site_dir: \"\""
    echo "log_level: \"info\""
} > "$INSTALL_DIR/server.yaml"
log "Wrote $INSTALL_DIR/server.yaml"

if command -v ufw >/dev/null 2>&1; then
    # Port 80 is only needed for Phantom's own ACME challenge - skip opening it
    # when using an existing certificate instead, where it's likely already
    # fielding traffic for whatever else issued that certificate.
    [ -z "$CERT_FILE" ] && { ufw allow 80/tcp >/dev/null 2>&1 || true; }
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
fingerprint: \"chrome133\"
psk: \"$PSK\"
server_public_key: \"$SERVER_PUB\"
listen: \"127.0.0.1:1080\"
listen_http: \"127.0.0.1:1081\"
pool_size: 4
log_level: \"info\""

echo "$CLIENT_YAML" > "$INSTALL_DIR/client.yaml.example"

if [ -n "$CERT_FILE" ]; then
    log "Installed and running with the existing certificate at $CERT_FILE. Watch:"
else
    log "Installed and running. Certificate issuance happens lazily on the first real connection - watch:"
fi
echo "    journalctl -u $SERVICE_NAME -f"
echo ""
log "client.yaml (also saved at $INSTALL_DIR/client.yaml.example) - paste this whole block into:"
echo "    - the Windows app (phantom.exe, from https://github.com/$REPO/releases/latest) - the + button"
echo "    - the Android app (phantom.apk, same releases page) - the + button"
echo "    - or cmd/client -config client.yaml for the plain SOCKS5/HTTP proxy client"
echo ""
echo "$CLIENT_YAML"
