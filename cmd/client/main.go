package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"phantom/internal/config"
	"phantom/internal/protocol"
	"phantom/internal/proxy"
	"phantom/internal/transport"
	"phantom/internal/tunnel"
)

func main() {
	configPath := flag.String("config", "client.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.LoadClientConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("[client] connecting to %s (domain: %s)", cfg.Server, cfg.Domain)

	psk, err := cfg.GetPSK()
	if err != nil {
		log.Fatalf("Invalid psk: %v", err)
	}
	serverPub, err := cfg.GetServerPublicKey()
	if err != nil {
		log.Fatalf("Invalid server_public_key: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[client] shutting down...")
		cancel()
		os.Exit(0)
	}()

	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		PSK:         psk,
		ServerPub:   serverPub,
	}

	pool := transport.NewConnPool(cfg.PoolSize, 12*1024, func(ctx context.Context) (net.Conn, *protocol.SessionCrypto, error) {
		return transport.Dial(ctx, tlsCfg)
	})
	defer pool.Close()

	mux, err := pool.Get(ctx)
	if err != nil {
		log.Fatalf("Failed to get connection: %v", err)
	}

	session := tunnel.NewSessionFromMux(mux)

	httpAddr := cfg.ListenHTTP
	if httpAddr == "" {
		httpAddr = "127.0.0.1:1081"
	}

	log.Printf("[client] connected")
	log.Printf("[client] SOCKS5 on %s", cfg.Listen)
	log.Printf("[client] HTTP   on %s", httpAddr)

	httpProxy := proxy.NewHTTPProxyServer(httpAddr, session)
	go func() {
		if err := httpProxy.Start(); err != nil {
			log.Fatalf("HTTP proxy error: %v", err)
		}
	}()

	socks5 := proxy.NewSOCKS5Server(cfg.Listen, session)
	if err := socks5.Start(); err != nil {
		log.Fatalf("SOCKS5 server error: %v", err)
	}
}
