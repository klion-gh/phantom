// console is a local operator dashboard: the servers saved in the Phantom
// desktop app on a world map, checked on demand with a real handshake, and a
// board for planning protocol work.
//
// It reads the app's configs.json and never writes it; servers added in the
// console itself and what it learns (each IP's location, the last check of
// each config, the board) live in its own console.json next to it. The page only ever sees domains, endpoints and
// results - the yaml with its PSK and keys stays in this process.
//
// Nothing runs on a timer. A check happens when someone presses "Проверить";
// a location is looked up once per IP (see internal/geoip for what that
// discloses and to whom) and kept.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:7865", "address to serve the dashboard on (loopback only)")
	configsPath := flag.String("configs", "", "the Phantom app's configs.json (default: the app's own, in the user config dir)")
	noBrowser := flag.Bool("no-browser", false, "don't open the dashboard in a browser")
	historyPath := flag.String("history", "", "HISTORY.md for the История section (default: found by walking up from the working directory)")
	flag.Parse()

	userDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("user config dir: %v", err)
	}
	appDir := filepath.Join(userDir, "Phantom")
	if *configsPath == "" {
		*configsPath = filepath.Join(appDir, "configs.json")
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("-addr: %v", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		// The API starts handshakes and lists the operator's servers; it has
		// no authentication of its own, so it must not be reachable from
		// anywhere but this machine.
		log.Fatalf("-addr must be a loopback address, got %q", host)
	}

	st, err := openStore(filepath.Join(appDir, "console.json"))
	if err != nil {
		log.Fatalf("open console.json: %v", err)
	}
	a := &app{configsPath: *configsPath, store: st, historyPath: findHistory(*historyPath)}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	url := "http://127.0.0.1:" + port + "/"

	log.Printf("configs: %s", *configsPath)
	log.Printf("history: %s", a.historyPath)
	log.Printf("dashboard: %s", url)
	if !*noBrowser {
		openBrowser(url)
	}

	srv := &http.Server{Handler: guard(port, a.routes()), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(web))

	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		h, err := readHistory(a.historyPath)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, h)
	})

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		servers, err := a.views()
		if err != nil {
			httpError(w, err)
			return
		}
		news, newsAt := a.store.news()
		writeJSON(w, map[string]any{
			"servers": servers, "tasks": a.store.tasks(),
			"news": news, "newsUpdatedAt": newsAt,
			"configsPath": a.configsPath, "slowMs": slowMs,
		})
	})

	// News is written by Claude (or edited on the page). ?touch=0 updates an
	// item without marking the news as freshly gathered - used when the page
	// links an action to a task it just created.
	mux.HandleFunc("POST /api/news", func(w http.ResponseWriter, r *http.Request) {
		var n News
		if err := readJSON(r, &n); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		n.Title = strings.TrimSpace(n.Title)
		if n.Title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		for _, src := range n.Sources {
			// Rendered as links on the page: anything but a web address is refused.
			if !strings.HasPrefix(src.URL, "https://") && !strings.HasPrefix(src.URL, "http://") {
				http.Error(w, "source url must be http(s): "+src.URL, http.StatusBadRequest)
				return
			}
		}
		if n.ID == "" {
			n.ID = "news" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		touch := r.URL.Query().Get("touch") != "0"
		if err := a.store.putNews(n, touch, time.Now().UTC().Format(time.RFC3339)); err != nil {
			httpError(w, err)
			return
		}
		news, newsAt := a.store.news()
		writeJSON(w, map[string]any{"news": news, "newsUpdatedAt": newsAt})
	})

	mux.HandleFunc("DELETE /api/news/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.store.deleteNews(r.PathValue("id")); err != nil {
			httpError(w, err)
			return
		}
		news, newsAt := a.store.news()
		writeJSON(w, map[string]any{"news": news, "newsUpdatedAt": newsAt})
	})

	mux.HandleFunc("POST /api/locate", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		a.locate(ctx)
		a.writeServers(w)
	})

	mux.HandleFunc("POST /api/check", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"` // empty: every saved config
		}
		if err := readJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cs, err := a.findConfigs(req.IDs)
		if err != nil {
			httpError(w, err)
			return
		}
		a.check(cs)
		// A check can turn up an IP the console hasn't placed yet.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		a.locate(ctx)
		a.writeServers(w)
	})

	// Adding a server is a pasted client.yaml, as in the apps. It is checked
	// right away - the person just asked for it - and placed on the map.
	mux.HandleFunc("POST /api/servers", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Yaml string `json:"yaml"`
		}
		if err := readJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, err := a.addConfig(req.Yaml)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errDuplicate) {
				code = http.StatusConflict
			}
			http.Error(w, err.Error(), code)
			return
		}
		a.check([]savedConfig{c})
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		a.locate(ctx)
		servers, err := a.views()
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"servers": servers, "id": c.ID})
	})

	mux.HandleFunc("DELETE /api/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		ok, err := a.store.deleteConfig(r.PathValue("id"))
		if err != nil {
			httpError(w, err)
			return
		}
		if !ok {
			http.Error(w, "этот сервер добавлен в приложении Phantom, удалите его там", http.StatusConflict)
			return
		}
		a.writeServers(w)
	})

	mux.HandleFunc("POST /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		var t Task
		if err := readJSON(r, &t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		t.Title = strings.TrimSpace(t.Title)
		if t.Title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		if t.ID == "" {
			t.ID = "t" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		if t.Order == 0 {
			t.Order = time.Now().Unix()
		}
		t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := a.store.putTask(t); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"tasks": a.store.tasks()})
	})

	mux.HandleFunc("DELETE /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.store.deleteTask(r.PathValue("id")); err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, map[string]any{"tasks": a.store.tasks()})
	})
	return mux
}

func (a *app) writeServers(w http.ResponseWriter) {
	servers, err := a.views()
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]any{"servers": servers})
}

// guard keeps the API to this machine's own page. The Host check stops DNS
// rebinding (a remote page whose name resolves to 127.0.0.1); the custom
// header on API calls can't be sent cross-origin without a CORS preflight,
// which this server never approves.
func guard(port string, next http.Handler) http.Handler {
	allowed := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("X-Phantom-Console") != "1" {
			http.Error(w, "missing X-Phantom-Console header", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func readJSON(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("expected application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("open browser: %v (open %s yourself)", err, url)
	}
}
