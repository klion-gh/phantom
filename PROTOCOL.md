# Phantom — Protocol & Implementation Reference

This document is a ground-truth technical reference for the Phantom codebase in this
repository. It is written to be read by an LLM (or engineer) with no prior context. It
describes what the code actually does, not aspirational design goals — where a
limitation or residual risk exists, it's called out explicitly.

Repo root: `phantom/` (on disk: `phantom-tls-updated/`). Go module: `phantom` (Go 1.26).

This is a ground-up hardened rewrite of an earlier prototype (informally "v1", kept in
a sibling directory as a reference/fallback and not otherwise relevant here). Every
design decision in the wire protocol exists specifically to close a weakness identified
in that prototype: a self-signed certificate baked into the binary, no forward secrecy,
a distinctive fixed-size frame sent immediately after the TLS handshake, unauthenticated
probes getting silence instead of realistic behavior, and padding that was scaffolded
but never wired in.

---

## 1. What this project is

Phantom is a personal VPN protocol designed so that, on the wire, a connection to the
server is indistinguishable from an ordinary HTTPS visit to a real website — because it
genuinely is one, terminated with a real CA-signed certificate for a domain the
operator controls. Authentication and key exchange are smuggled inside what looks like
an unremarkable WebSocket-upgrade HTTP request; anything that isn't authenticated gets
served a real small decoy website instead of being dropped.

One Go implementation of the protocol (`internal/`) backs three clients:

- **Desktop proxy** (`cmd/client`): local SOCKS5 (`127.0.0.1:1080`) + HTTP CONNECT
  (`127.0.0.1:1081`) proxy. Doesn't need Administrator; only tunnels traffic from apps
  explicitly pointed at the proxy, not the whole machine.
- **Windows app** (`windows/`, `phantom.exe`): a full system-wide VPN via a Wintun
  adapter (Wails v2 GUI). All IP traffic on the machine goes through the tunnel, not
  just proxy-aware apps. See §11.
- **Android app** (`android/`, package `com.phantom.vpn`): a real system-wide VPN via
  `VpnService`, backed by the same Go core (`mobile/`) compiled with `gomobile bind`.
  See §10.

The Windows and Android apps share one packet-routing core (`internal/netstack`, a
gVisor userspace network stack) and one "preview a server without connecting" core
(`internal/pingcheck`) — the only genuinely platform-specific code in either app is how
raw IP packets get in and out (a raw file descriptor from Android's `VpnService` vs. a
Wintun device on Windows) and the GUI itself.

---

## 2. Repository layout

```
phantom/
├── cmd/
│   ├── client/main.go     Desktop client: SOCKS5 + HTTP CONNECT proxy
│   ├── server/main.go     Server: ACME cert, disguised handshake, TCP+UDP relay, decoy
│   ├── vpn/main.go        Interactive console manager for cmd/client (on/off/status/log
│   │                      commands) + Windows system-proxy toggling - NOT a tray icon,
│   │                      despite the name; see §11 for the actual tray icon
│   └── keygen/main.go     Generates the server's long-term X25519 keypair + PSK
├── internal/
│   ├── common/
│   │   └── pool.go           Small shared byte-slice sync.Pool
│   ├── config/                YAML config loading/parsing (client.yaml / server.yaml)
│   ├── protocol/
│   │   ├── frame.go             6-byte binary frame header + real bucket padding
│   │   └── crypto.go             Ephemeral-ECDH-derived HKDF keys + XChaCha20-Poly1305
│   ├── handshake/
│   │   └── handshake.go          Disguised WebSocket-upgrade handshake + embedded auth/ECDH
│   ├── transport/
│   │   ├── tls_client.go         uTLS client dial (Chrome/Firefox/Safari fingerprint)
│   │   ├── tls_server.go         Real ACME (Let's Encrypt) cert via HTTP-01 + decoy dispatch
│   │   ├── decoy.go              Realistic fallback site for unauthenticated connections
│   │   └── connpool.go           Pool of parallel TLS connections, self-healing on death
│   ├── tunnel/
│   │   ├── multiplexer.go        Frame read/write loops, stream table (ported, see §4.3)
│   │   ├── stream.go              Per-stream Read/Write/Close
│   │   └── session.go             Open/OpenUDP/Accept over a Multiplexer
│   ├── proxy/
│   │   ├── socks5.go              Client-side SOCKS5 → session.Open()
│   │   ├── http_proxy.go          Client-side HTTP CONNECT → session.Open()
│   │   └── direct.go              Server-side outbound: TCP io.Copy + UDP datagram relay
│   ├── netstack/
│   │   └── netstack.go            Shared gVisor wiring: stack.New, NIC, TCP/UDP forwarders,
│   │                              splice loops - platform-neutral, fed by either a raw fd
│   │                              (Android) or a channel.Endpoint (Windows) - see §9
│   └── pingcheck/
│       └── pingcheck.go           One real disguised handshake, timed, no tunnel built -
│                                  backs both apps' "preview a saved server" UI feature
├── mobile/mobile.go        gomobile-bind entry point: internal/netstack ⇄ Phantom session
├── android/                 Kotlin/Compose app (package com.phantom.vpn) using mobile.aar - §10
├── windows/                 Wails v2 app (Go + HTML/CSS/JS), phantom.exe - §11
├── configs/                 Working client.yaml / server.yaml
├── scripts/install.sh       One-command server install/uninstall (curl | sh)
└── Makefile
```

---

## 3. Connection lifecycle, end to end

```
1. TCP connect to the server's real domain:port.
2. Real TLS 1.3 handshake. Client mimics a real browser ClientHello via uTLS
   (SNI = the operator's real domain). Server presents a genuine CA-signed
   certificate for that domain (ACME/Let's Encrypt) - not a self-signed one.
3. Disguised handshake (internal/handshake), riding on top of that real TLS
   session:
     - Client sends what parses as an ordinary HTTP/1.1 WebSocket-upgrade
       request. A fresh per-connection X25519 ephemeral keypair's public key
       and an HMAC proof are base64'd into a Cookie header.
     - Server validates the proof (requires knowing the PSK and completing a
       real ECDH with its own static key - see §5) and that it's bound to
       *this* TLS connection (via TLS exporter keying material, RFC 5705).
     - Valid  -> server replies "101 Switching Protocols" (a normal-looking
       upgrade response) and the connection becomes the tunnel.
     - Invalid/missing/malformed -> the connection is handed to a real decoy
       website (internal/transport/decoy.go) instead of being dropped.
4. Multiplexer (internal/tunnel) takes over the now-authenticated connection.
   It carries no authentication of its own - the auth in step 3 is the only
   auth step - so the first bytes it puts on the wire are ordinary application
   frames, not a fixed-size auth record right after the TLS handshake (see §4.3).
5. Application data flows as TCP-relay or UDP-relay streams (session.Open /
   session.OpenUDP), each DATA frame's plaintext padded to a fixed bucket size
   before encryption.
```

A `Ping` (§9) is the same steps 1-3 only, closing the connection immediately after step
3 succeeds instead of proceeding to step 4 — used by both GUI apps to show a saved
config's live latency without building a tunnel.

---

## 4. Wire protocol

### 4.1 Frame format (`internal/protocol/frame.go`)

```
Offset  Size   Field
0       1      Type
1       2      StreamID   (big-endian uint16)
3       1      Flags
4       2      Payload length (big-endian uint16, so payload ≤ 65535 bytes)
6       N      Payload
```

Frame types:

| Value | Name | Used for |
|-------|------|----------|
| 0x00 | `FrameData`    | Stream payload (encrypted + padded) |
| 0x01 | `FrameOpen`    | Open a new logical stream; payload = target `"host:port"` |
| 0x02 | `FrameClose`   | Close a logical stream |
| 0x03 | `FramePing`    | Keepalive; echoed back verbatim |
| 0x04 | `FrameSettings`| Received and ignored - vestigial, no sender ever emits it |
| 0x05 | `FramePadding` | Received and ignored - vestigial, no sender ever emits it |

Flags: only `FlagUDP = 0x04` (marks a stream as UDP-relay rather than TCP-relay, see §7).

### 4.2 Padding

`PadPlaintext`/`UnpadPlaintext` wrap every `FrameData` plaintext as
`[2-byte real length][real payload][random padding]`, sized up to the nearest of
`BucketSizes = []int{256, 512, 1024, 2048, 4096}` (or, past 4096, the next multiple of
4096 - e.g. `io.Copy`'s default 32KB buffer). This means two different payload sizes
that land in the same bucket produce **identical wire sizes** once encrypted - verified
directly by `TestPadPlaintextSameSizeDifferentPayloads` and
`TestEncryptFrameHidesPlaintextLength`. This padding is applied *inside*
`SessionCrypto.EncryptFrame`/`DecryptFrame` (`internal/protocol/crypto.go`), so it's
transparent to the multiplexer and every caller.

### 4.3 No in-band authentication

The Multiplexer carries no authentication of its own. The only auth step is the
disguised handshake (§5), which completes before a `Multiplexer` is even
constructed - so `NewMultiplexer(conn, crypto)` takes no auth flags and the
first thing it writes is ordinary application data. This is deliberate: it means
nothing fixed-size and protocol-specific rides right after the TLS handshake,
which is exactly the "distinctive fixed-size frame sent immediately after the
TLS handshake" signature the v1 prototype had and this rewrite set out to remove
(§1). The v1 in-band `FrameAuth` path (`sendAuth`/`expectAuth`/`WaitForAuth`/
`handleAuth`, the `FrameAuth` frame type, and `protocol.ComputeAuthTag`/
`VerifyAuthTag`) was removed entirely rather than carried over dead.

---

## 5. The disguised handshake (`internal/handshake/handshake.go`)

This is the actual security core of the project.

### 5.1 Key exchange

- The **server** has a long-term X25519 keypair (`cmd/keygen` generates it; the private
  half goes in `server.yaml`'s `private_key`, the public half in every client's
  `client.yaml` as `server_public_key`).
- The **client** generates a **fresh X25519 ephemeral keypair on every single
  connection** (including every `Ping`) and computes
  `ecdhSecret = X25519(clientEphemeralPriv, serverStaticPub)`. The server computes the
  same value the other way: `X25519(serverStaticPriv, clientEphemeralPub)`.
- `protocol.DeriveSessionKeys(ecdhSecret, psk, clientEphemeralPub, serverStaticPub)`
  (`internal/protocol/crypto.go`) mixes the ECDH secret **and** a long-term PSK **and**
  both public keys through HKDF-SHA256 to produce `InnerKey` (frame encryption) and
  `AuthKey` (the handshake proof HMAC - §5.2/§5.3).
- **Forward secrecy**: because `ecdhSecret` is different on every connection, compromising
  the long-term PSK alone is not enough to decrypt a previously captured session.
  (Caveat shared with any semi-static ECDH scheme, including XTLS Reality's identical
  approach: if the *server's* long-term private key is later compromised *and* the
  traffic was recorded, past sessions become computable, since the server's key is
  static. Full forward secrecy against that specific threat would need an
  ephemeral-ephemeral exchange on both sides, which isn't implemented here.)

### 5.2 Disguise shape

Right after the real outer TLS handshake, the client sends:

```
GET /ws HTTP/1.1              <- path picked from a small pool of plausible paths
Host: <operator's real domain>
Connection: Upgrade
Upgrade: websocket
Sec-WebSocket-Version: 13
Sec-WebSocket-Key: <standard random base64, RFC 6455>
Cookie: session=<base64url(clientEphemeralPub || 16-byte truncated HMAC tag)>
```

This is a genuinely ordinary-looking HTTP/1.1 request; the only thing carrying the
protocol's actual secret material is the Cookie value, which is indistinguishable from
an opaque session token to anyone without the PSK.

The server parses this with `net/http.ReadRequest`, extracts and decodes the cookie,
recomputes the ECDH + HKDF + tag, and compares in constant time
(`crypto/hmac.Equal`). On success it replies:

```
HTTP/1.1 101 Switching Protocols
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Accept: <correctly computed per RFC 6455>
```

...and the raw connection becomes the Phantom tunnel from that point on (or, for a
`Ping`, the client closes it here). Both the request and response are complete,
protocol-correct HTTP/1.1 WebSocket-upgrade messages; nothing about their *shape*
betrays the tunnel.

### 5.3 Replay protection (channel binding)

The HMAC tag is computed over `clientEphemeralPub || tlsExporterValue`, where
`tlsExporterValue` comes from **TLS 1.3 exported keying material** (RFC 5705):

```go
state := conn.ConnectionState()
binding, _ := state.ExportKeyingMaterial("phantom-handshake", nil, 32)
```

Both sides derive this independently from their own view of the *same* outer TLS
session. Because it's unique per TLS connection, a captured Cookie value replayed on a
*different* TLS connection recomputes to a different expected tag and fails -
verified directly by `TestHandshakeMismatchedTLSBindingFails`.

**Known gotcha, fixed in `tls_client.go`**: mimicking a real Chrome ClientHello via
uTLS causes the ClientHello to include a `renegotiation_info` extension, which makes
the underlying TLS stack set `config.Renegotiation` to a non-`Never` value to match a
real browser — and Go's `crypto/tls` (and uTLS, which forks it) unconditionally
**refuses to export keying material at all** whenever renegotiation is enabled, since
it can't be considered stable across a possible renegotiation. Since
`c.config.Renegotiation` is read fresh from the config on every `ConnectionState()`
call (not latched at handshake time), `tls_client.go` resets
`utlsCfg.Renegotiation = utls.RenegotiateNever` immediately after the handshake
completes and before ever calling the exporter — the ClientHello bytes are already on
the wire by that point, so the fingerprint is unaffected, but `ExportKeyingMaterial`
becomes available again. This is a real, previously-hit build/runtime error
(`"ExportKeyingMaterial is unavailable when renegotiation is enabled"`); if this fix is
ever reverted, every real connection attempt (and every `Ping`) will fail at the
handshake step.

### 5.4 Server-side dispatch (`ServerHandshake` return shape)

`ServerHandshake` returns one of three outcomes, and the caller (`tls_server.go`)
handles all three:

1. **Authenticated**: `(*ServerResult, nil, nil)` — the 101 response has already been
   written; hand the connection to the real tunnel handler.
2. **Parsed as HTTP but not authenticated**: `(nil, *http.Request, nil)` — nothing has
   been written yet; serve `cfg.Decoy` using the already-parsed request.
3. **Not even valid HTTP**: `(nil, nil, err)` — close the connection (matches how a
   real server would behave for garbage input; not worth a full decoy response).

---

## 6. TLS layer

### 6.1 Client (`internal/transport/tls_client.go`)

`transport.Dial(ctx, *TLSClientConfig)` is the single client-side entry point every
caller uses — `cmd/client`, `mobile.Start`, `windows/wintun.go`'s `StartWindows`, and
`internal/pingcheck.Ping` all call it directly.

- uTLS-based fingerprint mimicry (`fingerprint` config field). Default is `chrome133`
  (`utls.HelloChrome_133`), which carries a real `X25519MLKEM768` post-quantum hybrid
  key share in its ClientHello - matching current real Chrome, where a majority of
  connections now include one. `chrome131` is the same story (`X25519MLKEM768` too);
  `chrome120` (`utls.HelloChrome_120`) is kept only for explicit opt-in/backward
  compatibility and predates Chrome's PQ rollout, making it the more anomalous-looking
  ClientHello of the two now, not the safer/older-is-stabler choice it used to be - a
  passive fingerprint-matching censor comparing ClientHello shape against the current
  real-browser population (JA3/JA4-style) can use "claims to be modern Chrome but has no
  PQ key share" as a distinguisher precisely because that share is no longer rare.
  `firefox120`/`safari16` remain available but have no PQ-carrying capture in the pinned
  uTLS version. This only affects the outer TLS camouflage layer (what a passive
  fingerprint check sees) - it has no bearing on Phantom's own inner handshake crypto
  (§5.1's semi-static X25519 ECDH), which is unrelated and still classical.
- **SNI is the operator's real domain**, not a borrowed/spoofed one.
- **Certificate validation is real** (no `InsecureSkipVerify`) — since the server
  presents a genuinely CA-signed certificate, the client validates it exactly like a
  real browser would.
- `ProtectFD` hook: on Android, wired to `VpnService.protect()` via a `net.Dialer.Control`
  callback so the app's own connection to the server bypasses its own VPN routing (see
  §10). Windows has no equivalent per-socket exemption API — `windows/wintun.go` solves
  the same underlying problem with a routing-table trick instead (see §11.2). `Ping`
  doesn't set `ProtectFD` at all since it never establishes a competing `0.0.0.0/0`
  route in the first place.

### 6.2 Server (`internal/transport/tls_server.go`)

- Certificate: **real, automatically obtained and renewed** via
  `golang.org/x/crypto/acme/autocert`, using the **HTTP-01** challenge type
  specifically (not TLS-ALPN-01) — this was a deliberate choice so the VPN's own
  listener can run on any port (deployed at `:8443`) while only a small, separate
  HTTP responder needs port 80 reachable, purely for the infrequent (~every 60 days)
  issuance/renewal handshake with Let's Encrypt. Port 80 carries no VPN traffic.
- Confirmed working against the live Let's Encrypt production API (not just staging):
  `openssl s_client` against a deployed server shows `issuer=C=US, O=Let's Encrypt`.
- `MinVersion: tls.VersionTLS13`.
- Connections that negotiate the ALPN protocol `acme-tls/1` (an artifact of
  `autocert.Manager.TLSConfig()` always enabling ALPN-01 capability regardless of which
  challenge type is actually used) are recognized and left alone — `autocert` answers
  those itself.

### 6.3 Static certificate option (`cert_file`/`key_file`, shared servers)

`ListenAndServe` only uses autocert when `TLSServerConfig.CertFile`/`KeyFile` are both
empty. If set, it instead builds a `tls.Config{GetCertificate: ...}` backed by
`certReloader`, which stats both files on every handshake and only re-parses them (via
`tls.LoadX509KeyPair`) when their mtimes actually changed — a renewal (e.g. certbot
replacing the files at the same path on its own schedule) is picked up automatically,
no restart or deploy-hook needed. A transient stat/read failure mid-renewal serves the
last-known-good certificate rather than failing in-flight handshakes.

This exists because autocert's HTTP-01 challenge needs to bind port 80 itself
(`ListenAndServe`'s inner `http.Server{Addr: ":80", ...}` goroutine) — on a shared box
already running its own web server on 80/443 for something unrelated, that bind loses
silently (logged, not fatal) and every handshake ends up with no certificate to offer
at all, surfacing to clients as a bare `tls: internal error` with no other explanation.
`cert_file`/`key_file` sidesteps ACME entirely for that case by pointing at a
certificate obtained some other way for the same domain (typically the shared box's
own certbot, already renewing itself independently of Phantom).

---

## 7. UDP relay

A stream opened with `FlagUDP` set (`Session.OpenUDP`) is treated by the server
(`internal/proxy/direct.go`) as a UDP relay — `net.Dial("udp", target)`, with each
`Write`/`Read` treated as exactly one datagram (relying on `Stream.Write` always
producing exactly one `FrameData` frame per call, and `Stream.Read` returning exactly
one queued frame's payload per call when the buffer is large enough — both guaranteed
by `internal/tunnel/stream.go`). Idle timeout: 60 seconds. This is what lets the mobile
and Windows VPN tunnels relay DNS/QUIC/WebRTC and other UDP-based traffic, not just TCP
— see §9's forwarder setup.

The same UDP relay is also reachable through the local SOCKS5 proxy: `socks5.go`
implements **UDP ASSOCIATE** (RFC 1928), binding a loopback UDP socket for the
association's lifetime (tied to the control TCP connection) and relaying each
SOCKS5-UDP-wrapped datagram to a per-destination `Session.OpenUDP` stream, with the
same 60-second idle eviction per destination. So an app pointed at the proxy (the
desktop `cmd/client`, or either GUI app's independent per-config proxy) can send UDP
through Phantom — e.g. Telegram voice calls or plain DNS-over-UDP — not just TCP
CONNECT. Fragmented datagrams (`FRAG != 0`) are unsupported and dropped, as is standard.

---

## 8. Configuration (`internal/config/config.go`)

```yaml
# client.yaml
server: "yourdomain.com:8443"       # required
domain: "yourdomain.com"             # required - SNI + Host header; must match the server's real cert domain
fingerprint: "chrome133"             # default if unset - see §6.1 for the post-quantum note
psk: "<64 hex chars>"                 # required - shared secret, one HKDF input alongside the ECDH secret
server_public_key: "<64 hex chars>"   # required - server's static X25519 public key
listen: "127.0.0.1:1080"              # default; desktop SOCKS5 (cmd/client only)
listen_http: "127.0.0.1:1081"         # default; desktop HTTP CONNECT (cmd/client only)
pool_size: 4                          # default; parallel pooled connections
log_level: "info"                     # not actually read by any logger; plain `log` package used unconditionally
```

The Windows and Android apps import this exact same `client.yaml` text verbatim (pasted
as a whole block, no separate parser on the Kotlin/JS side beyond a couple of scalar
fields read for display — see §10/§11) and can each store several of them side by side.
`listen`/`listen_http` are simply unused by those two apps, since they route all system
traffic rather than exposing a local proxy port.

```yaml
# server.yaml
listen: ":8443"                       # VPN listener; any port - decoupled from ACME (see §6.2)
domain: "yourdomain.com"              # required - must have a public A/AAAA record pointing at this server
acme_email: ""                        # optional; Let's Encrypt contact address
acme_cache_dir: "/var/lib/phantom/acme"  # where the issued cert/key persist across restarts
private_key: "<64 hex chars>"         # required - server's static X25519 private key
psk: "<64 hex chars>"                 # required - must equal every client's psk
decoy_site_dir: ""                    # optional; static files to serve to unauthenticated connections. Empty = built-in placeholder page
handshake_rate_per_sec: 0             # optional; per-IP auth-attempt throttle (0 = default 2/s). See below.
handshake_burst: 0                    # optional; per-IP burst (0 = default 60)
log_level: "info"                     # server-side only: debug|info|warn|error, drives internal/logx. Unused on the client (see above).
```

`handshake_rate_per_sec`/`handshake_burst` are a per-IP anti-enumeration throttle
(`internal/transport/ratelimit.go`): an IP that exceeds its token budget is served the
decoy site *without* an auth attempt rather than dropped, so a scanner hammering the
endpoint just sees an ordinary website and can't map the auth path's behavior by volume.
Both 0/unset use loose defaults (2/s sustained, 60 burst) — generous enough for a legit
client's connection pool + periodic pings and for many users behind one carrier-grade
NAT, while still defanging a high-rate scanner. This is anti-enumeration, not anti-DoS
(a TLS-flood still costs a handshake; that belongs at the firewall). The server also
keeps aggregate counters (authenticated / decoy / non-HTTP / rate-limited / active) and
logs a periodic summary via `internal/logx` — a spike in decoy or rate-limited relative
to authenticated is the earliest server-side sign of scanning.

`cmd/keygen` prints a matched `private_key`/`server_public_key`/`psk` triple.
`scripts/install.sh` runs it automatically during a fresh server install and prints a
ready-to-paste `client.yaml`.

---

## 9. Shared netstack core (`internal/netstack`) and the mobile/Windows bridges

`internal/netstack.New(session *tunnel.Session, linkEndpoint stack.LinkEndpoint, mtu int)`
is the platform-neutral core both GUI apps build on: it constructs the gVisor
`stack.Stack`, creates a NIC bound to whatever `LinkEndpoint` the caller hands in,
enables promiscuous mode + spoofing (needed since the stack is relaying for arbitrary
destinations, not terminating traffic addressed to itself), installs a
`0.0.0.0/0`+`::/0` route table pointing at that NIC, and registers TCP/UDP forwarders
that turn an inbound SYN/first-datagram into `session.Open`/`session.OpenUDP` calls,
splicing bytes between the gVisor endpoint and the Phantom stream in both directions.
Only TCP and UDP are registered — **no ICMP**, so ping-through-the-tunnel doesn't work
end-to-end on either app (see §12).

The only thing that differs per platform is how raw IP packets get into and out of that
`LinkEndpoint`:

- **Android** (`mobile/mobile.go`): `gvisor.dev/gvisor/pkg/tcpip/link/fdbased.New`
  reads/writes the raw TUN file descriptor Android's `VpnService.Builder.establish()`
  handed over. `mobile.Start(configYAML, tunFD, mtu, protector)` parses the config,
  dials via `transport.Dial`, builds the `fdbased` endpoint, and calls
  `netstack.New`. `mobile.Tunnel.Stop()`/`.Stats()`/`.IsAlive()` all just delegate to the
  inner `netstack.Tunnel`.
- **Windows** (`windows/wintun.go`): no raw fd exists on Windows, so
  `gvisor.dev/gvisor/pkg/tcpip/link/channel.New` is used instead — a queue-based
  endpoint with no OS handle requirement. Two goroutines pump packets between it and a
  `golang.zx2c4.com/wireguard/tun` Wintun device (`pumpTunToChannel`/`pumpChannelToTun`).
  See §11.2 for the routing-table setup this needs.

`gvisor.dev/gvisor` is pinned to the exact pseudo-version Tailscale ships in production
(`v0.0.0-20260224225140-573d5e7127a8`) rather than `@latest`, because the latest
upstream snapshot at development time had a broken test file that breaks plain
`go build`/`go list` package loading (filed upstream as google/gvisor#11699, unresolved)
and separately was missing Bazel-generated source files that the Tailscale-pinned
version has checked in.

### 9.1 `internal/pingcheck.Ping` — previewing a server without connecting

Both apps show each saved config's live latency and resolved IP before (and while) the
user is connected to it. `pingcheck.Ping(configYAML string) (Result, error)`:

1. Parses the config, resolves the server host via `net.DefaultResolver.LookupIP(ctx,
   "ip4", host)` — **`"ip4"` specifically, not a dual-stack lookup**: on at least one
   real network this project was tested on, the AAAA query stalled for several seconds
   before falling back to A, which dominated total connect time; skipping it outright
   was the fix (see the identical fix in `windows/wintun.go`'s own server-address
   resolution, §11.2).
2. Dials via `transport.Dial` (the full disguised handshake, §5), timing from just
   before the dial to just after it succeeds.
3. Closes the connection immediately — no session, no tunnel, no netstack involved.

Returns `{IP, LatencyMs}`. `mobile.Ping` wraps this as a JSON string (gomobile-safe
return type, same pattern as `Tunnel.Stats()`); the Windows `App.Ping` method does the
same for its Wails binding. Both UIs poll this on a repeating timer (every ~6s) per
saved config tile and independently resolve a country name/flag for the returned IP via
a public geo-IP HTTP lookup (`ipapi.co` from both apps' own code, not through the Go
core) - the one place in either app that calls a third party, purely for that cosmetic
label (see §12).

---

## 10. Android app (`android/`, package `com.phantom.vpn`)

Kotlin + Jetpack Compose, dark/purple theme (`Theme.kt`). Manual state-based screen
switching (a `Screen` enum in `MainActivity.kt`; no `NavHost`) across four screens:

- **Main** (`MainScreen` in `MainActivity.kt`): a scrollable list of saved-config tiles
  (`ConfigInfoCard`, `ConfigInfo.kt`), one per entry in `ConfigStore`. Each tile shows
  the config's domain, resolved IP, live ping (`fetchPing`/`pingcheck.Ping` via the
  `Mobile.ping` gomobile binding, polled every 6s independently per tile), and country +
  flag (`fetchGeo`, `ipapi.co`; the flag itself is a real image fetched from
  `flagcdn.com` and cached in memory, not the Unicode flag emoji — some Android system
  images/devices lack flag glyphs in their emoji font and fall back to showing the bare
  two-letter code, the same gap Windows has structurally, see §11.3). A circular connect
  button (`ConnectButton.kt`, reused at a smaller `size` for tiles) sits on the right of
  each tile; the currently-connected tile additionally gets a purple→pink→blue gradient
  border (`Modifier.border(width, Brush, shape)`). Header has a "+" button (always adds
  a new tile, never overwrites an existing one) and a gear icon.
- **Add/edit config** (`ConfigScreen`): a textarea for the full `client.yaml` text plus
  Save; reached either via "+" (blank, adds a new `SavedConfig`) or a long-press on an
  existing tile (pre-filled, edits that tile in place and offers a confirm-gated
  "Удалить конфигурацию" delete button).
- **Settings** (`SettingsScreen`): just a "Посмотреть лог" button — config
  management moved out of here into the dedicated add/edit screen above.
- **Log** (`LogScreen`): shows `FileLog`'s persisted plain-text log with a share button.

### 10.1 Config storage (`ConfigStore.kt`)

A list of `SavedConfig(id: String, yaml: String)`, backed by `EncryptedSharedPreferences`
(falling back to plain `SharedPreferences` if Android Keystore access throws on a given
device/ROM — `FileLog.e(...)` records which path was taken). One-time migration: if the
old pre-multi-config single `"client_yaml"` key exists and the new list key doesn't,
it's wrapped into a one-entry list and the old key removed.

### 10.2 Connecting, switching, and the persistent notification

`PhantomVpnService` (a `VpnService` subclass) exposes `ACTION_CONNECT` (with
`EXTRA_CONFIG_ID`/`EXTRA_CONFIG_YAML`), `ACTION_DISCONNECT`, and `ACTION_SHOW_STATUS`.
Tapping a different tile's connect button while another is already active reuses the
same `connect()` call — it tears down the previous tunnel/TUN fd before establishing the
new one, rather than requiring an explicit disconnect first.

A persistent, ongoing notification (posted via `startForeground`/`NotificationManager.notify`
depending on state, never removed on disconnect — `stopForeground(false)`/`STOP_FOREGROUND_DETACH`
detaches without clearing it) mirrors the connect/disconnect state with an action button
("Подключить"/"Отключить"). Tapping "Подключить" from the notification (no fresh config
extras available from a static `PendingIntent`) resumes whichever config ID was last
connected (`VpnStateHolder`'s `activeConfigId`, persisted across restarts), falling back
to the first saved config if none. On Android 13+ (`TIRAMISU`), `MainActivity` requests
the runtime `POST_NOTIFICATIONS` permission on first launch — without it the
notification silently never appears, since the manifest `<uses-permission>` declaration
alone isn't sufficient starting with that API level.

`VpnStateHolder` (`VpnState.kt`) is a simple `MutableStateFlow<VpnState>` bridge between
the service and the Compose UI; `VpnState` carries `status`/`message`/`activeConfigId`
(the last reset to `null` whenever `status` goes back to `IDLE`).

### 10.3 `Protector` / routing-loop prevention

Once `VpnService.Builder.establish()` installs a `0.0.0.0/0`+`::/0` route through the
TUN interface, the app's own outbound connection to the Phantom server would be
captured by its own tunnel and never complete. `PhantomVpnService` passes a `Protector`
implementation (`Protect(fd) = this@PhantomVpnService.protect(fd)`, i.e.
`VpnService.protect()`) into `Mobile.start`, which threads it into
`transport.TLSClientConfig.ProtectFD` (§6.1) via a `net.Dialer.Control` callback that
runs before the TCP connection completes.

---

## 11. Windows app (`windows/`, `phantom.exe`)

A Wails v2 app: Go backend (`App` struct in `app.go`, methods bound to
`window.go.main.App.*` in JS) + plain HTML/CSS/JS frontend (`frontend/`, no framework) in
the OS's native WebView2 control. Visually mirrors the Android app (same palette, same
tile layout, same gradient-border-when-connected treatment) since both are driven by
the same underlying data shape (`SavedConfig{id, yaml}`, ping/geo polling per tile).

### 11.1 Why a second, heavier client alongside `cmd/client`

`cmd/client` remains as a lighter-weight, no-Administrator-required option that only
tunnels traffic from apps explicitly pointed at the SOCKS5/HTTP proxy - useful for
headless use (WSL, a Linux box, curl/scripting) or routing just one app/browser profile
through Phantom without touching the rest of the machine's traffic. `windows/` is a full
system-wide VPN — all IP traffic goes through it — which requires creating a TUN adapter
and modifying the routing table, both of which need Administrator
(`build/windows/wails.exe.manifest` sets `requestedExecutionLevel="requireAdministrator"`,
so Windows shows the UAC prompt on launch automatically). An earlier `cmd/vpn` console
wrapper around `cmd/client` (start/stop/toggle the Windows system proxy) was removed once
`windows/` existed - it added nothing `windows/` didn't already do better (a full-tunnel
GUI supersedes a proxy-only CLI for the desktop case), and had a stale hardcoded server IP
in its status output.

### 11.2 `StartWindows` (`wintun.go`) — order of operations matters

Once a `0.0.0.0/0` route exists through the TUN interface, this process's own connection
to the Phantom server would loop back into the tunnel it's building — the same class of
bug as Android's routing loop (§10.3), but Windows has no per-socket exemption API, so
the fix is routing-table specificity instead. `StartWindows` does, strictly in this
order:

1. Resolve the server's IP (`net.DefaultResolver.LookupIP(ctx, "ip4", host)` — see the
   AAAA-stall note in §9.1, identical fix applied here) and find the current default
   gateway (`route print -4 0.0.0.0`, picking the lowest-metric entry whose gateway is
   an actual IP — this naturally skips any *other* already-active VPN's own `On-link`
   default route, which has no real gateway address to parse).
2. Add a `/32` host route for the server IP via that *original* gateway
   (`route add <ip> mask 255.255.255.255 <gateway>`) — more specific than the `/0` route
   added in step 5, so Windows' longest-prefix-match always prefers it regardless of
   metric.
3. Only now dial and establish the Phantom session (`transport.Dial`, pooled via
   `transport.NewConnPool`).
4. Create the Wintun device (`golang.zx2c4.com/wireguard/tun.CreateTUN`), assign it
   `10.10.0.2/24`, set DNS (`netsh interface ip set/add dns ... validate=no` — omitting
   `validate=no` was previously the single largest chunk of connect time, since `netsh`
   by default probes each DNS server for reachability before committing, which stalls
   for several seconds on a freshly-created adapter with routing not fully up yet).
5. Pin the new adapter's own interface metric to `1`
   (`netsh interface ipv4 set interface <name> metric=1`) *and* add the `0.0.0.0/0` route
   at route-metric `1` (`netsh interface ipv4 add route 0.0.0.0/0 name=<name> metric=1`).
   Windows' actual route preference is `route metric + interface metric`, not the route
   metric alone — a fresh Wintun adapter's automatically-computed interface metric can
   outrank even a fast physical NIC, so the `0.0.0.0/0` route can silently lose the
   routing race (traffic keeps going out the old path, external IP never changes) unless
   the interface's own metric is also pinned low, not just the route's.
6. Bridge the Wintun device into `internal/netstack.New` via a gVisor `channel.Endpoint`
   (§9) and start the TCP/UDP forwarders.

`Stop()` tears down in reverse, and explicitly `route delete`s the step-2 bypass host
route — it isn't tied to the tunnel interface's lifetime the way the `0.0.0.0/0` route
is (Windows drops routes bound to an interface's LUID automatically once that interface
disappears; a route via the *physical* gateway needs an explicit removal). Switching
from one saved config to another (`App.Connect` called again while a tunnel is already
up) tears down the previous tunnel first, same as the Android side.

All of `wintun.go`'s `route`/`netsh` subprocess calls run through a `runNetCmd` helper
that sets `syscall.SysProcAttr{HideWindow: true}` (otherwise each one flashes a visible
console window, since this is a GUI app with no console of its own) and logs the exact
command and its output to `phantom.log` either way — useful for diagnosing exactly which
step failed without attaching a debugger.

`wintun.dll` (the actual driver, same one WireGuard-for-Windows uses) is embedded in the
binary via `//go:embed` and extracted next to the running exe on first launch
(`wintun_dll.go`) — end users still only download one `.exe`.

### 11.3 Multi-config storage and Ping (`configstore.go`, `app.go`)

Same `SavedConfig{ID, Yaml}` list shape as Android, persisted as JSON
(`os.UserConfigDir()/Phantom/configs.json`) with the same one-time migration from the
pre-multi-config single `client.yaml` file. `App` exposes `ListConfigs`/`AddConfig`/
`UpdateConfig`/`DeleteConfig`/`Connect(id, yaml)`/`Disconnect`/`Status`/`Ping`/`ReadLog`
to the frontend. `Ping` wraps `internal/pingcheck.Ping` (§9.1) the same way `mobile.Ping`
does. The frontend (`main.js`) fetches country/flag from `ipapi.co`/`flagcdn.com`
directly (not through Go) — real flag *images*, not emoji, since Windows' Segoe UI Emoji
font has no flag glyphs at all and would otherwise show the bare two-letter country code
(a deliberate, longstanding Microsoft choice, not a WebView2 bug — confirmed by testing,
not assumed).

### 11.4 System tray (`tray.go`)

`github.com/energye/systray` runs its own native message loop on a locked OS thread in
a separate goroutine (`go runTray(app)` in `main.go`, alongside `wails.Run(...)` on the
main goroutine) — the two event loops don't interfere with each other. Right-click shows
a context menu (systray's default behavior when no `SetOnRClick` handler is registered):
a disabled status label, a "Подключить"/"Отключить" toggle (mirrors the Android
notification's last-active-config fallback logic exactly, §10.2), "Открыть"
(`runtime.WindowShow`), and "Выход" (`Disconnect()` then `os.Exit(0)` — a hard exit,
since it also needs to stop the tray's own native loop). Left-click also restores the
window, as a convenience.

Closing the main window (the X button) doesn't quit the app: `App.beforeClose`
(`options.App.OnBeforeClose`) unconditionally calls `runtime.WindowHide` and returns
`true` to cancel the default close-and-quit behavior, so the process (and any active
tunnel) keeps running in the tray until "Выход" is chosen explicitly.

---

## 12. iOS portability path (not implemented in this repo)

`gomobile bind -target=ios ./mobile` would produce an `.xcframework` from the identical
source used for Android. The only platform-specific piece is the link-layer glue in
`mobile.go` (`fdbased.New` reading a raw Android fd directly); iOS's
`NEPacketTunnelProvider` would need gVisor's callback-driven `channel.Endpoint` instead
(exactly the mechanism `windows/wintun.go` already uses for the same reason — no raw fd
available), since `NetworkExtension` doesn't hand out one either. Everything else —
config parsing, the disguised handshake, TCP/UDP forwarders, splicing, `Ping` — carries
over unchanged, since `internal/netstack` and `internal/pingcheck` were already factored
out to be platform-neutral.

---

## 13. Known gaps / residual risks

1. **Single VPS IP is a single point of failure.** CDN fronting (the standard fix for
   this — terminating the outer TLS at real, hard-to-block infrastructure like
   Cloudflare) was explicitly declined for this deployment to avoid a third-party
   dependency on the VPN path itself. Blocking the server's IP still stops everything,
   regardless of how good the wire-level disguise is.
2. **The outer TLS ClientHello carries a post-quantum hybrid key share
   (`X25519MLKEM768`, via the `chrome131`/`chrome133` fingerprints - §6.1), but
   Phantom's own inner handshake does not.** The disguised handshake's session-key
   ECDH (§5.1) is still plain X25519, so the forward-secrecy caveat there is
   unaffected by the outer TLS layer's PQ key share - that share only shapes what a
   passive fingerprint check sees, it doesn't feed into Phantom's own key derivation
   at all. Reality (the closest prior art) shipped `X25519MLKEM768` support in
   production in early 2026 (Xray-core v26.2.4+); this project's outer-layer PQ
   ClientHello was added for the same reason (matching current real-browser
   ClientHello shape) rather than as an independent design choice.
3. **No ICMP support** in either mobile tunnel (Android or Windows) — only TCP and UDP
   are registered with the gVisor stack (§9), so ping-through-the-tunnel doesn't work
   end-to-end on either app.
4. **Semi-static ECDH, not fully ephemeral-ephemeral** — see the forward-secrecy caveat
   in §5.1: a future compromise of the server's long-term private key combined with
   recorded traffic could still decrypt past sessions, same limitation Reality has.
5. **`FrameSettings`/`FramePadding` frame types are vestigial** — parsed and ignored,
   never emitted by any sender. Not a security risk (they're simply never triggered),
   but worth knowing about if extending the multiplexer later. (The v1 prototype's
   in-band `FrameAuth` path was removed outright in this rewrite — see §4.3.)
6. **Both GUI apps call a third-party geo-IP/flag service (`ipapi.co`, `flagcdn.com`)
   directly from the client**, purely to show a cosmetic country name + flag image next
   to each saved server. This is the only network dependency in either app that isn't
   the user's own Phantom server — it leaks the *server's* IP (not the user's own) to
   that third party on a timer. Easy to strip out if that tradeoff isn't wanted; nothing
   else about the tunnel depends on it.
7. **Windows routing/interface setup shells out to `route`/`netsh`** (§11.2) rather than
   using the native IP Helper API (`iphlpapi.dll` via `CreateUnicastIpAddressEntry`/
   `CreateIpForwardEntry2`, the approach `winipcfg`-based tools like WireGuard-Windows
   use). Simpler and lower-risk to get right, at the cost of process-spawn overhead and
   coarser error handling than a native API call would give.
8. **Windows tray icon adds a real third-party Go dependency**
   (`github.com/energye/systray`) with its own native message loop running alongside
   Wails' — low risk in practice (this is a common, well-tested pairing) but worth
   knowing about if something ever looks like an event-loop/threading issue specific to
   Windows.
