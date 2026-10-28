//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"

	"phantom/internal/daemonapi"
)

// runCtl drives a running phantomd from a terminal - for diagnosing a machine
// without the window, and for the release workflow's end-to-end test. Needs
// no root: the socket is open to local users, like the window's own access.
//
//	phantomd status
//	phantomd connect <client.yaml> [--smart site1,site2,...]
//	phantomd disconnect
//	phantomd ping <client.yaml>
func runCtl(socket string, args []string) int {
	c, err := daemonapi.DialPath(socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer c.Close()

	switch args[0] {
	case "status":
		st, err := c.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("protocol v%d connected=%v alive=%v config=%q\n%s\n", st.Version, st.Connected, st.Alive, st.ConfigID, st.Stats)
		if st.Connected && !st.Alive {
			return 2
		}
		return 0

	case "connect":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: phantomd connect <client.yaml> [--smart site1,site2,...]")
			return 1
		}
		yaml, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		var routing daemonapi.RoutingArgs
		if len(args) >= 4 && args[2] == "--smart" {
			routing = daemonapi.RoutingArgs{Smart: true, Sites: strings.Split(args[3], ",")}
		}
		if err := c.Connect(daemonapi.ConnectArgs{ConfigID: "cli", YAML: string(yaml), Routing: routing}); err != nil {
			fmt.Fprintln(os.Stderr, "connect:", err)
			return 1
		}
		fmt.Println("connected")
		return 0

	case "disconnect":
		if err := c.Disconnect(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("disconnected")
		return 0

	case "ping":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: phantomd ping <client.yaml>")
			return 1
		}
		yaml, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		r, err := c.Ping(string(yaml))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ping:", err)
			return 1
		}
		fmt.Printf("%s %d ms\n", r.IP, r.LatencyMs)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command %q (status, connect, disconnect, ping)\n", args[0])
	return 1
}
