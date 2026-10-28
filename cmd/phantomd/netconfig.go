//go:build linux

package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// How the machine's traffic is sent into the tunnel - the scheme wg-quick
// uses, which works the same on every distribution because it only touches
// the kernel (over netlink) and never a distribution's network manager:
//
//   - A routing table of phantomd's own whose default route is the TUN device
//     (IPv4 and IPv6).
//   - Rule "not fwmark fwMark -> that table": everything goes into the tunnel
//     except sockets phantomd marked - its connection to the server, smart
//     mode's direct flows, config pings (see markSocket).
//   - Rule "main table, suppress_prefixlength 0", ahead of it: the main table
//     still decides everything except the default route, so the local network
//     (printers, the router's admin page, a NAS) stays reachable directly.
//
// DNS needs one more piece. A resolver on the local network (the router,
// 192.168.1.1) is matched by the main table's own on-link route, so queries
// to it would skip the tunnel - leaking every name looked up, and leaving
// smart mode blind (it learns which addresses belong to a listed site from
// the DNS answers crossing the tunnel). So every unmarked DNS query leaving
// this machine is redirected to the tunnel's resolver address with nftables,
// whatever /etc/resolv.conf, systemd-resolved or NetworkManager say. Queries
// to a local stub (127.0.0.53, 127.0.0.1) are left alone - the stub's own
// upstream queries are what gets redirected.
const (
	fwMark     = 0x5048 // "PH"
	routeTable = 0x5048

	// Below the main table's rule (32766) and clear of the low numbers other
	// software tends to take.
	prioSuppress = 5100
	prioTunnel   = 5101

	// The tunnel's DNS address, where every redirected query lands. Must match
	// mobile.go's fakeDNSServer: the netstack answers queries sent to it from
	// the real upstream (tunnelled or direct, per split DNS).
	tunnelDNS = "10.10.0.1"

	nftTable = "phantom"
)

func installNetworking(ifIndex int) error {
	// wg-quick does the same: without it, strict reverse-path filtering drops
	// replies to marked sockets on some kernels' defaults.
	if err := os.WriteFile("/proc/sys/net/ipv4/conf/all/src_valid_mark", []byte("1"), 0644); err != nil {
		log.Printf("src_valid_mark: %v (continuing)", err)
	}

	if err := netlink.RouteReplace(&netlink.Route{
		LinkIndex: ifIndex,
		Dst:       &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Table:     routeTable,
	}); err != nil {
		return fmt.Errorf("default route into the tunnel: %w", err)
	}
	// IPv6 rides the tunnel too, or it would bypass it entirely. A kernel
	// with IPv6 switched off refuses the route - nothing to leak then.
	ipv6 := true
	if err := netlink.RouteReplace(&netlink.Route{
		LinkIndex: ifIndex,
		Dst:       &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		Table:     routeTable,
	}); err != nil {
		log.Printf("IPv6 default route: %v (IPv6 unavailable, continuing)", err)
		ipv6 = false
	}

	families := []int{unix.AF_INET}
	if ipv6 {
		families = append(families, unix.AF_INET6)
	}
	for _, family := range families {
		for _, r := range policyRules(family) {
			if err := netlink.RuleAdd(r); err != nil {
				return fmt.Errorf("routing rule (family %d, priority %d): %w", family, r.Priority, err)
			}
		}
	}

	if err := installDNSRedirect(); err != nil {
		// Not fatal: traffic is tunnelled either way, and a resolver reached
		// through the default route is still tunnelled. Only an on-link one
		// (the router) would leak - worth a loud line, not a failed connect.
		log.Printf("WARNING: DNS redirect unavailable, queries to a local-network resolver bypass the tunnel: %v", err)
	}
	return nil
}

func policyRules(family int) []*netlink.Rule {
	suppress := netlink.NewRule()
	suppress.Family = family
	suppress.Priority = prioSuppress
	suppress.Table = unix.RT_TABLE_MAIN
	suppress.SuppressPrefixlen = 0

	mask := uint32(0xffffffff)
	tunnel := netlink.NewRule()
	tunnel.Family = family
	tunnel.Priority = prioTunnel
	tunnel.Table = routeTable
	tunnel.Mark = fwMark
	tunnel.Mask = &mask
	tunnel.Invert = true
	return []*netlink.Rule{suppress, tunnel}
}

// cleanupNetworking removes everything installNetworking (and the device)
// set up. Idempotent and quiet: it runs before every connect and at startup,
// mostly finding nothing.
func cleanupNetworking() {
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		for _, r := range policyRules(family) {
			// A crashed instance could have added a rule twice; remove until gone.
			for i := 0; i < 8 && netlink.RuleDel(r) == nil; i++ {
			}
		}
		routes, err := netlink.RouteListFiltered(family, &netlink.Route{Table: routeTable}, netlink.RT_FILTER_TABLE)
		if err == nil {
			for _, rt := range routes {
				netlink.RouteDel(&rt)
			}
		}
	}
	removeDNSRedirect()
	if link, err := netlink.LinkByName(tunName); err == nil {
		netlink.LinkDel(link)
	}
}

// --- DNS redirect (nftables, over netlink - no nft binary needed) -----------

// installDNSRedirect adds, in tables named nftTable:
//
//	ip:  output nat:    meta mark != fwMark, ip daddr != 127.0.0.0/8,
//	                    {udp,tcp} dport 53 -> dnat to tunnelDNS
//	ip6: output filter: meta mark != fwMark, ip6 daddr != ::1,
//	                    {udp,tcp} dport 53 -> reject
//
// IPv6 queries are refused rather than redirected (the tunnel's resolver is
// IPv4-only): a refusal is immediate, so resolvers move on to an IPv4 server
// instead of waiting out a timeout.
func installDNSRedirect() error {
	removeDNSRedirect()
	c, err := nftables.New()
	if err != nil {
		return err
	}

	v4 := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: nftTable})
	nat := c.AddChain(&nftables.Chain{
		Name: "dns", Table: v4,
		Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest,
	})
	for _, proto := range []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP} {
		c.AddRule(&nftables.Rule{Table: v4, Chain: nat, Exprs: concat(
			unmarked(),
			// ip daddr & 255.0.0.0 != 127.0.0.0
			[]expr.Any{
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
				&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{255, 0, 0, 0}, Xor: []byte{0, 0, 0, 0}},
				&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{127, 0, 0, 0}},
			},
			dnsPort(proto),
			[]expr.Any{
				&expr.Immediate{Register: 1, Data: net.ParseIP(tunnelDNS).To4()},
				&expr.NAT{Type: expr.NATTypeDestNAT, Family: unix.NFPROTO_IPV4, RegAddrMin: 1},
			},
		)})
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("ipv4 table: %w", err)
	}

	v6 := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv6, Name: nftTable})
	filter := c.AddChain(&nftables.Chain{
		Name: "dns", Table: v6,
		Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter,
	})
	for _, proto := range []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP} {
		c.AddRule(&nftables.Rule{Table: v6, Chain: filter, Exprs: concat(
			unmarked(),
			[]expr.Any{
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
				&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: net.IPv6loopback.To16()},
			},
			dnsPort(proto),
			[]expr.Any{&expr.Reject{Type: unix.NFT_REJECT_ICMPX_UNREACH, Code: unix.NFT_REJECT_ICMPX_PORT_UNREACH}},
		)})
	}
	if err := c.Flush(); err != nil {
		// The IPv4 half already works; IPv6 resolvers just won't be refused.
		log.Printf("DNS redirect: ipv6 table: %v (continuing)", err)
	}
	return nil
}

func unmarked() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(fwMark)},
	}
}

func dnsPort(proto byte) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(53)},
	}
}

func concat(parts ...[]expr.Any) []expr.Any {
	var out []expr.Any
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func removeDNSRedirect() {
	c, err := nftables.New()
	if err != nil {
		return
	}
	for _, family := range []nftables.TableFamily{nftables.TableFamilyIPv4, nftables.TableFamilyIPv6} {
		c.DelTable(&nftables.Table{Family: family, Name: nftTable})
		// Each delete is flushed on its own: a missing table fails the whole
		// batch it's in, and usually there is nothing to delete at all.
		if err := c.Flush(); err != nil && !errors.Is(err, unix.ENOENT) {
			continue
		}
	}
}
