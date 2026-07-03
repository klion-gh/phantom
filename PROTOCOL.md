# Phantom — Protocol & Implementation Reference

This document is a ground-truth technical reference for the Phantom codebase in this
repository. It is written to be read by an LLM (or engineer) with no prior context. It
describes what the code actually does, not aspirational design goals — where a
limitation or residual risk exists, it's called out explicitly.

Repo root: `phantom/` (on disk: `phantom-tls-updated/`). Go module: `phantom` (Go 1.26).

This is a ground-up hardened rewrite of an earlier prototype (informally "v1", kept in
a sibling directory as a reference/fallback and not otherwise relevant here). Every
design decision below exists specifically to close a weakness identified in that
prototype: a self-signed certificate baked into the binary, no forward secrecy, a
distinctive fixed-size frame sent immediately after the TLS handshake, unauthenticated
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

Two clients share one Go implementation:
- **Desktop** (`cmd/client`): local SOCKS5 (`127.0.0.1:1080`) + HTTP CONNECT
  (`127.0.0.1:1081`) proxy, plus a Windows tray manager (`cmd/vpn`).
- **Android** (`android/`): a real system-wide VPN via `VpnService`, backed by the same
  Go core (`mobile/`) compiled with `gomobile bind`. Designed so an iOS client could
  reuse the same core later (see §11).

---

## 2. Repository layout

```
phantom/
├── cmd/
│   ├── client/main.go     Desktop client: SOCKS5 + HTTP CONNECT proxy
│   ├── server/main.go     Server: ACME cert, disguised handshake, TCP+UDP relay, decoy
│   ├── vpn/main.go        Windows tray manager for cmd/client + system proxy toggling
│   └── keygen/main.go     Generates the server's long-term X25519 keypair + PSK
├── internal/
│   ├── config/             YAML config loading/parsing (client.yaml / server.yaml)
│   ├── protocol/
│   │   ├── frame.go          6-byte binary frame header + real bucket padding
│   │   └── crypto.go         Ephemeral-ECDH-derived HKDF keys + XChaCha20-Poly1305
│   ├── handshake/
│   │   └── handshake.go      Disguised WebSocket-upgrade handshake + embedded auth/ECDH
│   ├── transport/
│   │   ├── tls_client.go     uTLS client dial (Chrome/Firefox/Safari fingerprint)
│   │   ├── tls_server.go     Real ACME (Let's Encrypt) cert via HTTP-01 + decoy dispatch
│   │   ├── decoy.go          Realistic fallback site for unauthenticated connections
│   │   └── connpool.go       Pool of parallel TLS connections, byte-based rotation
│   ├── tunnel/
│   │   ├── multiplexer.go    Frame read/write loops, stream table (ported, see §4.3)
│   │   ├── stream.go          Per-stream Read/Write/Close
│   │   └── session.go         Open/OpenUDP/Accept over a Multiplexer
│   └── proxy/
│       ├── socks5.go          Client-side SOCKS5 → session.Open()
│       ├── http_proxy.go      Client-side HTTP CONNECT → session.Open()
│       └── direct.go          Server-side outbound: TCP io.Copy + UDP datagram relay
├── mobile/mobile.go        gomobile-bind entry point: gVisor netstack ⇄ Phantom session
├── android/                 Kotlin/Compose app (package com.phantom.vpn) using mobile.aar
├── configs/                 Working client.yaml / server.yaml
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
   Both sides construct it with sendAuth=false/expectAuth=false - the
   authentication that already happened in step 3 makes the Multiplexer's own
   legacy in-band AUTH-frame mechanism unnecessary (see §4.3).
5. Application data flows as TCP-relay or UDP-relay streams (session.Open /
   session.OpenUDP), each DATA frame's plaintext padded to a fixed bucket size
   before encryption.
```

---

## 4. Wire protocol

### 4.1 Frame format (`internal/protocol/frame.go`)

Unchanged 6-byte header shape:

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
| 0x04 | `FrameSettings`| Received and ignored - vestigial, kept only so `multiplexer.go`'s switch statement (ported as-is) still compiles/behaves identically |
| 0x05 | `FramePadding` | Received and ignored - same reason, vestigial |
| 0x06 | `FrameAuth`    | **Dead at runtime.** Both sides always construct their `Multiplexer` with `sendAuth=false`, so this is never sent - see §4.3 |

Flags: only `FlagUDP = 0x04` (marks a stream as UDP-relay rather than TCP-relay, see §7).

### 4.2 Padding (new vs. the earlier prototype)

`PadPlaintext`/`UnpadPlaintext` wrap every `FrameData` plaintext as
`[2-byte real length][real payload][random padding]`, sized up to the nearest of
`BucketSizes = []int{256, 512, 1024, 2048, 4096}` (or, past 4096, the next multiple of
4096 - e.g. `io.Copy`'s default 32KB buffer). This means two different payload sizes
that land in the same bucket produce **identical wire sizes** once encrypted - verified
directly by `TestPadPlaintextSameSizeDifferentPayloads` and
`TestEncryptFrameHidesPlaintextLength`. This padding is applied *inside*
`SessionCrypto.EncryptFrame`/`DecryptFrame` (`internal/protocol/crypto.go`), so it's
transparent to the multiplexer and every caller.

### 4.3 Why the Multiplexer still has unused AUTH-frame code

`internal/tunnel/multiplexer.go` is carried over with its `sendAuth`/`expectAuth`/
`WaitForAuth`/`handleAuth` machinery intact, but every call site in this codebase
constructs it with `sendAuth=false` and no `expectAuth` (defaults false) - see
`cmd/client/main.go`, `cmd/server/main.go`, `mobile/mobile.go`, and the test helpers.
Real authentication now happens earlier, in `internal/handshake`, before a
`Multiplexer` is even created. Keeping the dead code path rather than deleting it was a
deliberate choice to minimize the surface area of the port; `protocol.FrameAuth`,
`protocol.ComputeAuthTag`, `protocol.VerifyAuthTag` all still exist purely so this file
compiles unchanged.

---

## 5. The disguised handshake (`internal/handshake/handshake.go`)

This is the actual security core of the project - the mechanism that replaces the
earlier prototype's bare "AUTH frame as the first bytes after the TLS handshake" (a
signature no real browser produces) and its lack of forward secrecy.

### 5.1 Key exchange

- The **server** has a long-term X25519 keypair (`cmd/keygen` generates it; the private
  half goes in `server.yaml`'s `private_key`, the public half in every client's
  `client.yaml` as `server_public_key`).
- The **client** generates a **fresh X25519 ephemeral keypair on every single
  connection** and computes `ecdhSecret = X25519(clientEphemeralPriv, serverStaticPub)`.
  The server computes the same value the other way:
  `X25519(serverStaticPriv, clientEphemeralPub)`.
- `protocol.DeriveSessionKeys(ecdhSecret, psk, clientEphemeralPub, serverStaticPub)`
  (`internal/protocol/crypto.go`) mixes the ECDH secret **and** a long-term PSK **and**
  both public keys through HKDF-SHA256 to produce `InnerKey` (frame encryption) and
  `AuthKey` (handshake proof / dead in-band-AUTH code path).
- **Forward secrecy**: because `ecdhSecret` is different on every connection (fresh
  client ephemeral key each time), compromising the long-term PSK alone is no longer
  enough to decrypt a previously captured session — unlike the earlier prototype, where
  the key was 100% static and derived from the PSK alone. (Note the same caveat that
  applies to any semi-static ECDH scheme like this, including XTLS Reality's identical
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

...and the raw connection becomes the Phantom tunnel from that point on. Both the
request and response are complete, protocol-correct HTTP/1.1 WebSocket-upgrade
messages; nothing about their *shape* betrays the tunnel.

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
ever reverted, every real connection attempt will fail at the handshake step.

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

Same uTLS-based fingerprint mimicry as before (`chrome120`/`firefox120`/`safari16` via
the `fingerprint` config field), but:
- **SNI is the operator's real domain**, not a borrowed/spoofed one.
- **Certificate validation is real** (no `InsecureSkipVerify`) — since the server now
  presents a genuinely CA-signed certificate, the client validates it exactly like a
  real browser would. This is a meaningful behavioral difference from a design that
  fakes a certificate and has to skip validation to tolerate it.
- `ProtectFD` hook unchanged from the mobile-VPN-routing-loop fix (see §9.4-equivalent
  in `mobile.go`'s comments) — Android's `VpnService.protect()` is wired through a
  `net.Dialer.Control` callback so the app's own connection to the server bypasses its
  own VPN routing.

### 6.2 Server (`internal/transport/tls_server.go`)

- Certificate: **real, automatically obtained and renewed** via
  `golang.org/x/crypto/acme/autocert`, using the **HTTP-01** challenge type
  specifically (not TLS-ALPN-01) — this was a deliberate choice so the VPN's own
  listener can run on any port (deployed at `:8443`) while only a small, separate
  HTTP responder needs port 80 reachable, purely for the infrequent (~every 60 days)
  issuance/renewal handshake with Let's Encrypt. Port 80 carries no VPN traffic.
- Confirmed working against the live Let's Encrypt production API (not just staging):
  `openssl s_client` against the deployed server shows `issuer=C=US, O=Let's Encrypt`.
- `MinVersion: tls.VersionTLS13`.
- Connections that negotiate the ALPN protocol `acme-tls/1` (an artifact of
  `autocert.Manager.TLSConfig()` always enabling ALPN-01 capability regardless of which
  challenge type is actually used) are recognized and left alone — `autocert` answers
  those itself.

---

## 7. UDP relay

Unchanged in design from the ideas in the earlier prototype, carried over intact: a
stream opened with `FlagUDP` set (`Session.OpenUDP`) is treated by the server
(`internal/proxy/direct.go`) as a UDP relay — `net.Dial("udp", target)`, with each
`Write`/`Read` treated as exactly one datagram (relying on `Stream.Write` always
producing exactly one `FrameData` frame per call, and `Stream.Read` returning exactly
one queued frame's payload per call when the buffer is large enough — both guaranteed
by `internal/tunnel/stream.go`). Idle timeout: 60 seconds. This is what lets the
Android VPN relay DNS/QUIC/WebRTC and other UDP-based traffic, not just TCP.

---

## 8. Configuration (`internal/config/config.go`)

```yaml
# client.yaml
server: "yourdomain.com:8443"       # required
domain: "yourdomain.com"             # required - SNI + Host header; must match the server's real cert domain
fingerprint: "chrome131"             # default if unset
psk: "<64 hex chars>"                 # required - shared secret, one HKDF input alongside the ECDH secret
server_public_key: "<64 hex chars>"   # required - server's static X25519 public key
listen: "127.0.0.1:1080"              # default; desktop SOCKS5
listen_http: "127.0.0.1:1081"         # default; desktop HTTP CONNECT
pool_size: 4                          # default; parallel pooled connections
log_level: "info"                     # not actually read by any logger; plain `log` package used unconditionally
```

```yaml
# server.yaml
listen: ":8443"                       # VPN listener; any port - decoupled from ACME (see §6.2)
domain: "yourdomain.com"              # required - must have a public A/AAAA record pointing at this server
acme_email: ""                        # optional; Let's Encrypt contact address
acme_cache_dir: "/var/lib/phantom/acme"  # where the issued cert/key persist across restarts
private_key: "<64 hex chars>"         # required - server's static X25519 private key
psk: "<64 hex chars>"                 # required - must equal every client's psk
decoy_site_dir: ""                    # optional; static files to serve to unauthenticated connections. Empty = built-in placeholder page
log_level: "info"                     # unused, same caveat as client
```

`cmd/keygen` prints a matched `private_key`/`server_public_key`/`psk` triple. Unlike
the earlier prototype, every field here is used exactly as its name says: `psk` really
is a flat symmetric secret, `private_key`/`server_public_key` really are an X25519
keypair used in a real ECDH exchange, not window dressing around a de-facto symmetric
value.

---

## 9. Mobile core (`mobile/mobile.go`)

Unchanged architecture from the design established for the earlier prototype's Android
port: gVisor netstack (`gvisor.dev/gvisor/pkg/tcpip`) bridges Android's raw TUN file
descriptor to the same `session.Open`/`OpenUDP` API used everywhere else, `Protector`
lets the Android app exempt the tunnel's own outbound socket from its own VPN routing,
and `Start(configYAML, tunFD, mtu, protector)` parses config, dials, and starts
forwarding exactly like the desktop client does. Config field names changed to match
§8 (`cfg.GetPSK()`/`cfg.GetServerPublicKey()` instead of the old `GetPublicKey()`), and
`transport.TLSClientConfig` now carries `Domain`/`ServerPub` instead of `SNI`. See the
mobile.go source comments for the full gVisor wiring details (netstack setup, TCP/UDP
forwarders, `endpointTarget` address resolution) — that part of the design is
unaffected by anything in this document's §5-§8 changes, since it operates purely in
terms of the `Session`/`Stream` abstractions.

`gvisor.dev/gvisor` is pinned to the exact pseudo-version Tailscale ships in production
(`v0.0.0-20260224225140-573d5e7127a8`) rather than `@latest`, because the latest
upstream snapshot at development time had a broken test file that breaks plain
`go build`/`go list` package loading (filed upstream as google/gvisor#11699, unresolved)
and separately was missing Bazel-generated source files that the Tailscale-pinned
version has checked in.

## 10. Android app (`android/`, package `com.phantom.vpn`)

Kotlin + Jetpack Compose, dark/purple theme, a single large circular connect button as
the primary interaction (tap to connect/disconnect), a gear icon opening a config
screen where the full `client.yaml` text is pasted in (no separate YAML parser in
Kotlin — the raw text goes straight into the Go core via `Mobile.start`). `FileLog`
persists a plain-text log to the app's private storage with an in-app viewer/share
screen, since diagnosing a startup crash with no ADB access was a real problem
encountered during development. Config is stored via `EncryptedSharedPreferences` with
a plain-prefs fallback if Keystore access throws on a given device/ROM.

## 11. iOS portability path (not implemented in this repo)

Same as previously designed: `gomobile bind -target=ios ./mobile` would produce an
`.xcframework` from the identical source used for Android. The only platform-specific
piece is the link-layer glue in `mobile.go`'s `setupNetstack` (`fdbased.New` reading a
raw Android fd directly); iOS's `NEPacketTunnelProvider` would need gVisor's
callback-driven `channel.Endpoint` instead, since `NetworkExtension` doesn't hand out a
raw fd. Everything else — config parsing, the disguised handshake, TCP/UDP forwarders,
splicing — carries over unchanged.

## 12. Known gaps / residual risks

1. **Single VPS IP is a single point of failure.** CDN fronting (the standard fix for
   this — terminating the outer TLS at real, hard-to-block infrastructure like
   Cloudflare) was explicitly declined for this deployment to avoid a third-party
   dependency. Blocking the server's IP still stops everything, regardless of how good
   the wire-level disguise is.
2. **No post-quantum key exchange.** Reality (the closest prior art) has started
   experimenting with hybrid X25519+ML-KEM-768; this project doesn't attempt that.
3. **No ICMP support in the mobile tunnel** — only TCP and UDP transport protocols are
   registered with the gVisor stack, so ping through the Android VPN won't work
   end-to-end.
4. **Semi-static ECDH, not fully ephemeral-ephemeral** — see the forward-secrecy caveat
   in §5.1: a future compromise of the server's long-term private key combined with
   recorded traffic could still decrypt past sessions, same limitation Reality has.
5. **`FrameSettings`/`FramePadding` frame types and the entire in-band `FrameAuth` path
   in `multiplexer.go` are dead code**, kept only to avoid modifying a ported file —
   see §4.3. Not a security risk (they're simply never triggered), but worth knowing
   about if extending the multiplexer later.
