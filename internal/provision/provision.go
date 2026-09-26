// Package provision sets up a Phantom server from nothing but what a VPS
// host hands out - an IP address, a login and a password - over SSH, and
// returns the client config for the new server. It backs "Подключить свой
// сервер" in both apps.
//
// The steps, each reported to a Listener as it starts and finishes:
//
//	connect  SSH in with the password (no keys: that is all a fresh VPS gives
//	         you); remember the server's host key the first time and refuse a
//	         different one after that.
//	checks   root, or a user sudo accepts the same password for; the domain
//	         actually points at this server (the most common mistake, and one
//	         Let's Encrypt would otherwise report much later and much worse).
//	install  scripts/install.sh, embedded in the app so it always matches it,
//	         run with PHANTOM_APP=1: no prompts, "@@PHANTOM" marker lines for
//	         progress, a failure code, and the finished client.yaml.
//	verify   one real Phantom handshake with that config, retried while the
//	         server fetches its certificate on the first connection.
//
// A server that already runs Phantom is not reinstalled - that would replace
// its keys and cut off every device already using it - its existing
// client.yaml is read back instead.
package provision

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"phantom/internal/config"
)

//go:embed install.sh
var installScript string

// Request is what the user typed.
type Request struct {
	Host     string `json:"host"`     // IP address (or name) of the server
	SSHPort  int    `json:"sshPort"`  // 0 means 22
	User     string `json:"user"`     // "" means root
	Password string `json:"password"` // SSH password; also given to sudo for a non-root user
	Domain   string `json:"domain"`   // must already point at Host
	Port     int    `json:"port"`     // Phantom's port, 0 means 8443
	// KnownHostKey is the host key fingerprint remembered from an earlier
	// connection to this server ("" the first time). A different key is
	// refused: either the server was reinstalled or someone is in between.
	KnownHostKey string `json:"knownHostKey"`
}

// Result is the new (or existing) server's config.
type Result struct {
	ConfigYAML string `json:"yaml"`
	HostKey    string `json:"hostKey"`  // to remember for next time
	Existing   bool   `json:"existing"` // Phantom was already installed
	LatencyMs  int64  `json:"latencyMs"`
}

// Error carries a stable code the apps translate, plus detail for the log.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

func fail(code, format string, a ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, a...)}
}

// Codes, besides the ones install.sh reports (not_root, no_curl, no_systemd,
// unsupported_arch, bad_port, port_busy, port80_busy, download_failed,
// checksum, keygen, service_failed, cert_missing, no_domain).
const (
	CodeInvalid       = "invalid_input"
	CodeUnreachable   = "unreachable"
	CodeAuth          = "auth_failed"
	CodeHostKey       = "host_key_changed"
	CodeNoSudo        = "no_sudo"
	CodeDNSMismatch   = "dns_mismatch"
	CodeDNSFailed     = "dns_failed"
	CodeExistingNoCfg = "existing_no_config"
	CodeInstall       = "install_failed"
	CodeVerify        = "verify_failed"
	CodeCancelled     = "cancelled"
)

// Steps.
const (
	StepConnect = "connect"
	StepChecks  = "checks"
	StepInstall = "install"
	StepVerify  = "verify"
)

// Listener receives progress. State is "active", "done" or "failed".
type Listener interface {
	Step(step, state string)
	Log(line string)
}

// Options are the platform's hooks.
type Options struct {
	// Dial opens the TCP connection to the SSH server - on a device whose own
	// VPN is up, one that goes around it (see the apps' ping paths).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Lookup resolves the domain for the DNS check, likewise around the VPN.
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
	// Verify performs one real Phantom handshake with the finished config and
	// reports its latency.
	Verify func(configYAML string) (int64, error)
	// VerifyFor bounds how long Verify is retried; 0 means 90s. The first
	// connection to a new server is what makes it request its certificate.
	VerifyFor time.Duration
	// VerifyEvery is the pause between attempts; 0 means 3s.
	VerifyEvery time.Duration
}

const (
	installDir    = "/opt/phantom"
	remoteScript  = "/tmp/phantom-install.sh"
	installBudget = 8 * time.Minute
)

var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

func (r *Request) normalize() error {
	r.Host = strings.TrimSpace(r.Host)
	r.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Domain), "."))
	r.User = strings.TrimSpace(r.User)
	if r.User == "" {
		r.User = "root"
	}
	if r.SSHPort == 0 {
		r.SSHPort = 22
	}
	if r.Port == 0 {
		r.Port = 8443
	}
	switch {
	case r.Host == "" || strings.ContainsAny(r.Host, " /@"):
		return fail(CodeInvalid, "host")
	case !userRe.MatchString(r.User):
		return fail(CodeInvalid, "user")
	case r.Password == "":
		return fail(CodeInvalid, "password")
	case !domainRe.MatchString(r.Domain):
		return fail(CodeInvalid, "domain")
	case r.SSHPort < 1 || r.SSHPort > 65535:
		return fail(CodeInvalid, "sshPort")
	case r.Port < 1 || r.Port > 65535 || r.Port == 80:
		return fail(CodeInvalid, "port")
	}
	return nil
}

// Run sets up the server (or reads back the one already there). It returns
// an *Error on failure.
func Run(ctx context.Context, req Request, opts Options, l Listener) (Result, error) {
	if err := req.normalize(); err != nil {
		return Result{}, err
	}
	p := &run{ctx: ctx, req: req, opts: opts, l: l}
	res, err := p.do()
	if err != nil && ctx.Err() != nil {
		err = fail(CodeCancelled, "")
	}
	if p.step != "" && err != nil {
		l.Step(p.step, "failed")
	}
	return res, err
}

type run struct {
	ctx     context.Context
	req     Request
	opts    Options
	l       Listener
	client  *ssh.Client
	hostKey string
	root    bool
	step    string
}

func (p *run) begin(step string) {
	if p.step != "" {
		p.l.Step(p.step, "done")
	}
	p.step = step
	p.l.Step(step, "active")
}

func (p *run) do() (Result, error) {
	p.begin(StepConnect)
	if err := p.connect(); err != nil {
		return Result{}, err
	}
	defer p.client.Close()
	// Closing the connection is what interrupts a command still running on
	// the server when the user cancels.
	stop := context.AfterFunc(p.ctx, func() { p.client.Close() })
	defer stop()

	p.begin(StepChecks)
	existing, err := p.checks()
	if err != nil {
		return Result{}, err
	}

	var yaml string
	p.begin(StepInstall)
	if existing {
		p.l.Log("Phantom уже установлен на этом сервере - подключаю существующую конфигурацию")
		if yaml, err = p.readExisting(); err != nil {
			return Result{}, err
		}
	} else if yaml, err = p.install(); err != nil {
		return Result{}, err
	}

	p.begin(StepVerify)
	latency, err := p.verify(yaml)
	if err != nil {
		return Result{}, err
	}
	p.l.Step(StepVerify, "done")
	p.step = ""
	return Result{ConfigYAML: yaml, HostKey: p.hostKey, Existing: existing, LatencyMs: latency}, nil
}

// HostKeyFingerprint formats a host key the way OpenSSH prints it.
func HostKeyFingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func (p *run) connect() error {
	addr := net.JoinHostPort(p.req.Host, strconv.Itoa(p.req.SSHPort))
	dialCtx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer cancel()
	dial := p.opts.Dial
	if dial == nil {
		d := net.Dialer{}
		dial = d.DialContext
	}
	conn, err := dial(dialCtx, "tcp", addr)
	if err != nil {
		return fail(CodeUnreachable, "%v", err)
	}
	password := p.req.Password
	cfg := &ssh.ClientConfig{
		User: p.req.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			// Some hosts only offer keyboard-interactive; its one question is
			// the password.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := HostKeyFingerprint(key)
			if p.req.KnownHostKey != "" && p.req.KnownHostKey != fp {
				return fail(CodeHostKey, "expected %s, got %s", p.req.KnownHostKey, fp)
			}
			p.hostKey = fp
			return nil
		},
		Timeout: 15 * time.Second,
	}
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		var he *Error
		if errors.As(err, &he) {
			return he
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return fail(CodeAuth, "%v", err)
		}
		return fail(CodeUnreachable, "%v", err)
	}
	_ = conn.SetDeadline(time.Time{})
	p.client = ssh.NewClient(c, chans, reqs)
	p.l.Log(fmt.Sprintf("Подключено к %s как %s (ключ сервера %s)", addr, p.req.User, p.hostKey))
	return nil
}

// exec runs one command, feeding stdin, and returns its combined output.
func (p *run) exec(cmd string, stdin string) (string, error) {
	s, err := p.client.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	// Separate buffers: the session copies stdout and stderr on two
	// goroutines, and one shared bytes.Buffer would be written concurrently.
	var stdout, stderr bytes.Buffer
	s.Stdout = &stdout
	s.Stderr = &stderr
	if stdin != "" {
		s.Stdin = strings.NewReader(stdin)
	}
	err = s.Run(cmd)
	return stdout.String() + stderr.String(), err
}

// asRoot wraps a command so it runs as root: as is for root, through sudo
// (password on stdin, no prompt text) otherwise. Everything interpolated into
// commands here is validated or fixed, never raw user input.
func (p *run) asRoot(cmd string) (string, string) {
	if p.root {
		return cmd, ""
	}
	return "sudo -S -p '' " + cmd, p.req.Password + "\n"
}

func (p *run) checks() (bool, error) {
	out, err := p.exec("id -u", "")
	if err != nil {
		return false, fail(CodeInstall, "id -u: %v %s", err, out)
	}
	p.root = strings.TrimSpace(out) == "0"
	if !p.root {
		cmd, in := p.asRoot("true")
		if out, err := p.exec(cmd, in); err != nil {
			return false, fail(CodeNoSudo, "%s", strings.TrimSpace(out))
		}
		p.l.Log(fmt.Sprintf("Пользователь %s не root - команды пойдут через sudo", p.req.User))
	}

	cmd, in := p.asRoot("test -f " + installDir + "/server.yaml")
	if _, err := p.exec(cmd, in); err == nil {
		return true, nil
	}

	// The domain has to point at this server before Let's Encrypt will issue
	// its certificate. Checked from here, the way the apps will look it up.
	serverIPs, err := p.lookup(p.req.Host)
	if err != nil {
		return false, fail(CodeDNSFailed, "%s: %v", p.req.Host, err)
	}
	domainIPs, err := p.lookup(p.req.Domain)
	if err != nil {
		return false, fail(CodeDNSFailed, "%s: %v", p.req.Domain, err)
	}
	if !overlap(serverIPs, domainIPs) {
		// Language-neutral: the apps put it inside their own sentence.
		return false, fail(CodeDNSMismatch, "%s → %s", p.req.Domain, joinIPs(domainIPs))
	}
	p.l.Log(fmt.Sprintf("Домен %s указывает на этот сервер", p.req.Domain))
	return false, nil
}

func (p *run) lookup(host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ctx, cancel := context.WithTimeout(p.ctx, 8*time.Second)
	defer cancel()
	if p.opts.Lookup != nil {
		return p.opts.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func overlap(a, b []net.IP) bool {
	for _, x := range a {
		for _, y := range b {
			if x.Equal(y) {
				return true
			}
		}
	}
	return false
}

func joinIPs(ips []net.IP) string {
	s := make([]string, len(ips))
	for i, ip := range ips {
		s[i] = ip.String()
	}
	return strings.Join(s, ", ")
}

func (p *run) readExisting() (string, error) {
	cmd, in := p.asRoot("cat " + installDir + "/client.yaml.example")
	out, err := p.exec(cmd, in)
	if err != nil || !looksLikeClientConfig(out) {
		return "", fail(CodeExistingNoCfg, "%s", strings.TrimSpace(out))
	}
	return strings.TrimSpace(out) + "\n", nil
}

func looksLikeClientConfig(s string) bool {
	cfg, err := config.ParseClientConfig([]byte(s))
	return err == nil && cfg.Domain != "" && len(cfg.ServerList()) > 0
}

// InstallScript is the installer as uploaded: the embedded copy with any
// CRLF line endings (a Windows checkout) turned back into LF, which is what
// sh needs.
func InstallScript() string {
	return strings.ReplaceAll(installScript, "\r\n", "\n")
}

func (p *run) install() (string, error) {
	if _, err := p.exec("cat > "+remoteScript, InstallScript()); err != nil {
		return "", fail(CodeInstall, "upload: %v", err)
	}
	defer p.exec("rm -f "+remoteScript, "")

	env := fmt.Sprintf("env PHANTOM_APP=1 PHANTOM_DOMAIN=%s PHANTOM_PORT=%d sh %s", p.req.Domain, p.req.Port, remoteScript)
	cmd, in := p.asRoot(env)

	ctx, cancel := context.WithTimeout(p.ctx, installBudget)
	defer cancel()
	res, err := p.stream(ctx, cmd, in, false)
	if err == nil && res.needTTY && !p.root {
		// sudoers with "requiretty": the same again, on a pseudo-terminal.
		p.l.Log("sudo требует терминал - повторяю с ним")
		res, err = p.stream(ctx, cmd, in, true)
	}
	if err != nil {
		return "", err
	}
	if res.code != "" {
		return "", fail(res.code, "%s", res.lastLine)
	}
	if !res.exitOK || !looksLikeClientConfig(res.yaml) {
		return "", fail(CodeInstall, "%s", res.lastLine)
	}
	return res.yaml, nil
}

type streamResult struct {
	yaml     string
	code     string
	lastLine string
	exitOK   bool
	needTTY  bool
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stream runs the installer, forwarding its log line by line and picking out
// the marker lines.
func (p *run) stream(ctx context.Context, cmd, stdin string, pty bool) (streamResult, error) {
	s, err := p.client.NewSession()
	if err != nil {
		return streamResult{}, fail(CodeInstall, "session: %v", err)
	}
	defer s.Close()
	if pty {
		if err := s.RequestPty("dumb", 40, 200, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
			return streamResult{}, fail(CodeInstall, "pty: %v", err)
		}
	}
	pr, pw := io.Pipe()
	s.Stdout = pw
	s.Stderr = pw
	if stdin != "" {
		s.Stdin = strings.NewReader(stdin)
	}
	if err := s.Start(cmd); err != nil {
		return streamResult{}, fail(CodeInstall, "start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait(); pw.Close() }()
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()

	var res streamResult
	var yaml strings.Builder
	inYAML := false
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(ansiRe.ReplaceAllString(sc.Text(), ""), "\r")
		switch {
		case strings.HasPrefix(line, "@@PHANTOM "):
			f := strings.Fields(strings.TrimPrefix(line, "@@PHANTOM "))
			switch {
			case len(f) >= 2 && f[0] == "error":
				res.code = f[1]
			case len(f) >= 2 && f[0] == "step":
				p.l.Log("── " + stepTitle(f[1]))
			case len(f) == 1 && f[0] == "client-begin":
				inYAML = true
			case len(f) == 1 && f[0] == "client-end":
				inYAML = false
			}
		case inYAML:
			yaml.WriteString(line + "\n")
		default:
			if strings.Contains(line, "must have a tty to run sudo") || strings.Contains(line, "no tty present") {
				res.needTTY = true
			}
			if strings.TrimSpace(line) != "" {
				res.lastLine = line
				p.l.Log(line)
			}
		}
	}
	err = <-done
	if ctx.Err() != nil && p.ctx.Err() == nil {
		return res, fail(CodeInstall, "timed out after %s", installBudget)
	}
	res.exitOK = err == nil
	res.yaml = yaml.String()
	return res, nil
}

func stepTitle(s string) string {
	switch s {
	case "checks":
		return "Проверка системы"
	case "download":
		return "Загрузка сервера Phantom"
	case "keys":
		return "Создание ключей"
	case "service":
		return "Запуск службы"
	}
	return s
}

func (p *run) verify(yaml string) (int64, error) {
	if p.opts.Verify == nil {
		return 0, nil
	}
	budget := p.opts.VerifyFor
	if budget == 0 {
		budget = 90 * time.Second
	}
	every := p.opts.VerifyEvery
	if every == 0 {
		every = 3 * time.Second
	}
	deadline := time.Now().Add(budget)
	var lastErr error
	for attempt := 1; ; attempt++ {
		latency, err := p.opts.Verify(yaml)
		if err == nil {
			p.l.Log(fmt.Sprintf("Сервер отвечает: %d ms", latency))
			return latency, nil
		}
		lastErr = err
		if attempt == 1 {
			p.l.Log("Сервер получает сертификат - жду…")
		}
		if time.Now().After(deadline) {
			return 0, fail(CodeVerify, "%v", lastErr)
		}
		select {
		case <-p.ctx.Done():
			return 0, fail(CodeCancelled, "")
		case <-time.After(every):
		}
	}
}
