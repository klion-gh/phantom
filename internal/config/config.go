package config

import (
	"encoding/hex"
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

type ClientConfig struct {
	Server          string `yaml:"server"`            // VPS address:port, e.g. "1.2.3.4:443"
	Domain          string `yaml:"domain"`            // real domain the server has a CA-signed cert for; used as SNI and as the Host header in the disguised handshake
	Fingerprint     string `yaml:"fingerprint"`       // uTLS ClientHello mimicry: chrome133/chrome131 (post-quantum X25519MLKEM768 key share)/chrome120/firefox120/safari16
	PSK             string `yaml:"psk"`               // shared secret (hex, 32 bytes) - one of several HKDF inputs, must match server's psk
	ServerPublicKey string `yaml:"server_public_key"` // server's static X25519 public key (hex, 32 bytes) - for real per-session ECDH
	Listen          string `yaml:"listen"`            // SOCKS5 proxy, desktop only
	ListenHTTP      string `yaml:"listen_http"`       // HTTP CONNECT proxy, desktop only
	PoolSize        int    `yaml:"pool_size"`
	LogLevel        string `yaml:"log_level"`
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
	LogLevel     string `yaml:"log_level"`
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

func (c *ClientConfig) Validate() error {
	if c.Server == "" {
		return errors.New("server is required")
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
