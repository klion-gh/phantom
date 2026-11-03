// flowlab records what a DPI box on the path sees of two kinds of traffic -
// a browser's own HTTPS connections, and the Phantom tunnel carrying the same
// browsing - so that analyze.py can train a classifier and say how far apart
// the two are, and on which features.
//
// It is a local HTTP CONNECT proxy for the browser. In -mode direct each
// CONNECT is dialed straight to the site; in -mode phantom it is opened as a
// stream inside a Phantom session. Either way the TCP connection that leaves
// the machine is wrapped, and every read and write on it is logged as one
// JSON line: connection id, time since it opened, direction, byte count.
// That is the encrypted traffic as the path sees it, minus how the kernel cut
// it into segments - and it needs no packet capture, so no administrator.
//
// With -iface the outgoing sockets are bound to that interface, so a Phantom
// tunnel already running on the machine doesn't end up inside the
// measurement.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"phantom/internal/config"
	"phantom/internal/transport"
	"phantom/internal/tunnel"
)

func main() {
	mode := flag.String("mode", "direct", "direct: dial sites straight; phantom: carry them through a Phantom session")
	listen := flag.String("listen", "127.0.0.1:18080", "HTTP CONNECT proxy address for the browser")
	out := flag.String("out", "flows.jsonl", "file to append the recording to")
	label := flag.String("label", "", "free-form label stored with every connection (site, scenario)")
	domain := flag.String("domain", "", "phantom mode: the saved config whose domain this is")
	configsPath := flag.String("configs", "", "phantom mode: the app's configs.json (default: the app's own)")
	iface := flag.Int("iface", 0, "bind outgoing sockets to this interface index (0: follow the routing table)")
	conns := flag.Int("conns", 0, "phantom mode: pool size override (0: the config's own)")
	flag.Parse()

	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("open %s: %v", *out, err)
	}
	rec := newRecorder(f, *mode, *label)
	defer rec.close()

	var dial func(target string) (io.ReadWriteCloser, error)
	switch *mode {
	case "direct":
		d := &net.Dialer{Timeout: 10 * time.Second, Control: bindControl(*iface)}
		dial = func(target string) (io.ReadWriteCloser, error) {
			c, err := d.Dial("tcp", target)
			if err != nil {
				return nil, err
			}
			return rec.wrap(c, target), nil
		}
	case "phantom":
		session, err := phantomSession(rec, *domain, *configsPath, *iface, *conns)
		if err != nil {
			log.Fatalf("phantom: %v", err)
		}
		dial = func(target string) (io.ReadWriteCloser, error) { return session.Open(target) }
	default:
		log.Fatalf("unknown -mode %q", *mode)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("flowlab %s on %s -> %s", *mode, *listen, *out)
	go func() {
		// Flush on a timer as well as on close: the driver kills this
		// process between runs, and a kill skips deferred calls.
		for range time.Tick(time.Second) {
			rec.flush()
		}
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatalf("accept: %v", err)
		}
		go handle(c, dial)
	}
}

// phantomSession builds a session the way cmd/client does, with the raw TCP
// connection under the tunnel wrapped by the recorder.
func phantomSession(rec *recorder, domain, configsPath string, iface, conns int) (*tunnel.Session, error) {
	yaml, err := findConfig(domain, configsPath)
	if err != nil {
		return nil, err
	}
	cfg, err := config.ParseClientConfig([]byte(yaml))
	if err != nil {
		return nil, err
	}
	psk, err := cfg.GetPSK()
	if err != nil {
		return nil, err
	}
	pub, err := cfg.GetServerPublicKey()
	if err != nil {
		return nil, err
	}
	tlsCfg := &transport.TLSClientConfig{
		Domain:      cfg.Domain,
		Fingerprint: cfg.Fingerprint,
		ServerAddr:  cfg.Server,
		ServerAddrs: cfg.ServerList(),
		PSK:         psk,
		ServerPub:   pub,
		ProtectFD:   bindFD(iface),
		WrapConn:    func(c net.Conn) net.Conn { return rec.wrap(c, "phantom:"+cfg.Domain) },
	}
	size := cfg.PoolSize
	if conns > 0 {
		size = conns
	}
	pool := transport.NewConnPool(size, transport.NewFailoverDialer(tlsCfg))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mux, err := pool.Get(ctx)
	if err != nil {
		return nil, err
	}
	return tunnel.NewSessionFromPool(mux, pool.Pick), nil
}

// findConfig returns the yaml of the saved config for domain. It is never
// printed: it carries the PSK.
func findConfig(domain, path string) (string, error) {
	if domain == "" {
		return "", fmt.Errorf("-domain is required in phantom mode")
	}
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, "Phantom", "configs.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var saved []struct {
		Yaml string `json:"yaml"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return "", err
	}
	for _, s := range saved {
		if c, err := config.ParseClientConfig([]byte(s.Yaml)); err == nil && strings.EqualFold(c.Domain, domain) {
			return s.Yaml, nil
		}
	}
	return "", fmt.Errorf("no saved config for %s in %s", domain, path)
}

// handle serves one browser connection: a CONNECT, then bytes both ways.
func handle(c net.Conn, dial func(string) (io.ReadWriteCloser, error)) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		// Recording HTTPS only: what a browser sends as plain HTTP is not
		// what this measures.
		io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	up, err := dial(req.Host)
	if err != nil {
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	if n := br.Buffered(); n > 0 {
		buf, _ := br.Peek(n)
		up.Write(buf)
		br.Discard(n)
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); done <- struct{}{} }()
	go func() { io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// recorder appends one JSON line per event to the output file.
type recorder struct {
	mu    sync.Mutex
	w     *bufio.Writer
	f     *os.File
	mode  string
	label string
	next  int64
	run   string
}

func newRecorder(f *os.File, mode, label string) *recorder {
	return &recorder{w: bufio.NewWriterSize(f, 1<<16), f: f, mode: mode, label: label,
		run: fmt.Sprintf("%d", time.Now().UnixNano())}
}

func (r *recorder) emit(v map[string]any) {
	b, _ := json.Marshal(v)
	r.mu.Lock()
	r.w.Write(b)
	r.w.WriteByte('\n')
	r.mu.Unlock()
}

func (r *recorder) flush() {
	r.mu.Lock()
	r.w.Flush()
	r.mu.Unlock()
}

func (r *recorder) close() {
	r.flush()
	r.f.Close()
}

func (r *recorder) wrap(c net.Conn, target string) net.Conn {
	r.mu.Lock()
	r.next++
	id := fmt.Sprintf("%s-%d", r.run, r.next)
	r.mu.Unlock()
	rc := &recConn{Conn: c, r: r, id: id, start: time.Now()}
	r.emit(map[string]any{"ev": "open", "conn": id, "mode": r.mode, "label": r.label, "target": target, "t": rc.start.UnixNano()})
	return rc
}

type recConn struct {
	net.Conn
	r      *recorder
	id     string
	start  time.Time
	closed sync.Once
	up     recordParser
	down   recordParser
}

// Besides each read and write ("io" - how the bytes happened to be chunked
// by whichever program was on this end), the TLS records inside them are
// logged ("rec"): a record's 5-byte header is never encrypted, so its type
// and length - and when it arrived - are exactly what a DPI box on the path
// reads, whatever the socket's chunking. The analysis uses records.
func (c *recConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		t := time.Since(c.start).Microseconds()
		c.r.emit(map[string]any{"ev": "io", "conn": c.id, "t": t, "dir": "down", "n": n})
		c.down.feed(p[:n], func(typ byte, length int) {
			c.r.emit(map[string]any{"ev": "rec", "conn": c.id, "t": t, "dir": "down", "n": length, "type": typ})
		})
	}
	return n, err
}

func (c *recConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		t := time.Since(c.start).Microseconds()
		c.r.emit(map[string]any{"ev": "io", "conn": c.id, "t": t, "dir": "up", "n": n})
		c.up.feed(p[:n], func(typ byte, length int) {
			c.r.emit(map[string]any{"ev": "rec", "conn": c.id, "t": t, "dir": "up", "n": length, "type": typ})
		})
	}
	return n, err
}

// recordParser follows TLS record framing across arbitrary chunks of one
// direction of a connection. A record is reported when its header is
// complete; its timestamp is that of the chunk the header arrived in.
type recordParser struct {
	hdr    [5]byte
	have   int // header bytes collected so far
	remain int // body bytes still to skip
	broken bool
}

func (rp *recordParser) feed(b []byte, emit func(typ byte, length int)) {
	for len(b) > 0 && !rp.broken {
		if rp.remain > 0 {
			k := rp.remain
			if k > len(b) {
				k = len(b)
			}
			rp.remain -= k
			b = b[k:]
			continue
		}
		k := copy(rp.hdr[rp.have:], b)
		rp.have += k
		b = b[k:]
		if rp.have < 5 {
			return
		}
		rp.have = 0
		typ := rp.hdr[0]
		length := int(rp.hdr[3])<<8 | int(rp.hdr[4])
		if typ < 20 || typ > 24 || rp.hdr[1] != 3 {
			rp.broken = true // not TLS (or lost sync): stop rather than log garbage
			return
		}
		emit(typ, length)
		rp.remain = length
	}
}

func (c *recConn) Close() error {
	c.closed.Do(func() {
		c.r.emit(map[string]any{"ev": "close", "conn": c.id, "t": time.Since(c.start).Microseconds()})
	})
	return c.Conn.Close()
}
