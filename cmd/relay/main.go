// Command relay is a Phantom relay entry point: it accepts a client's TLS
// connection, reads the SNI from its ClientHello and passes the connection on
// to that domain's Phantom server, unchanged. See internal/relay.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"phantom/internal/logx"
	"phantom/internal/relay"
)

func main() {
	configPath := flag.String("config", "relay.yaml", "path to config file")
	flag.Parse()

	data, err := os.ReadFile(*configPath)
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("Failed to read config: %v", err)
	}
	cfg, err := relay.ParseConfig(data)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	logx.SetLevel(cfg.LogLevel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[relay] shutting down...")
		cancel()
	}()

	allow := "any domain"
	if len(cfg.Allow) > 0 {
		allow = strings.Join(cfg.Allow, ", ")
	}
	log.Printf("[relay] forwarding to port %d of: %s", cfg.TargetPort, allow)

	r := relay.New(cfg)
	var wg sync.WaitGroup
	for _, addr := range cfg.Listen {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("Failed to listen on %s: %v", addr, err)
		}
		log.Printf("[relay] listening on %s", addr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.Serve(ctx, ln); err != nil {
				log.Printf("[relay] %s: %v", addr, err)
				cancel()
			}
		}()
	}
	wg.Wait()
}
