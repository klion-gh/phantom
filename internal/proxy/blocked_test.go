package proxy

import (
	"net"
	"strings"
	"testing"
)

// TestResolveAllowedRefusesLocalTargets pins the server-side SSRF guard: a client
// holding the PSK must not be able to reach the server's own loopback services or
// the cloud metadata endpoint through the tunnel.
func TestResolveAllowedRefusesLocalTargets(t *testing.T) {
	for _, tc := range []struct {
		target string
		refuse bool
		why    string
	}{
		{"127.0.0.1:22", true, "loopback by literal - an SSH daemon bound to localhost"},
		{"127.0.0.53:53", true, "systemd-resolved's stub listener"},
		{"[::1]:8080", true, "IPv6 loopback"},
		{"localhost:5432", true, "loopback by name"},
		{"169.254.169.254:80", true, "cloud metadata - returns VPS credentials on most providers"},
		{"0.0.0.0:80", true, "unspecified"},
		{"[::]:80", true, "unspecified, IPv6"},

		{"1.1.1.1:53", false, "ordinary public address"},
		{"192.168.1.1:80", false, "RFC1918 stays allowed on purpose - reaching one's own LAN is a real use case"},
		{"10.0.0.5:22", false, "RFC1918"},
		{"[2606:4700:4700::1111]:53", false, "ordinary public IPv6"},
	} {
		_, err := resolveAllowed(tc.target, false)
		refused := err != nil && strings.Contains(err.Error(), "refused")
		if refused != tc.refuse {
			t.Errorf("resolveAllowed(%q): refused=%v want %v (%s) [err=%v]",
				tc.target, refused, tc.refuse, tc.why, err)
		}
	}
}

// TestResolveAllowedReturnsDialableAddress is what closes the DNS-rebinding hole:
// the caller must get back a concrete address to dial, so there is no second
// lookup that could disagree with the one that passed the check.
func TestResolveAllowedReturnsDialableAddress(t *testing.T) {
	addrs, err := resolveAllowed("1.1.1.1:53", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 1 {
		t.Fatalf("got %d addresses, want 1", len(addrs))
	}
	host, port, err := net.SplitHostPort(addrs[0])
	if err != nil {
		t.Fatalf("returned address %q is not dialable: %v", addrs[0], err)
	}
	if net.ParseIP(host) == nil {
		t.Fatalf("returned host %q is not an IP literal - a name here would be resolved a second time", host)
	}
	if port != "53" {
		t.Fatalf("port %q lost, want 53", port)
	}
}

// TestResolveAllowedPreservesIPv6Bracketing guards the JoinHostPort detail: an
// unbracketed IPv6 address plus port is not dialable.
func TestResolveAllowedPreservesIPv6Bracketing(t *testing.T) {
	addrs, err := resolveAllowed("[2606:4700:4700::1111]:53", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(addrs[0], "[") {
		t.Fatalf("IPv6 address came back unbracketed: %q", addrs[0])
	}
	if _, _, err := net.SplitHostPort(addrs[0]); err != nil {
		t.Fatalf("%q is not dialable: %v", addrs[0], err)
	}
}

// TestResolveAllowedOptOut checks the escape hatch for an operator who genuinely
// wants clients reaching services on the VPN host.
func TestResolveAllowedOptOut(t *testing.T) {
	addrs, err := resolveAllowed("127.0.0.1:22", true)
	if err != nil {
		t.Fatalf("AllowLocalTargets did not disable the guard: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != "127.0.0.1:22" {
		t.Fatalf("got %v, want [127.0.0.1:22]", addrs)
	}
}

// TestResolveAllowedRejectsMalformedTarget: a target that doesn't parse must fail
// here rather than reaching the dialer.
func TestResolveAllowedRejectsMalformedTarget(t *testing.T) {
	if _, err := resolveAllowed("this is not a target", false); err == nil {
		t.Error("a malformed target was accepted")
	}
}
