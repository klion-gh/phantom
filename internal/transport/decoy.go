package transport

import (
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DecoySite is what a connection gets instead of a dropped/reset connection
// when internal/handshake.ServerHandshake can't authenticate it (wrong or
// missing embedded auth, or not even a websocket-upgrade-shaped request at
// all). A human or automated prober sees an ordinary small website served
// over the server's real certificate - never a timeout, never a TLS alert.
//
// This replaces v1's masquerade.go, which defined exactly this idea
// (MasqueradeServer/ServeOnConnection) but never wired it into the actual
// server accept loop - unauthenticated connections just got a 10s timeout.
type DecoySite struct {
	dir string
}

// NewDecoySite serves static files from dir if set, falling back to the
// built-in placeholder page for any path it can't resolve. An empty dir
// serves the built-in page for every request.
func NewDecoySite(dir string) *DecoySite {
	return &DecoySite{dir: dir}
}

// ServeDefault writes the decoy's root response without a parsed request -
// used on the rate-limited path (see ratelimit.go), where the connection is
// answered like an ordinary homepage visit without spending an auth attempt on
// it, so the request bytes are never parsed in the first place.
func (d *DecoySite) ServeDefault(w io.Writer) {
	body, contentType, status := d.resolve("/")
	d.write(w, body, contentType, status)
}

// Serve writes a complete HTTP response for req directly to w. req is the
// *http.Request already parsed by handshake.ServerHandshake from the raw
// connection - this does not go through Go's http.Server machinery since the
// request has already been consumed from the socket.
func (d *DecoySite) Serve(w io.Writer, req *http.Request) {
	body, contentType, status := d.resolve(req.URL.Path)
	d.write(w, body, contentType, status)
}

func (d *DecoySite) write(w io.Writer, body []byte, contentType string, status int) {

	resp := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\n"+
			"Content-Type: %s\r\n"+
			"Content-Length: %d\r\n"+
			"Connection: close\r\n"+
			"\r\n",
		status, http.StatusText(status), contentType, len(body),
	)
	if _, err := w.Write([]byte(resp)); err != nil {
		log.Printf("[decoy] write response failed: %v", err)
		return
	}
	if _, err := w.Write(body); err != nil {
		log.Printf("[decoy] write body failed: %v", err)
	}
}

func (d *DecoySite) resolve(reqPath string) (body []byte, contentType string, status int) {
	if d.dir != "" {
		if data, ct, ok := d.readStatic(reqPath); ok {
			return data, ct, http.StatusOK
		}
	}

	if reqPath == "/" || reqPath == "" {
		return []byte(defaultDecoyHTML), "text/html; charset=utf-8", http.StatusOK
	}
	if reqPath == "/robots.txt" {
		return []byte("User-agent: *\nDisallow:\n"), "text/plain; charset=utf-8", http.StatusOK
	}
	return []byte(defaultDecoy404HTML), "text/html; charset=utf-8", http.StatusNotFound
}

func (d *DecoySite) readStatic(reqPath string) (data []byte, contentType string, ok bool) {
	clean := filepath.Clean(reqPath)
	if clean == "/" || clean == "." || clean == "" {
		clean = "/index.html"
	}

	full := filepath.Join(d.dir, clean)
	rel, err := filepath.Rel(d.dir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, "", false // path traversal attempt - refuse
	}

	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, "", false
	}

	ct := mime.TypeByExtension(filepath.Ext(full))
	if ct == "" {
		ct = "application/octet-stream"
	}
	return raw, ct, true
}

const defaultDecoyHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Welcome to Example Corp</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f5f5f5; color: #333; }
        .header { background: #2c3e50; color: white; padding: 1rem 2rem; }
        .hero { background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); color: white; padding: 4rem 2rem; text-align: center; }
        .content { max-width: 800px; margin: 2rem auto; padding: 0 1rem; }
        .card { background: white; border-radius: 8px; padding: 1.5rem; margin: 1rem 0; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .footer { background: #2c3e50; color: white; text-align: center; padding: 1rem; margin-top: 2rem; }
    </style>
</head>
<body>
    <header class="header"><h1>Example Corp</h1></header>
    <section class="hero">
        <h2>Welcome to Our Platform</h2>
        <p>Building the future of enterprise solutions</p>
    </section>
    <main class="content">
        <div class="card">
            <h3>About Us</h3>
            <p>We provide innovative solutions for modern businesses.</p>
        </div>
        <div class="card">
            <h3>Contact</h3>
            <p>Email: info@example.com</p>
        </div>
    </main>
    <footer class="footer"><p>&copy; 2026 Example Corp. All rights reserved.</p></footer>
</body>
</html>`

const defaultDecoy404HTML = `<!DOCTYPE html>
<html><head><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body>
</html>`
