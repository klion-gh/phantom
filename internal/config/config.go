package config

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type ClientConfig struct {
	Server          string   `yaml:"server"`            // VPS address:port, e.g. "1.2.3.4:443". Primary endpoint; see servers for failover.
	Servers         []string `yaml:"servers"`           // optional list of address:port endpoints, all serving the same domain/cert/psk. Tried with failover (see ServerList/transport.NewFailoverDialer); if set, takes precedence over server. Blocking one IP/port no longer kills everything.
	Domain          string   `yaml:"domain"`            // real domain the server has a CA-signed cert for; used as SNI and as the Host header in the disguised handshake
	Fingerprint     string   `yaml:"fingerprint"`       // uTLS ClientHello mimicry: auto (default: Firefox)/firefox/edge/360/qq/chrome133/chrome131/chrome120/safari16 - see internal/transport/fingerprint.go
	PSK             string   `yaml:"psk"`               // shared secret (hex, 32 bytes) - one of several HKDF inputs, must match server's psk
	ServerPublicKey string   `yaml:"server_public_key"` // server's static X25519 public key (hex, 32 bytes) - for real per-session ECDH
	Listen          string   `yaml:"listen"`            // SOCKS5 proxy, desktop only
	ListenHTTP      string   `yaml:"listen_http"`       // HTTP CONNECT proxy, desktop only
	PoolSize        int      `yaml:"pool_size"`
	LogLevel        string   `yaml:"log_level"`
	// Optional cosmetic location label the GUI apps show for this server's tile
	// (the apps read these straight from the yaml). Deliberately operator-set, not
	// looked up from the IP - that lookup used to leak the server IP to a
	// third-party geo/flag service. country_code is a two-letter ISO code.
	Country     string `yaml:"country"`
	CountryCode string `yaml:"country_code"`
	// Optional relay "address:port" (cmd/relay): when set, every connection
	// goes to the relay instead of server/servers, with SNI still domain. The
	// relay reads that SNI and passes the bytes through to the real server
	// untouched - it holds no keys and decrypts nothing. The apps don't store
	// it in the config: they add it on the way in (WithRelay) from their own
	// "use a relay" setting, so one relay serves every saved config.
	Relay string `yaml:"relay"`
	// Optional label of the server's relay-facing name: with "msk", a
	// connection through the relay presents msk.<domain> - a name whose DNS
	// points at the relay, so name and address agree - instead of <domain>,
	// whose DNS points at the server. The server holds a certificate for it
	// (extra_domains); a server that doesn't yet is reached with <domain>.
	RelayPrefix string `yaml:"relay_prefix"`
}

// RelayDomain is the name presented through the relay - <relay_prefix>.<domain> -
// or "" when there is no relay or no prefix.
func (c *ClientConfig) RelayDomain() string {
	if c.Relay == "" || c.RelayPrefix == "" {
		return ""
	}
	return c.RelayPrefix + "." + c.Domain
}

type ServerConfig struct {
	Listen       string `yaml:"listen"`         // default ":443" - a VPN quietly listening on a nonstandard port is itself a minor tell
	Domain       string `yaml:"domain"`         // real domain this server has (or will obtain) a CA-signed cert for
	ACMEEmail    string `yaml:"acme_email"`     // contact email for Let's Encrypt registration (optional but recommended)
	ACMECacheDir string `yaml:"acme_cache_dir"` // where the issued cert/key gets cached across restarts
	CertFile     string `yaml:"cert_file"`      // static cert+key pair instead of ACME (e.g. an existing certbot certificate) - both this and key_file must be set together
	KeyFile      string `yaml:"key_file"`       // needed when this server can't dedicate port 80 to ACME's HTTP-01 challenge (shared box already running its own web server)
	PrivateKey   string `yaml:"private_key"`    // server's static X25519 private key (hex, 32 bytes) - for real per-session ECDH
	PSK          string `yaml:"psk"`            // shared secret (hex, 32 bytes), must match every client's psk
	DecoySiteDir string `yaml:"decoy_site_dir"` // directory of static files served to connections that fail/skip the embedded auth check; empty = built-in minimal page
	// More names this server holds a certificate for - typically the
	// subdomain a relay is reached under ("msk.vpn.example" pointing at the
	// relay), so a client coming through the relay presents a name whose DNS
	// matches the address it connects to. ACME only.
	ExtraDomains []string `yaml:"extra_domains"`
	// Per-IP anti-enumeration throttle on auth-handshake attempts (see
	// internal/transport/ratelimit.go). Both 0 = use defaults (2/s sustained,
	// 60 burst); an over-budget IP is served the decoy without an auth attempt,
	// not dropped, so throttling stays invisible.
	HandshakeRatePerSec float64 `yaml:"handshake_rate_per_sec"`
	HandshakeBurst      float64 `yaml:"handshake_burst"`
	// debug|info|warn|error; controls internal/logx (server-side logging).
	// Note that debug is the level that logs each stream's destination
	// host:port - i.e. turning it on starts recording what users visit, which
	// nothing else does. Leave it at info (the default) outside diagnosis.
	LogLevel string `yaml:"log_level"`
}

func LoadClientConfig(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseClientConfig(data)
}

// ParseClientConfig parses client config YAML from memory, with no file I/O -
// used by cmd/client (after reading the file) and by the mobile core, which
// receives the config as an in-memory string (imported/pasted client.yaml).
func ParseClientConfig(data []byte) (*ClientConfig, error) {
	cfg := &ClientConfig{
		Fingerprint: "auto",
		Listen:      "127.0.0.1:1080",
		PoolSize:    4,
		LogLevel:    "info",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ServerList returns the endpoints to try, newest-style servers list first,
// falling back to the single server field. All share the same domain/cert/psk;
// they're just alternative address:port pairs for failover.
func (c *ClientConfig) ServerList() []string {
	if c.Relay != "" {
		return []string{c.Relay}
	}
	return c.DirectServerList()
}

// DirectServerList is ServerList ignoring the relay: the server's own
// endpoints. Through a relay they are never dialed, but they still say where
// the server is (the IP a tile shows and looks the country up for).
func (c *ClientConfig) DirectServerList() []string {
	if len(c.Servers) > 0 {
		return c.Servers
	}
	if c.Server != "" {
		return []string{c.Server}
	}
	return nil
}

func (c *ClientConfig) Validate() error {
	if len(c.DirectServerList()) == 0 {
		return errors.New("server (or servers) is required")
	}
	if c.Domain == "" {
		return errors.New("domain is required")
	}
	if c.PSK == "" {
		return errors.New("psk is required")
	}
	if c.ServerPublicKey == "" {
		return errors.New("server_public_key is required")
	}
	if c.Relay != "" {
		if _, _, err := net.SplitHostPort(c.Relay); err != nil {
			return fmt.Errorf("relay must be address:port: %w", err)
		}
	}
	return nil
}

// WithRelay returns configYAML with its relay set to addr and its relay
// prefix to prefix, or with both removed when addr is empty. It edits the
// document rather than the text, so the rest of the config - comments, key
// order, quoting - comes back as it was.
func WithRelay(configYAML, addr, prefix string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(configYAML), &doc); err != nil {
		return "", err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("config is not a mapping")
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); {
		if k := m.Content[i].Value; k == "relay" || k == "relay_prefix" {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			continue
		}
		i += 2
	}
	add := func(k, v string) {
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: yaml.DoubleQuotedStyle})
	}
	if addr != "" {
		add("relay", addr)
		if prefix != "" {
			add("relay_prefix", prefix)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (c *ClientConfig) GetPSK() ([]byte, error) {
	return decodeKey(c.PSK)
}

func (c *ClientConfig) GetServerPublicKey() ([]byte, error) {
	return decodeKey(c.ServerPublicKey)
}

func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseServerConfig(data)
}

func ParseServerConfig(data []byte) (*ServerConfig, error) {
	cfg := &ServerConfig{
		Listen:       ":443",
		ACMECacheDir: "/var/lib/phantom/acme",
		LogLevel:     "info",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *ServerConfig) Validate() error {
	if s.Domain == "" {
		return errors.New("domain is required")
	}
	if s.PrivateKey == "" {
		return errors.New("private_key is required")
	}
	if s.PSK == "" {
		return errors.New("psk is required")
	}
	if (s.CertFile == "") != (s.KeyFile == "") {
		return errors.New("cert_file and key_file must be set together (or both left empty to use ACME)")
	}
	return nil
}

func (s *ServerConfig) GetPrivateKey() ([]byte, error) {
	return decodeKey(s.PrivateKey)
}

func (s *ServerConfig) GetPSK() ([]byte, error) {
	return decodeKey(s.PSK)
}

func decodeKey(hexStr string) ([]byte, error) {
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("key must be 32 bytes (64 hex characters)")
	}
	return b, nil
}

// NormalizeRelayPrefix checks a relay prefix as typed: one DNS label, lower
// case, dots around it dropped ("msk", " .MSK. " -> "msk"). "" stays "".
func NormalizeRelayPrefix(s string) (string, error) {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), "."))
	if s == "" {
		return "", nil
	}
	if len(s) > 63 {
		return "", errors.New("too long")
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' && i > 0 && i < len(s)-1
		if !ok {
			return "", fmt.Errorf("not a DNS label: %q", s)
		}
	}
	return s, nil
}

// NormalizeRelayAddr turns what a user typed as a relay address into the
// address:port WithRelay takes: surrounding space trimmed, and the relay's
// default port 443 added when none is given. An error means it isn't an
// address at all.
func NormalizeRelayAddr(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty")
	}
	if strings.ContainsAny(s, " \t/") {
		return "", errors.New("not an address")
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// No port: a bare host, or a bare IPv6 address.
		host, port = strings.Trim(s, "[]"), "443"
	}
	if host == "" {
		return "", errors.New("no host")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("bad port %q", port)
	}
	return net.JoinHostPort(host, port), nil
}
