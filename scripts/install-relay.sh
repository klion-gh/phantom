#!/bin/sh
# Phantom relay installer/uninstaller.
#
# A relay is an entry point in front of Phantom servers: clients connect to it,
# it reads the domain from their TLS ClientHello and passes the connection on
# to that domain's server untouched. It holds no keys and decrypts nothing.
# Put one where your clients' network still reaches it - typically inside the
# same country - and turn "use a relay" on in the apps with its address.
#
# Install:
#   curl -fsSL https://raw.githubusercontent.com/klion-gh/phantom/main/scripts/install-relay.sh | sh
#
# Uninstall:
#   curl -fsSL https://raw.githubusercontent.com/klion-gh/phantom/main/scripts/install-relay.sh | sh -s -- uninstall
#
# Config (env vars, all optional):
#   PHANTOM_RELAY_PORTS  ports the relay accepts clients on (default "443 8443")
#   PHANTOM_RELAY_ALLOW  space-separated domains it forwards to ("vpn.example *.example.org");
#                        default: any domain, on port 8443 of whatever it resolves to
#   PHANTOM_REPO         override the GitHub "owner/repo" release source
#   PHANTOM_VERSION      install this release tag (e.g. v2.5.0-beta.1) instead of the latest stable
#   PHANTOM_NO_TUNE=1    leave the kernel's TCP settings alone
#
# Edit /opt/phantom-relay/relay.yaml afterwards for anything else (routes for
# servers on another port); an upgrade keeps it.
#
# POSIX sh only - this is executed via `sh`, not necessarily bash.

set -e

REPO="${PHANTOM_REPO:-klion-gh/phantom}"
INSTALL_DIR=/opt/phantom-relay
SERVICE_FILE=/etc/systemd/system/phantom-relay.service
SERVICE_NAME=phantom-relay
SYSCTL_FILE=/etc/sysctl.d/99-phantom-relay.conf

log()  { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$1" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "must be run as root (try: curl ... | sudo sh)"

# The relay terminates the client's TCP and opens its own to the server, so
# both legs get this machine's congestion control - BBR for the same reasons
# as on the server (see install.sh).
tune_network() {
    [ "${PHANTOM_NO_TUNE:-}" = "1" ] && return 0
    modprobe tcp_bbr 2>/dev/null || true
    if ! grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control 2>/dev/null; then
        warn "this kernel has no BBR - leaving TCP settings as they are"
        return 0
    fi
    cat > "$SYSCTL_FILE" <<'SYSCTL'
# Written by the Phantom relay installer, removed by its uninstall.
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.ipv4.tcp_rmem = 4096 131072 16777216
net.ipv4.tcp_wmem = 4096 65536 16777216
net.ipv4.tcp_slow_start_after_idle = 0
SYSCTL
    if sysctl -q -p "$SYSCTL_FILE" >/dev/null 2>&1; then
        log "TCP tuned: BBR, fq, 16 MB socket buffers ($SYSCTL_FILE)."
    else
        warn "could not apply $SYSCTL_FILE now - it takes effect after a reboot"
    fi
}

if [ "$1" = "uninstall" ] || [ "$1" = "remove" ]; then
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload
    rm -rf "$INSTALL_DIR"
    if [ -f "$SYSCTL_FILE" ]; then
        rm -f "$SYSCTL_FILE"
        # A Phantom server on the same machine keeps its own copy of these.
        if [ ! -f /etc/sysctl.d/99-phantom.conf ]; then
            sysctl -q -w net.ipv4.tcp_congestion_control=cubic >/dev/null 2>&1 || true
            sysctl -q -w net.core.default_qdisc=fq_codel >/dev/null 2>&1 || true
        fi
    fi
    log "Relay removed."
    exit 0
fi

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v systemctl >/dev/null 2>&1 || die "systemd is required (systemctl not found)"

case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture: $(uname -m)" ;;
esac

if [ -n "${PHANTOM_VERSION:-}" ]; then
    case "$PHANTOM_VERSION" in
        v*) ;;
        *) die "PHANTOM_VERSION is a release tag like v2.5.0-beta.1, got '$PHANTOM_VERSION'" ;;
    esac
    BASE_URL="https://github.com/$REPO/releases/download/$PHANTOM_VERSION"
    log "Installing release $PHANTOM_VERSION."
else
    BASE_URL="https://github.com/$REPO/releases/latest/download"
fi

mkdir -p "$INSTALL_DIR"
UPGRADE=0
[ -f "$INSTALL_DIR/relay.yaml" ] && UPGRADE=1

log "Downloading phantom-relay ($ARCH)..."
curl -fsSL "$BASE_URL/phantom-relay-linux-$ARCH" -o "$INSTALL_DIR/phantom-relay.new" \
    || die "could not download phantom-relay from $BASE_URL (releases before 2.5 have no relay)"
if command -v sha256sum >/dev/null 2>&1; then
    want=$(curl -fsSL "$BASE_URL/SHA256SUMS" 2>/dev/null | awk -v n="phantom-relay-linux-$ARCH" '$2 == n || $2 == "*" n {print $1}' | head -1)
    got=$(sha256sum "$INSTALL_DIR/phantom-relay.new" | awk '{print $1}')
    if [ -z "$want" ]; then
        warn "phantom-relay-linux-$ARCH not listed in SHA256SUMS - skipping integrity check"
    elif [ "$got" != "$want" ]; then
        rm -f "$INSTALL_DIR/phantom-relay.new"
        die "checksum mismatch for phantom-relay-linux-$ARCH (expected $want, got $got) - refusing to install"
    else
        log "Checksum verified for phantom-relay-linux-$ARCH."
    fi
fi
chmod 755 "$INSTALL_DIR/phantom-relay.new"
mv "$INSTALL_DIR/phantom-relay.new" "$INSTALL_DIR/phantom-relay"

tune_network

if [ "$UPGRADE" = "1" ]; then
    log "Existing relay.yaml kept. Restarting with the new binary..."
    systemctl restart "$SERVICE_NAME"
    systemctl status "$SERVICE_NAME" --no-pager || true
    exit 0
fi

PORTS="${PHANTOM_RELAY_PORTS:-443 8443}"
listen=""
for p in $PORTS; do
    case "$p" in
        ''|*[!0-9]*) die "PHANTOM_RELAY_PORTS must be port numbers, got '$p'" ;;
    esac
    if command -v ss >/dev/null 2>&1 && ss -ltnH "sport = :$p" 2>/dev/null | grep -q .; then
        warn "port $p is already in use - the relay will not listen on it"
        continue
    fi
    listen="$listen\"0.0.0.0:$p\", "
    first_port="${first_port:-$p}"
done
[ -n "$listen" ] || die "none of the ports ($PORTS) is free"

allow=""
for d in ${PHANTOM_RELAY_ALLOW:-}; do
    allow="$allow\"$d\", "
done

umask 077
{
    echo "# Phantom relay. Clients connect here; each connection goes to port"
    echo "# target_port of the domain in its TLS ClientHello."
    echo "listen: [${listen%, }]"
    echo "target_port: 8443"
    echo "# Empty: any domain that resolves to a public address."
    echo "allow: [${allow%, }]"
    echo "# A server on another port, or whose domain points elsewhere:"
    echo "# routes:"
    echo "#   vpn.example: \"203.0.113.10:443\""
    echo "log_level: info"
} > "$INSTALL_DIR/relay.yaml"
log "Wrote $INSTALL_DIR/relay.yaml"

cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Phantom relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$INSTALL_DIR/phantom-relay -config $INSTALL_DIR/relay.yaml
Restart=always
RestartSec=2
DynamicUser=yes
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
chmod 644 "$INSTALL_DIR/relay.yaml"
systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME" >/dev/null 2>&1
systemctl status "$SERVICE_NAME" --no-pager || true

IP=$(curl -4 -fsS --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')
log "Relay is running. In the Phantom app turn on \"use a relay\" and enter:"
echo ""
echo "    $IP:$first_port"
echo ""
