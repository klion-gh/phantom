package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"phantom/internal/routing"
)

// The browser bridge: a small HTTP API on 127.0.0.1 that the Phantom browser
// extension (extension/ in this repo) talks to, so a site open in a tab can be
// added to the Умный VPN list in one click.
//
// Who may use it:
//   - Only requests addressed to 127.0.0.1/localhost (the Host header) - a
//     web page that rebinds its own domain to 127.0.0.1 still sends its own
//     domain as Host, so DNS rebinding gets nowhere.
//   - Nothing from a web page. Browsers set Origin themselves, a page can't
//     claim an extension's, and every POST a page can make carries its own
//     origin (or "null") - so a request that names a non-extension origin is
//     refused outright. A request with no Origin at all can only come from an
//     extension (Firefox doesn't always send one) or a local program, which
//     could read the app's files anyway.
//   - Everything else needs a token, which the extension only receives after
//     the user approves the pairing in this app, having checked that both show
//     the same code. A page can't attach a token header without a CORS
//     preflight, and no response here ever carries CORS headers: extensions
//     with host permission for 127.0.0.1 don't need them, and nothing else
//     gets to read an answer.
//
// Tokens are stored hashed; "Отключить все браузеры" in Settings forgets them.

const (
	bridgeFirstPort = 47815
	bridgePortCount = 5 // the extension probes the same range

	bridgeEnabledFileName = "browser_bridge"
	bridgeClientsFileName = "browser_clients"

	pairCodeTTL = 2 * time.Minute
)

type bridgeClient struct {
	TokenHash string    `json:"tokenHash"`
	Client    string    `json:"client"`
	Origin    string    `json:"origin"`
	Paired    time.Time `json:"paired"`
}

type pairRequest struct {
	ID      string
	Code    string
	Client  string
	Origin  string
	Created time.Time
	Status  string // pending, approved, denied, expired
	Token   string // handed out once, on the first poll after approval
}

type browserBridge struct {
	app *App

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	port     int
	pending  *pairRequest // at most one: a new request replaces the old
	clients  []bridgeClient
}

var bridge = &browserBridge{}

func loadBridgeEnabled() bool { return loadSetting(bridgeEnabledFileName, "1", "1", "0") == "1" }

func saveBridgeEnabled(on bool) { saveSetting(bridgeEnabledFileName, boolToSetting(on), "1", "1", "0") }

func loadBridgeClients() []bridgeClient {
	var out []bridgeClient
	if raw := readSettingFile(bridgeClientsFileName); raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

func saveBridgeClients(clients []bridgeClient) {
	data, _ := json.Marshal(clients)
	writeSettingFile(bridgeClientsFileName, string(data))
}

// start listens on the first free port of the range. A no-op if already
// running.
func (b *browserBridge) start(app *App) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.app = app
	if b.server != nil {
		return
	}
	b.clients = loadBridgeClients()
	for p := bridgeFirstPort; p < bridgeFirstPort+bridgePortCount; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		b.listener, b.port = ln, p
		b.server = &http.Server{
			Handler:           b.handler(),
			ReadHeaderTimeout: 5 * time.Second,
			MaxHeaderBytes:    16 << 10,
		}
		go b.server.Serve(ln)
		diag(diagCatApp, "bridgeListening", "port", p, "clients", len(b.clients))
		return
	}
	diag(diagCatApp, "bridgeNoPort", "from", bridgeFirstPort, "count", bridgePortCount)
}

func (b *browserBridge) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hello", b.handleHello)
	mux.HandleFunc("/v1/pair", b.handlePair)
	mux.HandleFunc("/v1/pair/", b.handlePairPoll)
	mux.HandleFunc("/v1/status", b.authed(b.handleStatus))
	mux.HandleFunc("/v1/site", b.authed(b.handleSite))
	mux.HandleFunc("/v1/site/add", b.authed(b.handleSiteAdd))
	mux.HandleFunc("/v1/site/remove", b.authed(b.handleSiteRemove))
	mux.HandleFunc("/v1/smart", b.authed(b.handleSmart))
	mux.HandleFunc("/v1/show", b.authed(b.handleShow))
	return b.guard(mux)
}

func (b *browserBridge) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.server != nil {
		b.server.Close()
		b.server, b.listener, b.port = nil, nil, 0
		diag(diagCatApp, "bridgeStopped")
	}
	b.pending = nil
}

// guard applies the checks every request must pass: addressed to this
// machine by name, and - when a browser says where it comes from - coming from
// an extension.
func (b *browserBridge) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !isExtensionOrigin(o) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		next.ServeHTTP(w, r)
	})
}

func isExtensionOrigin(o string) bool {
	for _, scheme := range []string{"chrome-extension://", "moz-extension://", "extension://"} {
		if strings.HasPrefix(o, scheme) && len(o) > len(scheme) {
			return true
		}
	}
	return false
}

func (b *browserBridge) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Phantom-Token")
		if token == "" || !b.knownToken(token) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unpaired"})
			return
		}
		next(w, r)
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (b *browserBridge) knownToken(token string) bool {
	h := []byte(hashToken(token))
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.clients {
		if subtle.ConstantTimeCompare(h, []byte(c.TokenHash)) == 1 {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	if r.Method != http.MethodPost {
		return errors.New("POST only")
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (b *browserBridge) handleHello(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"app": "phantom", "version": AppVersion, "api": 1})
}

// handlePair starts a pairing: the app pops up a confirmation showing a code
// that the extension shows too, and the extension polls /v1/pair/<id> for the
// outcome.
func (b *browserBridge) handlePair(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin") // guard has refused any non-extension one
	var req struct {
		Client string `json:"client"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	client := strings.TrimSpace(req.Client)
	if len(client) > 40 {
		client = client[:40]
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(10000))
	p := &pairRequest{
		ID:      randomHex(16),
		Code:    fmt.Sprintf("%04d", n.Int64()),
		Client:  client,
		Origin:  origin,
		Created: time.Now(),
		Status:  "pending",
	}
	b.mu.Lock()
	b.pending = p
	app := b.app
	b.mu.Unlock()

	diag(diagCatApp, "bridgePairRequest", "client", client)
	if app != nil && app.ctx != nil {
		runtime.WindowShow(app.ctx)
		runtime.WindowUnminimise(app.ctx)
		runtime.EventsEmit(app.ctx, "bridge:pair", map[string]string{"id": p.ID, "code": p.Code, "client": client})
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": p.ID, "code": p.Code})
}

func (b *browserBridge) handlePairPoll(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/pair/")
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pending
	if p == nil || subtle.ConstantTimeCompare([]byte(id), []byte(p.ID)) != 1 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "expired"})
		return
	}
	if p.Status == "pending" && time.Since(p.Created) > pairCodeTTL {
		p.Status = "expired"
	}
	resp := map[string]string{"status": p.Status}
	if p.Status == "approved" {
		resp["token"] = p.Token
	}
	if p.Status != "pending" {
		// Settled: the answer (and the token with it) is handed out once.
		b.pending = nil
		b.notifyPairClosed(p.ID)
	}
	writeJSON(w, http.StatusOK, resp)
}

// answer settles the pending pairing from the app's confirmation dialog.
func (b *browserBridge) answer(id string, approve bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pending
	if p == nil || p.ID != id || p.Status != "pending" {
		return
	}
	if !approve || time.Since(p.Created) > pairCodeTTL {
		p.Status = "denied"
		diag(diagCatApp, "bridgePairDenied", "client", p.Client)
		return
	}
	p.Status = "approved"
	p.Token = randomHex(32)
	b.clients = append(b.clients, bridgeClient{
		TokenHash: hashToken(p.Token),
		Client:    p.Client,
		Origin:    p.Origin,
		Paired:    time.Now(),
	})
	saveBridgeClients(b.clients)
	diag(diagCatApp, "bridgePaired", "client", p.Client, "clients", len(b.clients))
}

func (b *browserBridge) notifyPairClosed(id string) {
	if b.app != nil && b.app.ctx != nil {
		runtime.EventsEmit(b.app.ctx, "bridge:pairClosed", id)
	}
}

func (b *browserBridge) revokeAll() {
	b.mu.Lock()
	b.clients = nil
	b.mu.Unlock()
	saveBridgeClients(nil)
	diag(diagCatApp, "bridgeRevoked")
}

func (b *browserBridge) snapshot() (running bool, port, clients int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.server != nil, b.port, len(b.clients)
}

type bridgeStatus struct {
	Version      string `json:"version"`
	SmartEnabled bool   `json:"smartEnabled"`
	AutoEnabled  bool   `json:"autoEnabled"`
	Connected    bool   `json:"connected"`
	Sites        int    `json:"sites"`
}

func (b *browserBridge) status() bridgeStatus {
	connected := false
	if b.app != nil {
		b.app.mu.Lock()
		connected = b.app.tunnel != nil
		b.app.mu.Unlock()
	}
	return bridgeStatus{
		Version:      AppVersion,
		SmartEnabled: loadSmartEnabled(),
		AutoEnabled:  loadAutoEnabled(),
		Connected:    connected,
		Sites:        len(loadSmartSites()),
	}
}

func (b *browserBridge) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, b.status())
}

type siteResponse struct {
	routing.SiteMatch
	Status bridgeStatus `json:"status"`
}

func (b *browserBridge) siteAnswer(w http.ResponseWriter, host string) {
	writeJSON(w, http.StatusOK, siteResponse{
		SiteMatch: routing.MatchSite(host, loadSmartSites()),
		Status:    b.status(),
	})
}

type siteRequest struct {
	Host  string `json:"host"`
	Entry string `json:"entry"`
}

func (b *browserBridge) handleSite(w http.ResponseWriter, r *http.Request) {
	var req siteRequest
	if err := readJSON(r, &req); err != nil || routing.NormalizeHost(req.Host) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	b.siteAnswer(w, req.Host)
}

func (b *browserBridge) handleSiteAdd(w http.ResponseWriter, r *http.Request) {
	var req siteRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	before := loadSmartSites()
	after, ok := routing.AddSite(before, req.Host, req.Entry)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(after) != len(before) {
		// The user picked this site themselves, so naming it in the log
		// reveals nothing they didn't choose to put in the list.
		diag(diagCatRoute, "bridgeSiteAdd", "host", routing.NormalizeHost(req.Host), "added", len(after)-len(before))
		b.applySites(after)
	}
	b.siteAnswer(w, req.Host)
}

func (b *browserBridge) handleSiteRemove(w http.ResponseWriter, r *http.Request) {
	var req siteRequest
	if err := readJSON(r, &req); err != nil || routing.NormalizeHost(req.Host) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	before := loadSmartSites()
	after := routing.RemoveSite(before, req.Host)
	if len(after) != len(before) {
		diag(diagCatRoute, "bridgeSiteRemove", "host", routing.NormalizeHost(req.Host), "removed", len(before)-len(after))
		b.applySites(after)
	}
	b.siteAnswer(w, req.Host)
}

func (b *browserBridge) applySites(sites []string) {
	if b.app == nil {
		return
	}
	applySiteList(b.app, sites)
	if b.app.ctx != nil {
		runtime.EventsEmit(b.app.ctx, "routing:changed")
	}
}

// handleSmart turns Умный VPN on or off. The frontend owns what that entails
// (connecting, or handing the tunnel back), so this asks it to do exactly
// what its own toggle does rather than re-implementing that here.
func (b *browserBridge) handleSmart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if b.app == nil || b.app.ctx == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	diag(diagCatRoute, "bridgeSmart", "enabled", req.Enabled)
	runtime.EventsEmit(b.app.ctx, "bridge:smart", req.Enabled)
	writeJSON(w, http.StatusAccepted, map[string]bool{"requested": req.Enabled})
}

func (b *browserBridge) handleShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if b.app != nil && b.app.ctx != nil {
		runtime.WindowShow(b.app.ctx)
		runtime.WindowUnminimise(b.app.ctx)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- App bindings (Settings → Браузерное расширение) -----------------------

// GetBrowserBridge reports the bridge's state for Settings.
func (a *App) GetBrowserBridge() string {
	running, port, clients := bridge.snapshot()
	data, _ := json.Marshal(map[string]any{
		"enabled": loadBridgeEnabled(),
		"running": running,
		"port":    port,
		"clients": clients,
	})
	return string(data)
}

// SetBrowserBridgeEnabled turns the bridge on or off. Off stops listening
// altogether; paired browsers stay paired and work again once it's back on.
func (a *App) SetBrowserBridgeEnabled(on bool) {
	saveBridgeEnabled(on)
	if on {
		bridge.start(a)
	} else {
		bridge.stop()
	}
}

// RevokeBrowserClients forgets every paired browser.
func (a *App) RevokeBrowserClients() { bridge.revokeAll() }

// AnswerBrowserPairing settles the pairing request shown in the app's dialog.
func (a *App) AnswerBrowserPairing(id string, approve bool) { bridge.answer(id, approve) }

func startBrowserBridgeIfEnabled(a *App) {
	if loadBridgeEnabled() {
		bridge.start(a)
	} else {
		log.Printf("[bridge] disabled in settings")
	}
}
