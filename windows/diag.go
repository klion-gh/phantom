package main

import (
	"fmt"
	"log"
	"strings"
)

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

// diag writes one structured line. fields are alternating key, value - a
// vararg of `any` rather than a map so call sites stay short and field order
// is stable across lines, which is what makes two runs diffable.
func diag(category, event string, fields ...any) {
	if !diagEnabled {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cat=%s ev=%s", category, event)
	for i := 0; i+1 < len(fields); i += 2 {
		fmt.Fprintf(&b, " %v=%v", fields[i], fields[i+1])
	}
	// Odd trailing field would silently vanish above; surface it instead of
	// hiding a call-site typo.
	if len(fields)%2 != 0 {
		fmt.Fprintf(&b, " MALFORMED_TRAILING=%v", fields[len(fields)-1])
	}
	log.Print(b.String())
}
