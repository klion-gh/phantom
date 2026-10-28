package main

import (
	"fmt"
	"log"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Watches for the underlying physical network changing out from under an
// active tunnel (Ethernet <-> Wi-Fi, a different Wi-Fi network taking over,
// etc.) via NotifyRouteChange2 - a native, event-driven Windows notification,
// not a polling loop - the same category of mechanism WireGuard's own Windows
// client and most mature VPN clients use on every platform (ConnectivityManager
// on Android, SCNetworkReachability on macOS/iOS, NetworkManager D-Bus signals
// on Linux). See PhantomVpnService.kt's registerNetworkCallback for the
// Android side of the same fix.
//
// The routing table changes for lots of reasons that don't matter here (a
// route to some unrelated subnet appearing/disappearing), so every callback
// just re-checks findDefaultGateway() after a short debounce and only acts if
// the *default* gateway actually differs from the one captured when the
// tunnel connected - that's the real signal that this process's own dialed
// connections are now bound to a dead interface.

var (
	iphlpapiRoute              = syscall.NewLazyDLL("iphlpapi.dll")
	procNotifyRouteChange2     = iphlpapiRoute.NewProc("NotifyRouteChange2")
	procCancelMibChangeNotify2 = iphlpapiRoute.NewProc("CancelMibChangeNotify2")
)

// The callback trampoline is created exactly once and reused for the whole
// process lifetime - syscall.NewCallback documents that only a limited
// number of these can ever be created, and a long-running app could
// reconnect (and thus re-watch) many times over its uptime.
var (
	routeCallbackOnce sync.Once
	routeCallbackPtr  uintptr
)

var (
	routeWatchMu       sync.Mutex
	routeWatchHandle   windows.Handle
	routeWatchGateway  string
	routeWatchOnChange func()
	routeWatchTimer    *time.Timer
)

// routeChangeCallback matches PIPFORWARD_CHANGE_CALLBACK's C signature
// exactly (three pointer-sized args, one pointer-sized result) - the row and
// notification-type details are deliberately ignored, since any change is
// just a cue to re-run findDefaultGateway() ourselves rather than something
// worth hand-parsing MIB_IPFORWARD_ROW2 for.
func routeChangeCallback(callerContext, row, notificationType uintptr) uintptr {
	routeWatchMu.Lock()
	if routeWatchOnChange != nil {
		if routeWatchTimer != nil {
			routeWatchTimer.Stop()
		}
		// Debounced, not instant - a real handover fires a burst of route
		// notifications in quick succession while things settle, and checking
		// (let alone reconnecting) on every single one would just be noise.
		routeWatchTimer = time.AfterFunc(1500*time.Millisecond, checkGatewayChanged)
	}
	routeWatchMu.Unlock()
	return 0
}

func checkGatewayChanged() {
	routeWatchMu.Lock()
	expected := routeWatchGateway
	onChange := routeWatchOnChange
	routeWatchMu.Unlock()
	if onChange == nil {
		return
	}

	current, err := findDefaultGateway()
	if err != nil {
		// Transient - e.g. briefly no network at all mid-handover. The next
		// route-change notification (there will be one once a network comes
		// back) will trigger another check.
		return
	}
	if current != expected {
		log.Printf("network change detected: gateway %s -> %s, reconnecting", expected, current)
		onChange()
	}
}

// startWatchingRouteChanges begins watching for routing-table changes,
// calling onChange (already debounced, and only once the default gateway has
// actually changed from initialGateway) when the physical network genuinely
// changes. Safe to call repeatedly - each call replaces any previous watch.
func startWatchingRouteChanges(initialGateway string, onChange func()) error {
	stopWatchingRouteChanges()

	routeCallbackOnce.Do(func() {
		routeCallbackPtr = syscall.NewCallback(routeChangeCallback)
	})

	routeWatchMu.Lock()
	routeWatchGateway = initialGateway
	routeWatchOnChange = onChange
	routeWatchMu.Unlock()

	var handle windows.Handle
	// NotifyRouteChange2(AddressFamily, Callback, CallerContext, InitialNotification, *NotificationHandle).
	// CallerContext is unused (0) - state is kept in the package-level vars
	// above instead of threaded through the C callback context, which avoids
	// any question of passing a Go pointer through non-Go-aware code.
	// InitialNotification=FALSE: we don't want a synthetic upfront callback
	// for the route table as it already stands right now.
	ret, _, _ := procNotifyRouteChange2.Call(
		uintptr(windows.AF_INET),
		routeCallbackPtr,
		0,
		0,
		uintptr(unsafe.Pointer(&handle)),
	)
	if ret != 0 {
		routeWatchMu.Lock()
		routeWatchOnChange = nil
		routeWatchMu.Unlock()
		return fmt.Errorf("NotifyRouteChange2 failed: code %d", ret)
	}

	routeWatchMu.Lock()
	routeWatchHandle = handle
	routeWatchMu.Unlock()
	return nil
}

// stopWatchingRouteChanges cancels any active watch and any pending debounced
// check. Safe to call even if nothing was ever started.
func stopWatchingRouteChanges() {
	routeWatchMu.Lock()
	handle := routeWatchHandle
	routeWatchHandle = 0
	routeWatchOnChange = nil
	if routeWatchTimer != nil {
		routeWatchTimer.Stop()
		routeWatchTimer = nil
	}
	routeWatchMu.Unlock()

	if handle != 0 {
		procCancelMibChangeNotify2.Call(uintptr(handle))
	}
}
