//go:build !windows

package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"phantom/internal/pingcheck"
	"phantom/internal/provision"
)

// ProvisionListener receives progress from ProvisionServer: each step as it
// goes active/done/failed, and every log line. Called on Go's goroutines -
// the app hops to its main thread itself.
type ProvisionListener interface {
	OnStep(step string, state string)
	OnLog(line string)
}

var (
	provisionMu     sync.Mutex
	provisionCancel context.CancelFunc
)

type provisionAdapter struct{ l ProvisionListener }

func (a provisionAdapter) Step(step, state string) { a.l.OnStep(step, state) }
func (a provisionAdapter) Log(line string)         { a.l.OnLog(line) }

// ProvisionServer sets up Phantom on a VPS from its IP, login and password
// ("Подключить свой сервер") - see internal/provision. Blocks until done, so
// call it off the main thread. requestJSON is provision.Request (including
// knownHostKey, which the app remembers per server); the reply is
// {"ok":true,"yaml":...,"existing":...,"latencyMs":...,"hostKey":...} or
// {"ok":false,"code":...,"detail":...}. The password is used for this call
// only.
func ProvisionServer(requestJSON string, listener ProvisionListener) string {
	var req provision.Request
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return provisionReply(nil, &provision.Error{Code: provision.CodeInvalid, Detail: err.Error()})
	}
	ctx, cancel := context.WithCancel(context.Background())
	provisionMu.Lock()
	if provisionCancel != nil {
		provisionCancel()
	}
	provisionCancel = cancel
	provisionMu.Unlock()
	defer cancel()

	// Around the app's own VPN when it is up, the same way its pings go.
	protect := pingOptions().ProtectFD
	opts := provision.Around(protect, func(yaml string) (int64, error) {
		res, err := pingcheck.PingWith(yaml, pingOptions())
		return res.LatencyMs, err
	})
	res, err := provision.Run(ctx, req, opts, provisionAdapter{listener})
	return provisionReply(&res, err)
}

// CancelProvision stops a ProvisionServer in progress.
func CancelProvision() {
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
	} else {
		out["yaml"], out["existing"], out["latencyMs"], out["hostKey"] = res.ConfigYAML, res.Existing, res.LatencyMs, res.HostKey
	}
	data, _ := json.Marshal(out)
	return string(data)
}
