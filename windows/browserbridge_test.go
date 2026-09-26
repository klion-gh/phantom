package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const extOrigin = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"

// newTestBridge serves a bridge whose settings live in a temp directory
// rather than the user's real %AppData%\Phantom.
func newTestBridge(t *testing.T) (*browserBridge, *httptest.Server) {
	t.Helper()
	t.Setenv("AppData", t.TempDir())
	b := &browserBridge{app: &App{}}
	srv := httptest.NewServer(b.handler())
	t.Cleanup(srv.Close)
	return b, srv
}

func call(t *testing.T, srv *httptest.Server, method, path, origin, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set("X-Phantom-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("%s %s answered with CORS headers - a web page could read it", method, path)
	}
	return resp.StatusCode, out
}

func pair(t *testing.T, b *browserBridge, srv *httptest.Server) string {
	t.Helper()
	code, started := call(t, srv, "POST", "/v1/pair", extOrigin, "", map[string]string{"client": "Chrome"})
	if code != 200 || started["code"] == "" {
		t.Fatalf("pairing did not start: %d %v", code, started)
	}
	id := started["id"].(string)
	if _, poll := call(t, srv, "GET", "/v1/pair/"+id, extOrigin, "", nil); poll["status"] != "pending" {
		t.Fatalf("expected pending before the user answers, got %v", poll)
	}
	b.answer(id, true)
	_, poll := call(t, srv, "GET", "/v1/pair/"+id, extOrigin, "", nil)
	token, _ := poll["token"].(string)
	if poll["status"] != "approved" || len(token) != 64 {
		t.Fatalf("expected a token once approved, got %v", poll)
	}
	if _, again := call(t, srv, "GET", "/v1/pair/"+id, extOrigin, "", nil); again["token"] != nil {
		t.Fatal("the token was handed out twice")
	}
	return token
}

func TestBridgeRefusesWebPagesAndForeignHosts(t *testing.T) {
	_, srv := newTestBridge(t)
	if code, _ := call(t, srv, "POST", "/v1/pair", "https://evil.example", "", map[string]string{"client": "x"}); code != 403 {
		t.Fatalf("a web page started a pairing: %d", code)
	}
	if code, _ := call(t, srv, "POST", "/v1/pair", "null", "", map[string]string{"client": "x"}); code != 403 {
		t.Fatalf("an opaque-origin page (sandboxed frame, redirected form) started a pairing: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/hello", "https://evil.example", "", nil); code != 403 {
		t.Fatalf("a web page got an answer: %d", code)
	}

	// DNS rebinding: the page's own name arrives as Host.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/hello", nil)
	req.Host = "rebind.evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("a rebound host got through: %d", resp.StatusCode)
	}
}

func TestBridgeNeedsAPairedToken(t *testing.T) {
	_, srv := newTestBridge(t)
	if code, _ := call(t, srv, "GET", "/v1/status", extOrigin, "", nil); code != 401 {
		t.Fatalf("status without a token: %d", code)
	}
	if code, _ := call(t, srv, "POST", "/v1/site/add", extOrigin, "made-up", map[string]string{"host": "example.com"}); code != 401 {
		t.Fatalf("add with an unknown token: %d", code)
	}
}

func TestBridgePairedExtensionAddsAndRemovesSites(t *testing.T) {
	b, srv := newTestBridge(t)
	token := pair(t, b, srv)

	code, got := call(t, srv, "POST", "/v1/site/add", extOrigin, token, map[string]string{"host": "www.rutracker.org"})
	if code != 200 || got["listed"] != true {
		t.Fatalf("add failed: %d %v", code, got)
	}
	if sites := loadSmartSites(); len(sites) != 2 { // RuTracker is a catalogue service
		t.Fatalf("expected the whole service to be added, got %v", sites)
	}
	if code, _ := call(t, srv, "POST", "/v1/site/add", extOrigin, token,
		map[string]string{"host": "example.com", "entry": "anything-else.com"}); code != 400 {
		t.Fatalf("an entry that isn't one of the host's options was accepted: %d", code)
	}

	_, got = call(t, srv, "POST", "/v1/site/remove", extOrigin, token, map[string]string{"host": "rutracker.org"})
	if got["listed"] != false || len(loadSmartSites()) != 0 {
		t.Fatalf("remove failed: %v, list %v", got, loadSmartSites())
	}

	b.revokeAll()
	if code, _ := call(t, srv, "GET", "/v1/status", extOrigin, token, nil); code != 401 {
		t.Fatalf("a revoked token still works: %d", code)
	}
}

func TestBridgeDeniedPairingGetsNoToken(t *testing.T) {
	b, srv := newTestBridge(t)
	_, started := call(t, srv, "POST", "/v1/pair", extOrigin, "", map[string]string{"client": "Firefox"})
	id := started["id"].(string)
	b.answer(id, false)
	_, poll := call(t, srv, "GET", "/v1/pair/"+id, extOrigin, "", nil)
	if poll["status"] != "denied" || poll["token"] != nil {
		t.Fatalf("got %v", poll)
	}
}
