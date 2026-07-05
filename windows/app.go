package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// App is the Wails-bound backend: every exported method here is directly
// callable from the frontend's JS (window.go.main.App.*).
type App struct {
	ctx context.Context

	mu     sync.Mutex
	tunnel *WinTunnel
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	initLog()
	log.Println("App started")
}

// shutdown runs when the window is closed. Without this, closing the window
// while connected (instead of clicking Disconnect first) would leave the TUN
// adapter, routing table entries, and pooled connections dangling.
func (a *App) shutdown(ctx context.Context) {
	a.Disconnect()
}

// Connect blocks until the tunnel is either up or has definitively failed
// (StartWindows has its own internal dial timeout), returning an empty
// string on success or an error message otherwise. The frontend sets its own
// "connecting" UI state immediately after calling this, the same way the
// Android app's button does, rather than needing a separate polling step for
// this specific transition.
func (a *App) Connect(configYAML string) string {
	a.mu.Lock()
	if a.tunnel != nil {
		a.mu.Unlock()
		return "already connected"
	}
	a.mu.Unlock()

	log.Println("connect: establishing tunnel")
	tun, err := StartWindows(configYAML)
	if err != nil {
		log.Printf("connect failed: %v", err)
		return err.Error()
	}

	a.mu.Lock()
	a.tunnel = tun
	a.mu.Unlock()
	log.Println("connected")
	return ""
}

func (a *App) Disconnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tunnel == nil {
		return
	}
	a.tunnel.Stop()
	a.tunnel = nil
	log.Println("disconnected")
}

type statusResponse struct {
	Connected bool   `json:"connected"`
	Alive     bool   `json:"alive"`
	Stats     string `json:"stats"`
}

// Status reports whether a tunnel is currently up. The frontend polls this
// while "connected" to detect an unexpected drop (Alive=false while
// Connected=true means the session died without an explicit Disconnect).
func (a *App) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	resp := statusResponse{Connected: a.tunnel != nil}
	if a.tunnel != nil {
		resp.Alive = a.tunnel.IsAlive()
		resp.Stats = a.tunnel.Stats()
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

// ReadLog returns the full contents of the log file for the in-app viewer.
func (a *App) ReadLog() string {
	return readLog()
}

// SaveConfig/LoadConfig persist the pasted client.yaml text between runs,
// mirroring the Android app's EncryptedSharedPreferences-backed config
// screen (Windows equivalent: a file under the user's per-user config
// directory, which is already access-controlled by the OS to that user).
func (a *App) SaveConfig(configYAML string) string {
	path, err := configFilePath()
	if err != nil {
		return err.Error()
	}
	if err := os.WriteFile(path, []byte(configYAML), 0600); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) LoadConfig() string {
	path, err := configFilePath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, "Phantom")
	if err := os.MkdirAll(full, 0700); err != nil {
		return "", err
	}
	return full, nil
}

func configFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "client.yaml"), nil
}
