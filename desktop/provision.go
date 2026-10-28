package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"phantom/internal/pingcheck"
	"phantom/internal/provision"
)

// "Подключить свой сервер": set up Phantom on a VPS from its IP, login and
// password (internal/provision), with progress streamed to the frontend as
// "provision:step" / "provision:log" events.

// Host key fingerprints of servers set up before, so a later run notices a
// server whose key has changed. Keyed "host:port".
const knownHostsFileName = "ssh_known_hosts"

func loadKnownHosts() map[string]string {
	m := map[string]string{}
	if raw := readSettingFile(knownHostsFileName); raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}

func saveKnownHost(key, fingerprint string) {
	m := loadKnownHosts()
	m[key] = fingerprint
	data, _ := json.Marshal(m)
	writeSettingFile(knownHostsFileName, string(data))
}

var (
	provisionMu     sync.Mutex
	provisionCancel context.CancelFunc
)

type provisionListener struct{ ctx context.Context }

func (l provisionListener) Step(step, state string) {
	runtime.EventsEmit(l.ctx, "provision:step", map[string]string{"step": step, "state": state})
}

func (l provisionListener) Log(line string) {
	runtime.EventsEmit(l.ctx, "provision:log", line)
}

// ProvisionServer runs the whole setup and returns
// {"ok":true,"yaml":...,"existing":...,"latencyMs":...} or
// {"ok":false,"code":...,"detail":...}. The password is used for this run
// only and never stored.
func (a *App) ProvisionServer(requestJSON string) string {
	var req provision.Request
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return provisionReply(nil, &provision.Error{Code: provision.CodeInvalid, Detail: err.Error()})
	}
	sshPort := req.SSHPort
	if sshPort == 0 {
		sshPort = 22
	}
	hostKey := net.JoinHostPort(req.Host, strconv.Itoa(sshPort))
	req.KnownHostKey = loadKnownHosts()[hostKey]

	ctx, cancel := context.WithCancel(context.Background())
	provisionMu.Lock()
	if provisionCancel != nil {
		provisionCancel()
	}
	provisionCancel = cancel
	provisionMu.Unlock()
	defer cancel()

	diag(diagCatApp, "provisionStart", "sshPort", sshPort, "port", req.Port, "rootUser", req.User == "" || req.User == "root")
	opts := provision.Around(pingOptions().ProtectFD, func(yaml string) (int64, error) {
		res, err := pingcheck.PingWith(yaml, pingOptions())
		return res.LatencyMs, err
	})
	res, err := provision.Run(ctx, req, opts, provisionListener{ctx: a.ctx})
	if err == nil && res.HostKey != "" {
		saveKnownHost(hostKey, res.HostKey)
	}
	return provisionReply(&res, err)
}

// CancelProvision stops a setup in progress (closing the SSH connection
// stops whatever was running on the server).
func (a *App) CancelProvision() {
	provisionMu.Lock()
	if provisionCancel != nil {
		provisionCancel()
	}
	provisionMu.Unlock()
}

func provisionReply(res *provision.Result, err error) string {
	out := map[string]any{"ok": err == nil}
	if err != nil {
		var pe *provision.Error
		if errors.As(err, &pe) {
			out["code"], out["detail"] = pe.Code, pe.Detail
		} else {
			out["code"], out["detail"] = "failed", err.Error()
		}
		diag(diagCatApp, "provisionFailed", "code", out["code"])
	} else {
		out["yaml"], out["existing"], out["latencyMs"] = res.ConfigYAML, res.Existing, res.LatencyMs
		diag(diagCatApp, "provisionDone", "existing", res.Existing, "latencyMs", res.LatencyMs)
	}
	data, _ := json.Marshal(out)
	return string(data)
}

// ForgetServerKey drops a remembered host key - "Доверять новому ключу",
// after the user confirms the server was reinstalled.
func (a *App) ForgetServerKey(host string, sshPort int) {
	if sshPort == 0 {
		sshPort = 22
	}
	m := loadKnownHosts()
	delete(m, net.JoinHostPort(host, strconv.Itoa(sshPort)))
	data, _ := json.Marshal(m)
	writeSettingFile(knownHostsFileName, string(data))
}
