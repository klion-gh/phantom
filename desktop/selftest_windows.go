package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// runSelfTest is a temporary, headless verification path used to validate
// StartWindows/Stop from an elevated console without driving the Wails GUI
// (elevated windows can't be scripted via UI automation - Windows UIPI blocks
// synthetic input from a lower-integrity process). Invoked as:
//
//	phantom.exe selftest <path-to-client.yaml>
//
// Writes a plain-text report to %TEMP%\phantom_selftest.txt since an elevated
// child process's stdout isn't visible to a non-elevated parent shell.
func runSelfTest(configPath string) {
	report := os.Getenv("TEMP") + "\\phantom_selftest.txt"
	f, _ := os.Create(report)
	defer f.Close()
	log := func(format string, args ...interface{}) {
		fmt.Fprintf(f, format+"\n", args...)
	}

	yaml, err := os.ReadFile(configPath)
	if err != nil {
		log("FAIL: read config: %v", err)
		return
	}

	log("=== route table BEFORE connect ===")
	log("%s", routePrint())

	log("=== connecting ===")
	start := time.Now()
	tun, err := StartWindows(string(yaml), nil)
	if err != nil {
		log("FAIL: StartWindows: %v", err)
		return
	}
	log("connected in %s", time.Since(start))

	log("=== route table AFTER connect ===")
	log("%s", routePrint())

	log("IsAlive: %v", tun.IsAlive())
	log("Stats: %s", tun.Stats())

	log("=== fetching http://cloudflare.com/cdn-cgi/trace through the tunnel ===")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://cloudflare.com/cdn-cgi/trace")
	if err != nil {
		log("FAIL: http fetch through tunnel: %v", err)
	} else {
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		log("OK: http fetch succeeded, status=%s body=%s", resp.Status, string(buf[:n]))
	}

	log("=== disconnecting ===")
	tun.Stop()
	time.Sleep(1 * time.Second)

	log("=== route table AFTER disconnect ===")
	log("%s", routePrint())

	log("=== confirming normal connectivity restored ===")
	resp2, err := client.Get("http://cloudflare.com/cdn-cgi/trace")
	if err != nil {
		log("FAIL: http fetch after disconnect: %v", err)
	} else {
		resp2.Body.Close()
		log("OK: connectivity restored, status=%s", resp2.Status)
	}

	log("=== DONE ===")
}

func routePrint() string {
	out, err := runNetCmd("route", "print", "-4")
	if err != nil {
		return fmt.Sprintf("route print failed: %v", err)
	}
	return out
}
