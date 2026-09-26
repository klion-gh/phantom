package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeVPS is a real SSH server (golang.org/x/crypto/ssh, in-process) whose
// "shell" answers the handful of commands provisioning sends, the way a
// fresh VPS would.
type fakeVPS struct {
	t        *testing.T
	user     string
	password string
	uid      string
	sudo     bool   // the user may sudo with its password
	tty      bool   // sudo insists on a terminal ("requiretty")
	existing string // client.yaml.example of an existing install, "" for a fresh server
	install  func(cmd string, pty bool) (string, int)

	mu       sync.Mutex
	cmds     []string
	uploaded string
	addr     string
	key      ssh.PublicKey
}

const goodYAML = `server: "vpn.example.com:9443"
domain: "vpn.example.com"
fingerprint: "auto"
psk: "0000000000000000000000000000000000000000000000000000000000000001"
server_public_key: "0000000000000000000000000000000000000000000000000000000000000002"
listen: "127.0.0.1:1080"
listen_http: "127.0.0.1:1081"
pool_size: 4
log_level: "info"`

func newFakeVPS(t *testing.T) *fakeVPS {
	v := &fakeVPS{t: t, user: "root", password: "hunter2", uid: "0", sudo: true}
	v.install = func(cmd string, pty bool) (string, int) {
		return "\x1b[1;34m==>\x1b[0m Downloading phantom-server (amd64)...\n@@PHANTOM step download\n" +
			"@@PHANTOM step keys\n@@PHANTOM step service\n@@PHANTOM client-begin\n" + goodYAML + "\n@@PHANTOM client-end\n", 0
	}
	return v
}

func (v *fakeVPS) start() {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		v.t.Fatal(err)
	}
	v.key = signer.PublicKey()
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == v.user && string(pass) == v.password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		v.t.Fatal(err)
	}
	v.t.Cleanup(func() { ln.Close() })
	v.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go v.serve(c, cfg)
		}
	}()
}

func (v *fakeVPS) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			pty := false
			for r := range requests {
				switch r.Type {
				case "pty-req":
					pty = true
					r.Reply(true, nil)
				case "exec":
					n := binary.BigEndian.Uint32(r.Payload[:4])
					cmd := string(r.Payload[4 : 4+n])
					r.Reply(true, nil)
					stdin, _ := io.ReadAll(ch)
					out, code := v.run(cmd, string(stdin), pty)
					if _, err := io.WriteString(ch, out); err != nil {
						v.t.Logf("fake sshd write: %v", err)
					}
					// What sshd does: EOF on the output, then the exit status.
					ch.CloseWrite()
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
					return
				default:
					r.Reply(false, nil)
				}
			}
		}()
	}
}

func (v *fakeVPS) run(cmd, stdin string, pty bool) (string, int) {
	v.mu.Lock()
	v.cmds = append(v.cmds, cmd)
	v.mu.Unlock()
	if strings.HasPrefix(cmd, "sudo -S -p '' ") {
		if !v.sudo || stdin != v.password+"\n" {
			return "Sorry, user " + v.user + " is not allowed to execute this as root.\n", 1
		}
		if v.tty && !pty {
			return "sudo: sorry, you must have a tty to run sudo\n", 1
		}
		cmd = strings.TrimPrefix(cmd, "sudo -S -p '' ")
		stdin = ""
	}
	switch {
	case cmd == "id -u":
		return v.uid + "\n", 0
	case cmd == "true":
		return "", 0
	case cmd == "test -f /opt/phantom/server.yaml":
		if v.existing != "" {
			return "", 0
		}
		return "", 1
	case cmd == "cat /opt/phantom/client.yaml.example":
		if v.existing == "" {
			return "cat: /opt/phantom/client.yaml.example: No such file or directory\n", 1
		}
		return v.existing, 0
	case cmd == "cat > /tmp/phantom-install.sh":
		v.mu.Lock()
		v.uploaded = stdin
		v.mu.Unlock()
		return "", 0
	case strings.HasPrefix(cmd, "env PHANTOM_APP=1 "):
		return v.install(cmd, pty)
	case cmd == "rm -f /tmp/phantom-install.sh":
		return "", 0
	}
	return "sh: unknown command: " + cmd + "\n", 127
}

type recorder struct {
	mu    sync.Mutex
	steps []string
	logs  []string
}

func (r *recorder) Step(step, state string) {
	r.mu.Lock()
	r.steps = append(r.steps, step+":"+state)
	r.mu.Unlock()
}
func (r *recorder) Log(line string) { r.mu.Lock(); r.logs = append(r.logs, line); r.mu.Unlock() }

func (v *fakeVPS) request() (Request, Options) {
	host, port, _ := net.SplitHostPort(v.addr)
	p := 0
	fmt.Sscan(port, &p)
	req := Request{Host: host, SSHPort: p, User: v.user, Password: v.password, Domain: "vpn.example.com", Port: 9443}
	verifies := 0
	opts := Options{
		Lookup: func(_ context.Context, name string) ([]net.IP, error) {
			if name == "vpn.example.com" {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
			return nil, errors.New("no such host")
		},
		// The first attempt fails, as it does while the certificate is issued.
		Verify: func(string) (int64, error) {
			verifies++
			if verifies == 1 {
				return 0, errors.New("tls: handshake failure")
			}
			return 42, nil
		},
		VerifyEvery: 10 * time.Millisecond,
	}
	return req, opts
}

func TestFreshRootInstall(t *testing.T) {
	v := newFakeVPS(t)
	v.start()
	req, opts := v.request()
	rec := &recorder{}
	res, err := Run(context.Background(), req, opts, rec)
	if err != nil {
		t.Fatalf("Run: %v\nlog: %v", err, rec.logs)
	}
	if res.Existing || res.LatencyMs != 42 || !strings.Contains(res.ConfigYAML, `server: "vpn.example.com:9443"`) {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.HostKey != HostKeyFingerprint(v.key) {
		t.Fatalf("host key %q, want %q", res.HostKey, HostKeyFingerprint(v.key))
	}
	want := []string{"connect:active", "connect:done", "checks:active", "checks:done", "install:active", "install:done", "verify:active", "verify:done"}
	if strings.Join(rec.steps, " ") != strings.Join(want, " ") {
		t.Fatalf("steps %v", rec.steps)
	}
	// The script arrives as sh needs it, whatever the checkout's line endings.
	if strings.Contains(v.uploaded, "\r") || !strings.HasPrefix(v.uploaded, "#!/bin/sh") {
		t.Fatal("uploaded installer is not a clean LF shell script")
	}
	var install string
	for _, c := range v.cmds {
		if strings.Contains(c, "PHANTOM_APP=1") {
			install = c
		}
	}
	if install != "env PHANTOM_APP=1 PHANTOM_DOMAIN=vpn.example.com PHANTOM_PORT=9443 sh /tmp/phantom-install.sh" {
		t.Fatalf("install command %q; all commands: %q", install, v.cmds)
	}
	for _, l := range rec.logs {
		if strings.Contains(l, "\x1b[") {
			t.Fatalf("colour codes leaked into the log: %q", l)
		}
	}
}

func TestNonRootUserGoesThroughSudo(t *testing.T) {
	v := newFakeVPS(t)
	v.user, v.uid = "ubuntu", "1000"
	v.start()
	req, opts := v.request()
	if _, err := Run(context.Background(), req, opts, &recorder{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.cmds {
		if strings.Contains(c, "PHANTOM_APP=1") && !strings.HasPrefix(c, "sudo -S -p '' env PHANTOM_APP=1") {
			t.Fatalf("installer not run through sudo: %q", c)
		}
	}
}

func TestSudoRequiringATerminalIsRetriedWithOne(t *testing.T) {
	v := newFakeVPS(t)
	v.user, v.uid, v.tty = "centos", "1000", true
	v.start()
	req, opts := v.request()
	// checks' own "sudo true" has no terminal either; requiretty hosts refuse
	// that too, so the check must not be what stops the install.
	v.tty = false
	origInstall := v.install
	calls := 0
	v.install = func(cmd string, pty bool) (string, int) {
		calls++
		if !pty {
			return "sudo: sorry, you must have a tty to run sudo\n", 1
		}
		return origInstall(cmd, pty)
	}
	if _, err := Run(context.Background(), req, opts, &recorder{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected a retry on a terminal, installer ran %d times", calls)
	}
}

func TestUserWithoutSudoIsRefusedClearly(t *testing.T) {
	v := newFakeVPS(t)
	v.user, v.uid, v.sudo = "guest", "1001", false
	v.start()
	req, opts := v.request()
	_, err := Run(context.Background(), req, opts, &recorder{})
	assertCode(t, err, CodeNoSudo)
}

func TestExistingInstallIsReadNotReinstalled(t *testing.T) {
	v := newFakeVPS(t)
	v.existing = goodYAML
	v.start()
	req, opts := v.request()
	res, err := Run(context.Background(), req, opts, &recorder{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Existing || !strings.Contains(res.ConfigYAML, "vpn.example.com") {
		t.Fatalf("unexpected result %+v", res)
	}
	for _, c := range v.cmds {
		if strings.Contains(c, "PHANTOM_APP=1") || strings.Contains(c, "phantom-install.sh") {
			t.Fatalf("existing server was reinstalled: %q", c)
		}
	}
}

func TestWrongPassword(t *testing.T) {
	v := newFakeVPS(t)
	v.start()
	req, opts := v.request()
	req.Password = "nope"
	_, err := Run(context.Background(), req, opts, &recorder{})
	assertCode(t, err, CodeAuth)
}

func TestChangedHostKeyIsRefused(t *testing.T) {
	v := newFakeVPS(t)
	v.start()
	req, opts := v.request()
	req.KnownHostKey = "SHA256:somethingelse"
	_, err := Run(context.Background(), req, opts, &recorder{})
	assertCode(t, err, CodeHostKey)
}

func TestDomainPointingElsewhereStopsBeforeInstalling(t *testing.T) {
	v := newFakeVPS(t)
	v.start()
	req, opts := v.request()
	opts.Lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("198.51.100.9")}, nil
	}
	_, err := Run(context.Background(), req, opts, &recorder{})
	assertCode(t, err, CodeDNSMismatch)
	for _, c := range v.cmds {
		if strings.Contains(c, "phantom-install.sh") {
			t.Fatal("installed despite the domain pointing elsewhere")
		}
	}
}

func TestInstallerErrorCodeReachesTheApp(t *testing.T) {
	v := newFakeVPS(t)
	v.install = func(string, bool) (string, int) {
		return "@@PHANTOM step checks\n@@PHANTOM error port80_busy\nERROR: port 80 is already in use\n", 1
	}
	v.start()
	req, opts := v.request()
	rec := &recorder{}
	_, err := Run(context.Background(), req, opts, rec)
	assertCode(t, err, "port80_busy")
	if rec.steps[len(rec.steps)-1] != "install:failed" {
		t.Fatalf("the failing step was not marked: %v", rec.steps)
	}
}

func TestInputIsValidated(t *testing.T) {
	for _, r := range []Request{
		{Host: "1.2.3.4", Password: "x", Domain: "not a domain"},
		{Host: "1.2.3.4", Password: "x", Domain: "a.example; rm -rf /"},
		{Host: "1.2.3.4", Password: "", Domain: "a.example.com"},
		{Host: "1.2.3.4", Password: "x", Domain: "a.example.com", User: "root; id"},
		{Host: "1.2.3.4", Password: "x", Domain: "a.example.com", Port: 80},
	} {
		_, err := Run(context.Background(), r, Options{}, &recorder{})
		assertCode(t, err, CodeInvalid)
	}
}

// The installer shipped inside the apps must be the one in scripts/.
func TestEmbeddedInstallerMatchesScript(t *testing.T) {
	data, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(data), "\r\n", "\n") != InstallScript() {
		t.Fatal("internal/provision/install.sh is out of date - copy scripts/install.sh over it")
	}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("want error code %q, got %v", code, err)
	}
}
