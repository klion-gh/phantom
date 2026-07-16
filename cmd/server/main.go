package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/curve25519"

	"phantom/internal/config"
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

	serverCfg := &transport.TLSServerConfig{
		ListenAddr:   cfg.Listen,
		Domain:       cfg.Domain,
		ACMEEmail:    cfg.ACMEEmail,
		ACMECacheDir: cfg.ACMECacheDir,
		CertFile:     cfg.CertFile,
		KeyFile:      cfg.KeyFile,
		PSK:          psk,
		ServerPriv:   serverPriv,
		ServerPub:    serverPub,
		Decoy:        transport.NewDecoySite(cfg.DecoySiteDir),
	}

	direct := proxy.NewDirectOutbound(30 * time.Second)

	err = transport.ListenAndServe(ctx, serverCfg, func(conn net.Conn, crypto *protocol.SessionCrypto) {
		// Authentication and per-session key derivation already happened in
		// internal/handshake before this callback ever runs, so - unlike v1 -
		// there is no in-band auth frame exchange here.
		mux := tunnel.NewMultiplexer(conn, crypto)
		session := tunnel.NewSessionFromMux(mux)
		defer session.Close()

		log.Printf("[server] new session established")

		session.HandleIncoming(ctx, func(stream *tunnel.Stream) {
			direct.HandleStream(stream)
		})
	})

	if err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
