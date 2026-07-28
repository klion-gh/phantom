package proxy

import "testing"

// TestBlockedTarget pins the server-side SSRF guard: a client holding the PSK
// must not be able to reach the server's own loopback services or the cloud
// metadata endpoint through the tunnel.
func TestBlockedTarget(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   bool
		why    string
	}{
		{"127.0.0.1:22", true, "loopback by literal - an SSH daemon bound to localhost"},
		{"127.0.0.53:53", true, "systemd-resolved's stub listener"},
		{"[::1]:8080", true, "IPv6 loopback"},
		{"localhost:5432", true, "loopback by name"},
		{"169.254.169.254:80", true, "cloud metadata - returns VPS credentials on most providers"},
		{"0.0.0.0:80", true, "unspecified"},

		{"1.1.1.1:53", false, "ordinary public address"},
		{"example.com:443", false, "ordinary public name"},
		{"192.168.1.1:80", false, "RFC1918 stays allowed on purpose - reaching one's own LAN is a real use case"},
		{"10.0.0.5:22", false, "RFC1918"},
	} {
		if _, got := blockedTarget(tc.target, false); got != tc.want {
			t.Errorf("blockedTarget(%q) = %v, want %v (%s)", tc.target, got, tc.want, tc.why)
		}
	}
}

// TestBlockedTargetOptOut checks the escape hatch for an operator who genuinely
// wants clients reaching services on the VPN host.
func TestBlockedTargetOptOut(t *testing.T) {
	if _, blocked := blockedTarget("127.0.0.1:22", true); blocked {
		t.Error("AllowLocalTargets did not disable the guard")
	}
}

// TestMalformedTargetIsNotBlocked: a target that doesn't even parse should fall
// through to the dial and fail there, not be silently swallowed by the guard.
func TestMalformedTargetIsNotBlocked(t *testing.T) {
	if _, blocked := blockedTarget("this is not a target", false); blocked {
		t.Error("a malformed target should fail at dial, not be reported as blocked")
	}
}
