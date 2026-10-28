//go:build linux

// phantomd is the privileged half of the Linux desktop client: it owns the
// tunnel - the TUN device, the routing rules that send the machine's traffic
// into it, the DNS redirect - and the window drives it over a unix socket
// (see internal/daemonapi for why the two are separate processes).
//
// It is started by systemd (phantomd.service), or once per session through
// pkexec by the window itself where there is no systemd. Either way it runs
// as root, and it does nothing on its own until a client asks it to connect.
package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"phantom/internal/daemonapi"
)

func main() {
	socketPath := flag.String("socket", daemonapi.SocketPath, "unix socket to listen on (or, with a command, to talk to)")
	flag.Parse()
	if flag.NArg() > 0 {
		os.Exit(runCtl(*socketPath, flag.Args()))
	}

	// stderr is the journal under systemd; timestamps in milliseconds so a
	// connect, a network change and a reconnect can be lined up.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("phantomd starting (protocol v%d, uid %d)", daemonapi.Version, os.Geteuid())

	// Whatever a previous instance left behind if it was killed mid-session -
	// rules pointing at a device that no longer exists would leave the machine
	// without a working route for anything but the main table.
	cleanupNetworking()

	svc := newService()
	server := rpc.NewServer()
	if err := server.RegisterName(daemonapi.ServiceName, svc); err != nil {
		log.Fatalf("register rpc: %v", err)
	}

	ln, err := listen(*socketPath)
	if err != nil {
		log.Fatalf("listen on %s: %v", *socketPath, err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-stop
		log.Printf("%v - disconnecting and exiting", sig)
		svc.shutdown()
		ln.Close()
		os.Remove(*socketPath)
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept: %v", err)
			continue
		}
		logPeer(conn)
		go server.ServeCodec(jsonrpc.NewServerCodec(conn))
	}
}

// listen opens the socket every local user's window can reach. The service
// is a per-machine VPN, like NetworkManager's connections: whoever is sitting
// at the desktop drives it. Each client's uid is logged (see logPeer).
func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	os.Remove(path) // a stale socket from a killed instance refuses the bind
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func logPeer(conn net.Conn) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return
	}
	raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil {
			log.Printf("client connected: pid %d uid %d", cred.Pid, cred.Uid)
		}
	})
}
