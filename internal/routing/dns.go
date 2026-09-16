package routing

import (
	"io"
	"net"

	"golang.org/x/net/dns/dnsmessage"
)

// SniffDNS wraps a UDP flow that carries DNS (destination port 53) so every
// response passing back through it teaches set which IPs belong to which name.
//
// Why this is needed at all: by the time an app opens a TCP connection, it has
// already resolved the name and is dialing a bare IP - the domain the user
// actually listed is long gone. The DNS answer that produced that IP, however,
// crosses the same tunnel moments earlier, so watching it is what makes
// "route youtube.com through the VPN" expressible at the IP level.
//
// This reads the bytes as they stream past rather than intercepting anything:
// the message is parsed on its way to the app, unchanged either way, so a
// malformed or unparseable response is simply not learned from.
//
// Limits worth knowing: an app doing its own DNS-over-HTTPS/TLS resolves
// nothing through here, so its flows can only be matched by a literal IP/CIDR
// entry. That is inherent to any client that isn't a full resolver, not
// specific to this implementation.
func SniffDNS(inner io.ReadWriteCloser, set *DomainSet) io.ReadWriteCloser {
	return &dnsSniffer{inner: inner, set: set}
}

type dnsSniffer struct {
	inner io.ReadWriteCloser
	set   *DomainSet
}

func (d *dnsSniffer) Read(p []byte) (int, error) {
	n, err := d.inner.Read(p)
	if n > 0 {
		// One Read is one datagram here (see netstack's spliceUDP), so the
		// buffer holds exactly one complete DNS message - no framing needed.
		learnFromResponse(p[:n], d.set)
	}
	return n, err
}

func (d *dnsSniffer) Write(p []byte) (int, error) { return d.inner.Write(p) }
func (d *dnsSniffer) Close() error                { return d.inner.Close() }

// learnFromResponse pulls the A/AAAA records out of one DNS response and hands
// them to the set, keyed by the name that was actually asked about.
//
// CNAME chains are handled by keying everything on the *question* name: if the
// user listed "youtube.com" and the answer is a CNAME to some CDN host with
// the addresses attached to that alias, those addresses are still learned for
// youtube.com, which is what the user meant.
func learnFromResponse(msg []byte, set *DomainSet) {
	var parser dnsmessage.Parser
	header, err := parser.Start(msg)
	if err != nil || !header.Response {
		return
	}
	question, err := parser.Question()
	if err != nil {
		return
	}
	name := question.Name.String()
	if !set.MatchesName(name) {
		return
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return
	}

	var ips []net.IP
	for {
		answer, err := parser.AnswerHeader()
		if err != nil {
			break
		}
		switch answer.Type {
		case dnsmessage.TypeA:
			r, err := parser.AResource()
			if err != nil {
				return
			}
			ip := make(net.IP, len(r.A))
			copy(ip, r.A[:])
			ips = append(ips, ip)
		case dnsmessage.TypeAAAA:
			r, err := parser.AAAAResource()
			if err != nil {
				return
			}
			ip := make(net.IP, len(r.AAAA))
			copy(ip, r.AAAA[:])
			ips = append(ips, ip)
		default:
			if err := parser.SkipAnswer(); err != nil {
				return
			}
		}
	}
	set.Learn(name, ips)
}
