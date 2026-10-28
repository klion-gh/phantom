package main

import _ "embed"

//go:embed build/windows/icon.ico
var trayIconBytes []byte

// trayAvailable: the Windows notification area is always there.
func trayAvailable() bool { return true }
