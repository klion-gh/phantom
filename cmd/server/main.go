package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/curve25519"

	"phantom/internal/config"
	"phantom/internal/logx"
	"phantom/internal/protocol"
	"phantom/internal/proxy"
	"phantom/internal/transport"
	"phantom/internal/tunnel"
)

func main() {
	configPath := flag.String("config", "server.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.LoadServerConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("[server] starting on %s (domain=%s)", cfg.Listen, cfg.Domain)

	serverPriv, err := cfg.GetPrivateKey()
	if err != nil {
		log.Fatalf("Invalid private_key: %v", err)
	}
	serverPub, err := curve25519.X25519(serverPriv, curve25519.Basepoint)
	if err != nil {
		log.Fatalf("Failed to derive server public key: %v", err)
	}

	psk, err := cfg.GetPSK()
	if err != nil {
		log.Fatalf("Invalid psk: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[server] shutting down...")
		cancel()
	}()

	logx.SetLevel(cfg.LogLevel)

	serverCfg := &transport.TLSServerConfig{
		ListenAddr:          cfg.Listen,
		Domain:              cfg.Domain,
		ACMEEmail:           cfg.ACMEEmail,
		ACMECacheDir:        cfg.ACMECacheDir,
		CertFile:            cfg.CertFile,
		KeyFile:             cfg.KeyFile,
		PSK:                 psk,
		ServerPriv:          serverPriv,
		ServerPub:           serverPub,
		Decoy:               transport.NewDecoySite(cfg.DecoySiteDir),
		HandshakeRatePerSec: cfg.HandshakeRatePerSec,
		HandshakeBurst:      cfg.HandshakeBurst,
	}

	go logMetricsLoop(ctx)

	direct := proxy.NewDirectOutbound(30 * time.Second)

	err = transport.ListenAndServe(ctx, serverCfg, func(conn net.Conn, crypto *protocol.SessionCrypto) {
		// Authentication and per-session key derivation already happened in
		// internal/handshake before this callback ever runs, so - unlike v1 -
		// there is no in-band auth frame exchange here.
		mux := tunnel.NewMultiplexer(conn, crypto)
		session := tunnel.NewSessionFromMux(mux)
		defer session.Close()

		// Through logx so log_level actually governs it. It used to bypass the
		// level filter entirely, which meant an operator who turned logging down
		// still got one line per connection - and with the GUI clients' ~6s ping
		// cadence that is thousands of lines a day per user, on the one machine
		// where the less written down the better.
		logx.Infof("[server] new session established")

		session.HandleIncoming(ctx, func(stream *tunnel.Stream) {
			direct.HandleStream(stream)
		})
	})

	// A cancelled context is how a normal shutdown ends (SIGTERM -> cancel ->
	// ListenAndServe returns ctx.Err()), so it is not an error. Treating it as
	// one made every `systemctl stop` exit non-zero, which systemd reports as
	// "Failed with result 'exit-code'" and, combined with the shutdown taking a
	// while, as a timeout - so an ordinary restart looked exactly like a crash in
	// the journal, on the machine where the journal is the only thing to go on.
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Server error: %v", err)
	}
	log.Println("[server] stopped")
}

// logMetricsLoop prints an aggregate connection-outcome summary every few
// minutes (see internal/transport/metrics.go). A spike in decoy/non-HTTP/
// rate-limited relative to ok is the server's earliest sign of scanning or
// probing. Always logged at info so it shows up regardless of log_level's
// per-connection verbosity.
func logMetricsLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m := transport.MetricsSnapshot()
			logx.Infof("[metrics] active=%d ok=%d decoy=%d non_http=%d rate_limited=%d",
				m.ActiveNow, m.HandshakeOK, m.DecoyHits, m.NonHTTP, m.RateLimited)
		}
	}
}
