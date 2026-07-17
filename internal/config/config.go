package config

import (
	"encoding/hex"
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

type ClientConfig struct {
	Server          string   `yaml:"server"`            // VPS address:port, e.g. "1.2.3.4:443". Primary endpoint; see servers for failover.
	Servers         []string `yaml:"servers"`           // optional list of address:port endpoints, all serving the same domain/cert/psk. Tried with failover (see ServerList/transport.NewFailoverDialer); if set, takes precedence over server. Blocking one IP/port no longer kills everything.
	Domain          string   `yaml:"domain"`            // real domain the server has a CA-signed cert for; used as SNI and as the Host header in the disguised handshake
	Fingerprint     string   `yaml:"fingerprint"`       // uTLS ClientHello mimicry: chrome133/chrome131 (post-quantum X25519MLKEM768 key share)/chrome120/firefox120/safari16
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
	// Per-IP anti-enumeration throttle on auth-handshake attempts (see
	// internal/transport/ratelimit.go). Both 0 = use defaults (2/s sustained,
	// 60 burst); an over-budget IP is served the decoy without an auth attempt,
	// not dropped, so throttling stays invisible.
	HandshakeRatePerSec float64 `yaml:"handshake_rate_per_sec"`
	HandshakeBurst      float64 `yaml:"handshake_burst"`
	LogLevel            string  `yaml:"log_level"` // debug|info|warn|error; controls internal/logx (server-side logging)
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
		Fingerprint: "chrome133",
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
	if len(c.Servers) > 0 {
		return c.Servers
	}
	if c.Server != "" {
		return []string{c.Server}
	}
	return nil
}

func (c *ClientConfig) Validate() error {
	if len(c.ServerList()) == 0 {
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
	return nil
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
