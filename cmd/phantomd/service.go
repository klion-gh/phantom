//go:build linux

package main

import (
	"errors"
	"log"
	"sync"
	"time"

	"phantom/internal/daemonapi"
	"phantom/internal/pingcheck"
)

// service is the RPC receiver - see internal/daemonapi for the contract.
//
// opMu serialises the calls that build or tear down a tunnel (a connect can
// take 15 seconds); mu only guards the fields, so Status keeps answering
// instantly while a connect is in flight and the window's poll never stalls.
type service struct {
	opMu sync.Mutex

	mu       sync.Mutex
	tun      *linuxTunnel
	configID string
	routing  daemonapi.RoutingArgs
}

func newService() *service { return &service{} }

// seedWait bounds how long SetRouting waits for a new site list's addresses -
// the window resets misrouted flows right after, and a name that never
// resolves shouldn't hold up the ones that did.
const seedWait = 4 * time.Second

func (s *service) Connect(args daemonapi.ConnectArgs, _ *daemonapi.Empty) error {
	if args.YAML == "" {
		return errors.New("empty configuration")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	old := s.tun
	s.tun, s.configID = nil, ""
	s.routing = args.Routing
	s.mu.Unlock()
	if old != nil {
		old.Stop()
	}

	log.Printf("connect: config %s (smart %v, %d sites)", args.ConfigID, args.Routing.Smart, len(args.Routing.Sites))
	start := time.Now()
	t, err := startLinuxTunnel(args.YAML, args.Routing)
	if err != nil {
		log.Printf("connect failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return err
	}
	log.Printf("connected in %s", time.Since(start).Round(time.Millisecond))

	s.mu.Lock()
	s.tun, s.configID = t, args.ConfigID
	s.mu.Unlock()
	return nil
}

func (s *service) Disconnect(_ daemonapi.Empty, _ *daemonapi.Empty) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	t := s.tun
	s.tun, s.configID = nil, ""
	s.mu.Unlock()
	if t != nil {
		t.Stop()
		log.Printf("disconnected")
	}
	return nil
}

func (s *service) Status(_ daemonapi.Empty, reply *daemonapi.StatusReply) error {
	s.mu.Lock()
	t, id := s.tun, s.configID
	s.mu.Unlock()
	*reply = daemonapi.StatusReply{Version: daemonapi.Version, ConfigID: id}
	if t != nil {
		reply.Connected = true
		reply.Alive = t.IsAlive()
		reply.Stats = t.Stats()
	}
	return nil
}

func (s *service) SetRouting(args daemonapi.RoutingArgs, _ *daemonapi.Empty) error {
	s.mu.Lock()
	s.routing = args
	t := s.tun
	s.mu.Unlock()
	if t != nil {
		t.SetRouting(args, seedWait)
	}
	return nil
}

func (s *service) ResetMisroutedFlows(_ daemonapi.Empty, _ *daemonapi.Empty) error {
	s.mu.Lock()
	t := s.tun
	s.mu.Unlock()
	if t != nil {
		t.ResetMisroutedFlows()
	}
	return nil
}

// Ping always goes around the tunnel (the socket carries phantomd's routing
// mark): a config's ping is a statement about the path from this machine to
// that server, not from the current server to it - see pingcheck.Options.
func (s *service) Ping(args daemonapi.PingArgs, reply *daemonapi.PingReply) error {
	result, err := pingcheck.PingWith(args.YAML, pingcheck.Options{ProtectFD: markSocket})
	if err != nil {
		return err
	}
	*reply = daemonapi.PingReply{IP: result.IP, LatencyMs: result.LatencyMs}
	return nil
}

// shutdown tears the tunnel down on SIGTERM, so stopping the service (or the
// machine) restores normal networking instead of leaving rules behind.
func (s *service) shutdown() {
	s.Disconnect(daemonapi.Empty{}, nil)
	cleanupNetworking()
}
