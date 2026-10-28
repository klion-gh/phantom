package main

// The seam between the shared desktop client and each OS.
//
// Everything above it - saved configs, the smart selector, settings, the
// browser bridge, server setup, the Wails bindings the UI calls - is one
// codebase. Below it, Windows runs the tunnel in this very process (it is
// started elevated, see tunnel_windows.go), while Linux keeps it in the
// privileged phantomd service and talks to it over a unix socket
// (tunnel_linux.go): GTK refuses to start with elevated rights, so the window
// itself can never hold them there.
//
// Each platform file provides:
//
//	startTunnel(configID, configYAML, onNetworkChanged) (activeTunnel, error)
//	adoptRunningTunnel() (activeTunnel, configID string)
//	applyRoutingPlatform(sites, smart) <-chan struct{}
//	pingConfig(configYAML) (pingcheck.Result, error)
//	provisionProtect() func(fd int) bool
//	platformCommand(args) bool / platformPreflight() bool / platformOptions(opts, app)
//	trayIconBytes, logDir()
//	updateAssetName(), applyUpdate(...), cleanupAfterUpdate()

// activeTunnel is the App's handle on a running tunnel.
type activeTunnel interface {
	Stop()
	Stats() string
	IsAlive() bool
	// ResetMisroutedFlows closes direct connections the current smart-VPN
	// list would now tunnel - see applySiteList.
	ResetMisroutedFlows()
}
