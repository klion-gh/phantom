package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Stopping the server must not wait for the next client to connect:
// systemd's stop timeout used to expire on every restart.
func TestListenAndServeStopsWithoutAClient(t *testing.T) {
	cert := generateThrowawayCert(t, "phantom.test")
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600)
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ListenAndServe(ctx, &TLSServerConfig{ListenAddr: "127.0.0.1:0", CertFile: certFile, KeyFile: keyFile,
			Decoy: NewDecoySite("")}, nil)
	}()
	time.Sleep(200 * time.Millisecond) // listening, nobody connecting
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ListenAndServe returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe kept waiting for a client after its context ended")
	}
}
