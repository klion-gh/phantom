package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"phantom/internal/handshake"
)

func TestClassify(t *testing.T) {
	cases := map[string]error{
		"auth":    fmt.Errorf("handshake: %w", handshake.ErrAuthFailed),
		"dns":     errors.New(`resolve server address "example.org": lookup example.org: no such host`),
		"refused": errors.New("handshake: dial tcp 203.0.113.1:443: connectex: No connection could be made because the target machine actively refused it."),
		"reset":   errors.New("handshake: read tcp 10.0.0.2:5000->203.0.113.1:443: wsarecv: An existing connection was forcibly closed by the remote host."),
		"timeout": errors.New("handshake: dial tcp 203.0.113.1:443: i/o timeout"),
		"other":   errors.New("handshake: x509: certificate signed by unknown authority"),
	}
	for want, err := range cases {
		if got := classify(err); got != want {
			t.Errorf("classify(%q) = %q, want %q", err, got, want)
		}
	}
}

func TestAddConfigRefusesDuplicatesAndBadYaml(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "console.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{configsPath: filepath.Join(dir, "configs.json"), store: st}
	yaml := "server: \"203.0.113.10:443\"\ndomain: \"example.org\"\n" +
		"psk: \"" + strings.Repeat("ab", 32) + "\"\nserver_public_key: \"" + strings.Repeat("cd", 32) + "\"\n"

	c, err := a.addConfig(yaml)
	if err != nil {
		t.Fatalf("addConfig: %v", err)
	}
	if _, err := a.addConfig(yaml); !errors.Is(err, errDuplicate) {
		t.Fatalf("second add: got %v, want errDuplicate", err)
	}
	if _, err := a.addConfig("domain: example.org\n"); err == nil {
		t.Fatal("yaml without keys was accepted")
	}

	views, err := a.views()
	if err != nil || len(views) != 1 || views[0].Source != "console" || views[0].IP != "203.0.113.10" {
		t.Fatalf("views = %+v, %v", views, err)
	}
	if ok, err := st.deleteConfig(c.ID); !ok || err != nil {
		t.Fatalf("deleteConfig = %v, %v", ok, err)
	}
}

func TestNewsAPI(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "console.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := (&app{configsPath: filepath.Join(dir, "configs.json"), store: st}).routes()
	post := func(path, body string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if code := post("/api/news", `{"id":"n1","title":"x","sources":[{"name":"s","url":"javascript:alert(1)"}]}`); code != http.StatusBadRequest {
		t.Fatalf("non-web source url: got %d, want 400", code)
	}
	if code := post("/api/news", `{"id":"n1","title":"x","sources":[{"name":"s","url":"https://example.org/a"}]}`); code != http.StatusOK {
		t.Fatalf("valid news: got %d", code)
	}
	_, at := st.news()
	if at == "" {
		t.Fatal("adding news did not set newsUpdatedAt")
	}
	st.st.NewsUpdatedAt = "before"
	if code := post("/api/news?touch=0", `{"id":"n1","title":"x2"}`); code != http.StatusOK {
		t.Fatalf("touch=0 update: got %d", code)
	}
	news, at := st.news()
	if len(news) != 1 || news[0].Title != "x2" || at != "before" {
		t.Fatalf("after touch=0: news=%+v at=%q", news, at)
	}
}

func TestGuard(t *testing.T) {
	h := guard("7865", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cases := []struct {
		host, path, header string
		want               int
	}{
		{"127.0.0.1:7865", "/", "", http.StatusOK},
		{"127.0.0.1:7865", "/api/state", "1", http.StatusOK},
		{"127.0.0.1:7865", "/api/state", "", http.StatusForbidden},     // cross-origin form or img can't set the header
		{"evil.example:7865", "/api/state", "1", http.StatusForbidden}, // DNS rebinding
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://"+c.host+c.path, nil)
		if c.header != "" {
			r.Header.Set("X-Phantom-Console", c.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s%s header=%q: got %d, want %d", c.host, c.path, c.header, w.Code, c.want)
		}
	}
}

func TestReadHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "HISTORY.md")
	md := "# История\n\nintro\n\n## Действия\n\n- 2026-11-19 — **v2.8.4**: done\n- 2026-11-18 — first:\n  - nested `a`\n\n  - nested b\n\n## Версии\n\n- 2026-11-19 — **v2.8.4**\n"
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := readHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Actions) != 2 || len(h.Versions) != 1 {
		t.Fatalf("got %d actions, %d versions", len(h.Actions), len(h.Versions))
	}
	if h.Actions[0].Date != "2026-11-19" || h.Actions[0].Text != "**v2.8.4**: done" {
		t.Fatalf("first action %+v", h.Actions[0])
	}
	if h.Actions[1].Text != "first:\n- nested `a`\n\n- nested b" {
		t.Fatalf("nested action %q", h.Actions[1].Text)
	}
	if _, err := readHistory(""); err == nil {
		t.Fatal("no path should be an error")
	}
}
