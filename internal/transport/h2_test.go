package transport

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"phantom/internal/handshake"
	"phantom/internal/protocol"
	"phantom/internal/proxy"
	"phantom/internal/tunnel"
)

// h2TestListener is a TLS listener offering what the real server offers.
func h2TestListener(t *testing.T, domain string) net.Listener {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{generateThrowawayCert(t, domain)},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   serverALPN,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// A browser negotiates h2 and gets the decoy site over HTTP/2 - not an
// HTTP/1.1 answer on an h2 connection, which a prober would read as broken.
func TestH2ProbeGetsDecoyOverHTTP2(t *testing.T) {
	serverPriv, serverPub := genX25519Keypair(t)
	ln := h2TestListener(t, "phantom.test")
	cfg := &TLSServerConfig{PSK: []byte("integration-test-psk-0123456789"), ServerPriv: serverPriv, ServerPub: serverPub, Decoy: NewDecoySite("")}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleConnection(c, cfg, func(net.Conn, *protocol.SessionCrypto) {
				t.Error("handler invoked for an unauthenticated probe")
			})
		}
	}()

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: "phantom.test"},
		ForceAttemptHTTP2: true,
	}}
	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("decoy answered over %s, want HTTP/2", resp.Proto)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Example Corp")) {
		t.Fatalf("got %d %q, want the decoy page", resp.StatusCode, body)
	}
	if resp.Header.Get("Date") == "" {
		t.Error("decoy over HTTP/2 sent no Date header")
	}

	resp404, err := client.Get("https://" + ln.Addr().String() + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: got %d, want 404", resp404.StatusCode)
	}
}

// A Phantom client up to 2.2 offers h2 (its mimicked ClientHello does) and
// gets it negotiated, but speaks HTTP/1.1 - it must still authenticate and
// tunnel exactly as before.
func TestHTTP1ClientStillWorksWhenH2Negotiated(t *testing.T) {
	serverPriv, serverPub := genX25519Keypair(t)
	psk := []byte("integration-test-psk-0123456789")
	domain := "phantom.test"
	echoAddr := startEchoServer(t)
	ln := h2TestListener(t, domain)
	cfg := &TLSServerConfig{PSK: psk, ServerPriv: serverPriv, ServerPub: serverPub, Decoy: NewDecoySite("")}

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		handleConnection(c, cfg, func(conn net.Conn, crypto *protocol.SessionCrypto) {
			session := tunnel.NewSessionFromMux(tunnel.NewMultiplexer(conn, crypto))
			defer session.Close()
			direct := proxy.NewDirectOutbound(5 * time.Second)
			direct.AllowLocalTargets(true)
			stream, err := session.Accept()
			if err != nil {
				return
			}
			direct.HandleStream(stream)
		})
	}()

	raw, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		ServerName: domain, InsecureSkipVerify: true, MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if p := raw.ConnectionState().NegotiatedProtocol; p != "h2" {
		t.Fatalf("negotiated %q, want h2 - the case under test", p)
	}
	exporter := func(label string, context []byte, length int) ([]byte, error) {
		st := raw.ConnectionState()
		return st.ExportKeyingMaterial(label, context, length)
	}
	crypto, err := handshake.ClientHandshake(raw, domain, psk, serverPub, exporter)
	if err != nil {
		t.Fatalf("handshake over negotiated h2: %v", err)
	}
	session := tunnel.NewSessionFromMux(tunnel.NewMultiplexer(raw, crypto))
	defer session.Close()
	stream, err := session.Open(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	payload := []byte("old client, new server")
	stream.Write(payload)
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil || !bytes.Equal(buf, payload) {
		t.Fatalf("echo: %q, %v", buf, err)
	}
}
