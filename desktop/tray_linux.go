package main

import (
	_ "embed"

	"github.com/godbus/dbus/v5"
)

// A small PNG: the tray protocol on Linux (StatusNotifierItem over D-Bus)
// ships the icon as raw pixels, so the 512px app icon would be a megabyte per
// update.
//
//go:embed build/linux/tray.png
var trayIconBytes []byte

// trayAvailable reports whether the session has a tray to show the icon in:
// a StatusNotifierItem host (KDE, Xfce, Cinnamon, MATE, GNOME with the
// AppIndicator extension, ...). Without a session bus the tray library would
// crash the process, and without a host the icon would silently never appear
// - so the window then minimises instead of hiding (see App.beforeClose).
func trayAvailable() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	var hasOwner bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&hasOwner)
	return err == nil && hasOwner
}
