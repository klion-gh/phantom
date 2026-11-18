//go:build !windows

package mobile

// AppOwner tells the tunnel whether a connection belongs to one of the apps
// listed under "Приложения под обход". Implemented on the Android side with
// ConnectivityManager.getConnectionOwnerUid. proto is 6 (TCP) or 17 (UDP);
// source and target are "ip:port", source being the app's own end.
type AppOwner interface {
	IsListed(proto int, source string, target string) bool
}

// SetAppRouting installs the listed-apps check: in Умный VPN, a flow the
// site list sends direct still goes through the tunnel when owner says it
// belongs to a listed app. count is how many apps are listed; 0 (or a nil
// owner) turns the check off.
func (t *Tunnel) SetAppRouting(count int, owner AppOwner) {
	if t.engine == nil {
		return
	}
	if owner == nil || count <= 0 {
		t.engine.SetApps(0, nil)
		return
	}
	t.engine.SetApps(count, func(network, source, target string) bool {
		proto := 6
		if network == "udp" {
			proto = 17
		}
		return owner.IsListed(proto, source, target)
	})
}

// SetAdBlock turns "Блокировать рекламу YouTube" on or off for this tunnel -
// see internal/routing/adblock.go. Live: the next DNS query sees it.
func (t *Tunnel) SetAdBlock(on bool) {
	if t.engine != nil {
		t.engine.SetAdBlock(on)
	}
}
