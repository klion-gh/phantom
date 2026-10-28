#!/bin/bash
# End-to-end test of phantomd on a real Linux kernel, run as root by the
# release workflow: a Phantom server on this machine, phantomd connecting to
# it, and checks that traffic and DNS really go through the tunnel and that
# disconnecting restores the network exactly.
#
#   sudo packaging/linux/e2e.sh <dir with phantom-server, phantom-keygen, phantomd>
set -euo pipefail

work=$(mktemp -d)
chmod 755 "$work"
# Copied out of the checkout: the runner's home directory isn't traversable
# by nobody, who runs the server and the unprivileged client below.
install -d -m 755 "$work/bin"
install -m 755 "$1/phantom-server" "$1/phantom-keygen" "$1/phantomd" "$work/bin/"
bin="$work/bin"
cd "$work"
server_pid=""
daemon_pid=""

cleanup() {
	set +e
	[ -n "$daemon_pid" ] && kill -TERM "$daemon_pid" && wait "$daemon_pid"
	[ -n "$server_pid" ] && kill "$server_pid"
	nft delete table inet e2e 2>/dev/null
	echo "--- phantomd log"; cat "$work/phantomd.log"
	echo "--- server log"; cat "$work/server.log"
}
trap cleanup EXIT

step() { echo; echo "=== $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

step "certificate for phantom.test from a throwaway CA"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
	-keyout ca.key -out ca.pem -days 2 -subj "/CN=Phantom e2e CA" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
	-keyout srv.key -out srv.csr -subj "/CN=phantom.test" 2>/dev/null
printf 'subjectAltName=DNS:phantom.test\n' > ext.cnf
openssl x509 -req -in srv.csr -CA ca.pem -CAkey ca.key -CAcreateserial \
	-out srv.pem -days 2 -extfile ext.cnf 2>/dev/null
chmod 644 srv.key

step "keys and configs"
keys=$("$bin/phantom-keygen")
priv=$(echo "$keys" | awk '/^Server Private Key:/ {print $4}')
pub=$(echo "$keys" | awk '/^Server Public Key:/ {print $4}')
psk=$(echo "$keys" | awk '/^PSK:/ {print $2}')
cat > server.yaml <<EOF
listen: "127.0.0.1:8443"
domain: "phantom.test"
cert_file: "$work/srv.pem"
key_file: "$work/srv.key"
private_key: "$priv"
psk: "$psk"
log_level: "debug"
EOF
cat > client.yaml <<EOF
server: "127.0.0.1:8443"
domain: "phantom.test"
fingerprint: "auto"
psk: "$psk"
server_public_key: "$pub"
pool_size: 2
EOF
chmod 644 server.yaml client.yaml

step "server, as nobody, with its own traffic kept out of the tunnel"
# On a real deployment the server is another machine. Here everything it
# sends - its relayed connections and its own DNS queries, which the DNS
# redirect would otherwise send straight back into the tunnel - carries
# phantomd's mark, so the rules treat it as outside traffic.
nft add table inet e2e
nft add chain inet e2e out '{ type route hook output priority mangle; }'
nft add rule inet e2e out meta skuid 65534 meta mark set 0x5048
# The mark lands after the kernel already picked the source address - the
# tunnel's, from the route it took unmarked - so it's rewritten on the way out.
# phantomd marks its sockets before connecting and never needs this.
nft add chain inet e2e post '{ type nat hook postrouting priority srcnat; }'
nft add rule inet e2e post meta skuid 65534 oifname != '{ "lo", "phantom0" }' masquerade
# Strict reverse-path filtering, as RHEL-like systems ship it: replies to
# phantomd's direct connections must still get through (see addMarkRestore).
sysctl -w net.ipv4.conf.all.rp_filter=1
runuser -u nobody -- "$bin/phantom-server" -config server.yaml > server.log 2>&1 &
server_pid=$!
sleep 1
kill -0 "$server_pid" || fail "server did not start"

step "phantomd"
SSL_CERT_FILE="$work/ca.pem" "$bin/phantomd" > phantomd.log 2>&1 &
daemon_pid=$!
for _ in $(seq 50); do [ -S /run/phantom/phantomd.sock ] && break; sleep 0.1; done
[ -S /run/phantom/phantomd.sock ] || fail "phantomd socket never appeared"

ctl() { runuser -u nobody -- "$bin/phantomd" "$@"; } # an unprivileged client, like the window

step "baseline, before connecting"
# The tunnel's resolver forwards to 1.1.1.1 - if this runner can't reach it,
# DNS through the tunnel can't work either, and that's the runner, not us.
direct_dns=0
if dig +time=3 +tries=1 +short @1.1.1.1 example.com A | grep -Eq '^[0-9.]+$'; then direct_dns=1; fi
echo "1.1.1.1 reachable directly: $direct_dns"
example_ip=$(getent ahostsv4 example.com | awk 'NR==1 {print $1}')
echo "example.com = $example_ip"
curl -sS --max-time 20 -o /dev/null https://example.com || fail "no network even before connecting"

step "connect (full tunnel)"
ctl ping client.yaml
ctl connect client.yaml
ctl status
ip rule show
ip route show table 20552
ip -6 route show table 20552 || true

ip rule show | grep -q 'not from all fwmark 0x5048 lookup 20552' || fail "tunnel rule missing"
ip rule show | grep -q 'lookup main suppress_prefixlength 0' || fail "suppress rule missing"
ip route show table 20552 | grep -q 'default dev phantom0' || fail "tunnel default route missing"

step "traffic goes through the tunnel"
down() { ctl status | tail -1 | grep -o '"bytes_down":[0-9]*' | cut -d: -f2; }
before=$(down)
# By address first: TCP through the tunnel, independent of DNS.
curl -sS --max-time 20 --resolve "example.com:443:$example_ip" -o /dev/null -w '%{http_code}\n' https://example.com | grep -q '^200$' || fail "https through the tunnel"
after=$(down)
echo "bytes down through the tunnel: $before -> $after"
[ "${after:-0}" -gt "${before:-0}" ] || fail "tunnel counters did not move"

step "DNS to any resolver is answered through the tunnel"
# 192.0.2.53 is TEST-NET - nothing answers there. Only the redirect to the
# tunnel's resolver can produce an answer.
if [ "$direct_dns" = 1 ]; then
	dig +time=5 +tries=1 +short @192.0.2.53 example.com A | grep -Eq '^[0-9.]+$' || fail "DNS redirect"
	curl -sS --max-time 20 -o /dev/null https://example.com || fail "https by name through the tunnel"
else
	echo "SKIP: this runner can't reach 1.1.1.1, so tunnelled DNS can't be checked here"
fi

step "smart mode: listed site tunnelled, the rest direct"
ctl connect client.yaml --smart example.com
curl -sS --max-time 20 --resolve "example.com:443:$example_ip" -o /dev/null -w '%{http_code}\n' https://example.com | grep -q '^200$' || fail "listed site in smart mode"
curl -sS --max-time 20 -o /dev/null -w '%{http_code}\n' https://github.com | grep -Eq '^(200|301|302)$' || fail "unlisted site in smart mode"

step "disconnect restores the network"
ctl disconnect
ip rule show
if ip rule show | grep -q 20552; then fail "rules left behind"; fi
if ip link show phantom0 2>/dev/null; then fail "phantom0 left behind"; fi
if ip route show table 20552 2>/dev/null | grep -q .; then fail "routes left behind"; fi
curl -sS --max-time 20 -o /dev/null https://example.com || fail "network after disconnect"

step "SIGTERM while connected restores the network too"
ctl connect client.yaml
kill -TERM "$daemon_pid"
wait "$daemon_pid" || true
daemon_pid=""
if ip rule show | grep -q 20552; then fail "rules left behind after SIGTERM"; fi
curl -sS --max-time 20 -o /dev/null https://example.com || fail "network after SIGTERM"

step "all checks passed"
