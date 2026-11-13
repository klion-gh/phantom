package netcheck

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// server answers one request on c with reply (nothing at all if reply is "").
func server(t *testing.T, c net.Conn, reply string) {
	t.Helper()
	go func() {
		defer c.Close()
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		if reply == "" {
			time.Sleep(time.Second)
			return
		}
		io.WriteString(c, reply)
	}()
}

func TestHTTPAcceptsAnyStatusLine(t *testing.T) {
	a, b := net.Pipe()
	server(t, b, "HTTP/1.1 204 No Content\r\n\r\n")
	if err := HTTP(context.Background(), a, TunnelTargets[0]); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRejectsGarbage(t *testing.T) {
	a, b := net.Pipe()
	server(t, b, "SSH-2.0-nope\r\n")
	if err := HTTP(context.Background(), a, TunnelTargets[0]); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err=%v, want ErrNoAnswer", err)
	}
}

func TestHTTPGivesUpOnSilence(t *testing.T) {
	a, b := net.Pipe()
	server(t, b, "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := HTTP(ctx, a, TunnelTargets[0]); err == nil {
		t.Fatal("silence counted as an answer")
	}
	if time.Since(start) > 700*time.Millisecond {
		t.Fatal("did not give up when the context ended")
	}
}

func TestThroughFallsBackToTheSecondTarget(t *testing.T) {
	var asked []string
	_, err := Through(context.Background(), func(addr string) (io.ReadWriteCloser, error) {
		asked = append(asked, addr)
		if addr == TunnelTargets[0].Addr {
			return nil, errors.New("refused")
		}
		a, b := net.Pipe()
		server(t, b, "HTTP/1.1 204 No Content\r\n\r\n")
		return a, nil
	})
	if err != nil || len(asked) != 2 {
		t.Fatalf("err=%v asked=%v", err, asked)
	}
}

type fakeDialer struct{ ok string }

func (d fakeDialer) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	if addr == d.ok {
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDirect(t *testing.T) {
	if err := Direct(context.Background(), fakeDialer{ok: DirectTargets[2]}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Direct(ctx, fakeDialer{}); err == nil || !strings.Contains(err.Error(), "without the VPN") {
		t.Fatalf("err=%v", err)
	}
}
