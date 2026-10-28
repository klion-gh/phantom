package main

import idiag "phantom/internal/diag"

// Structured diagnostic logging, the Windows counterpart to Android's Diag.kt
// and deliberately the same shape: `cat=X ev=Y k=v ...` lines, so one session
// from either client can be read (and grepped) the same way.
//
// These go through the standard `log` package like everything else here, so
// they land in phantom.log next to the exe (see log.go) and show up in the
// in-app log viewer - no separate sink to collect.
//
//	findstr "cat=GLASS" phantom.log
//	findstr "cat=VPN" phantom.log
//
// diagEnabled is the single switch for the whole layer; diagnostic builds ship
// with it on.
const diagEnabled = true

// Categories - kept in sync with Android's Diag.Cat so the two clients' logs
// stay comparable side by side.
const (
	diagCatApp   = "APP"   // process start, one-time environment facts
	diagCatBG    = "BG"    // animated backdrop style/canvas (reported from the frontend)
	diagCatUI    = "UI"    // navigation, dialogs, settings changes (reported from the frontend)
	diagCatVPN   = "VPN"   // tunnel lifecycle, connect/disconnect, network changes
	diagCatRoute = "ROUTE" // smart routing, auto-select, per-config probes
)

// diag writes one structured line through internal/diag, which the core
// packages use too - one formatter, so a value with spaces or "=" is quoted
// the same way whichever side logged it.
func diag(category, event string, fields ...any) {
	if !diagEnabled {
		return
	}
	idiag.Event(category, event, fields...)
}
