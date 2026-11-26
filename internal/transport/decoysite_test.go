package transport

import (
	"bufio"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func siteDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func get(t *testing.T, d *DecoySite, method, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, "https://site.example"+path, nil)
	var buf bytes.Buffer
	d.Serve(&buf, req)
	resp, err := http.ReadResponse(bufio.NewReader(&buf), req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	var b bytes.Buffer
	b.ReadFrom(r.Body)
	return b.String()
}

func TestDecoySitePrettyPathsAndOwn404(t *testing.T) {
	d := NewDecoySite(siteDir(t, map[string]string{
		"index.html":      "home",
		"about.html":      "about",
		"blog/index.html": "blog",
		"404.html":        "own 404",
	}))
	for path, want := range map[string]string{"/": "home", "/about": "about", "/about.html": "about", "/blog/": "blog", "/blog": "blog"} {
		r := get(t, d, "GET", path)
		if r.StatusCode != 200 || body(t, r) != want {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		if r.Header.Get("Last-Modified") == "" {
			t.Fatalf("%s: no Last-Modified", path)
		}
	}
	r := get(t, d, "GET", "/nope")
	if r.StatusCode != 404 || body(t, r) != "own 404" {
		t.Fatalf("missing page: %d", r.StatusCode)
	}
	if r := get(t, d, "GET", "/../../etc/passwd"); r.StatusCode != 404 {
		t.Fatalf("traversal: %d", r.StatusCode)
	}
}

func TestDecoyHeadHasNoBody(t *testing.T) {
	d := NewDecoySite(siteDir(t, map[string]string{"index.html": "home page"}))
	r := get(t, d, "HEAD", "/")
	if r.StatusCode != 200 || r.ContentLength != int64(len("home page")) {
		t.Fatalf("HEAD: %d len %d", r.StatusCode, r.ContentLength)
	}
	req, _ := http.NewRequest("HEAD", "https://site.example/", nil)
	var raw bytes.Buffer
	d.Serve(&raw, req)
	if strings.Contains(raw.String(), "home page") {
		t.Fatal("HEAD sent a body")
	}
}

func TestDecoyBuiltInWithoutDir(t *testing.T) {
	d := NewDecoySite("")
	if r := get(t, d, "GET", "/"); r.StatusCode != 200 || !strings.Contains(body(t, r), "<html") {
		t.Fatal("built-in page missing")
	}
}
