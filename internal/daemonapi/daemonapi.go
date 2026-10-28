// Package daemonapi is the contract between the Linux desktop client and
// phantomd, the privileged service that owns its tunnel.
//
// Why two processes: on Linux, creating a TUN device, changing routes and
// installing the DNS redirect all need CAP_NET_ADMIN, and the window can't
// hold it - GTK (under WebKitGTK, under Wails) refuses to start in a process
// with elevated rights, file capabilities included. So the tunnel lives in
// phantomd (run by systemd as root, or started once through pkexec where there
// is no systemd) and the window drives it over a unix socket.
//
// The protocol is net/rpc with the JSON codec: one long-lived connection per
// client, calls are synchronous, and every type here is plain data. phantomd
// keeps the tunnel and its routing engine; everything else - saved configs,
// the smart selector, settings - stays in the window, exactly as on Windows.
package daemonapi

import (
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"time"
)

// SocketPath is where phantomd listens. /run is tmpfs, so a stale socket
// never survives a reboot; systemd's RuntimeDirectory= creates /run/phantom.
const SocketPath = "/run/phantom/phantomd.sock"

// Version is bumped whenever the calls below change incompatibly, so a window
// and a service from different releases notice instead of misbehaving.
const Version = 1

// ServiceName is the net/rpc receiver name the calls are registered under.
const ServiceName = "Phantom"

// Empty is the argument or reply of calls that carry none.
type Empty struct{}

// ConnectArgs starts (or replaces) the tunnel. ConfigID is opaque to the
// service: it is only handed back by Status, so a window started while a
// tunnel is already up knows which config it belongs to.
type ConnectArgs struct {
	ConfigID string
	YAML     string
	Routing  RoutingArgs
}

// RoutingArgs is the smart-VPN state. Smart false means everything rides the
// tunnel; Sites is kept either way so turning it on needs nothing else.
type RoutingArgs struct {
	Smart bool
	Sites []string
}

// StatusReply describes the service and its tunnel.
type StatusReply struct {
	Version   int
	Connected bool
	Alive     bool
	ConfigID  string
	Stats     string
}

// PingArgs asks for one disguised handshake against a config's server, made
// from outside the tunnel - see pingcheck.Options.
type PingArgs struct {
	YAML string
}

type PingReply struct {
	IP        string
	LatencyMs int64
}

// Client is a connection to phantomd. Safe for concurrent use.
type Client struct {
	rpc *rpc.Client
}

// ErrNotRunning means nothing is listening on the socket.
var ErrNotRunning = errors.New("phantomd is not running")

// Dial connects to phantomd at SocketPath.
func Dial() (*Client, error) { return DialPath(SocketPath) }

// DialPath connects to phantomd listening at path.
func DialPath(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
		}
		return nil, err
	}
	return &Client{rpc: jsonrpc.NewClient(conn)}, nil
}

func (c *Client) Close() error { return c.rpc.Close() }

// call is rpc.Call with a deadline: a wedged service must not hang the
// window's binding call (and with it the UI) forever.
func (c *Client) call(method string, args, reply any, timeout time.Duration) error {
	call := c.rpc.Go(ServiceName+"."+method, args, reply, make(chan *rpc.Call, 1))
	select {
	case done := <-call.Done:
		return done.Error
	case <-time.After(timeout):
		return fmt.Errorf("phantomd: %s timed out after %s", method, timeout)
	}
}

// Connect blocks until the tunnel is up or has failed; the server dial alone
// may take 15s, so the deadline leaves room for it.
func (c *Client) Connect(args ConnectArgs) error {
	return c.call("Connect", args, &Empty{}, 40*time.Second)
}

func (c *Client) Disconnect() error {
	return c.call("Disconnect", Empty{}, &Empty{}, 15*time.Second)
}

func (c *Client) Status() (StatusReply, error) {
	var r StatusReply
	err := c.call("Status", Empty{}, &r, 5*time.Second)
	return r, err
}

// SetRouting returns once the new site list's addresses are resolved (or a
// few seconds have passed), so a ResetMisroutedFlows right after it acts on
// the whole list.
func (c *Client) SetRouting(args RoutingArgs) error {
	return c.call("SetRouting", args, &Empty{}, 15*time.Second)
}

func (c *Client) ResetMisroutedFlows() error {
	return c.call("ResetMisroutedFlows", Empty{}, &Empty{}, 5*time.Second)
}

func (c *Client) Ping(yaml string) (PingReply, error) {
	var r PingReply
	err := c.call("Ping", PingArgs{YAML: yaml}, &r, 20*time.Second)
	return r, err
}
