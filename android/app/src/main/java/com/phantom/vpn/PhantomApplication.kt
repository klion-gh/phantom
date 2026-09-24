package com.phantom.vpn

import android.app.Application
import mobile.LogSink
import mobile.Mobile

class PhantomApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        FileLog.init(this)
        // Every line the Go core logs (tunnel stalls, dropped connections,
        // DNS going unanswered, routing decisions) lands in the same file as
        // the app's own - without this it went only to logcat, invisible on a
        // phone without adb. Set before anything can start the core.
        Mobile.setLogSink(object : LogSink {
            override fun log(line: String) = FileLog.go(line)
        })
        // First line of every session's diagnostics - device/emulator, API
        // level and app version, which every rendering or connectivity report
        // has to be read against.
        Diag.logEnvironment()
        // Load the UI language early (before any Activity or the VpnService), so
        // both Compose and the service's notifications render in the right one.
        I18n.load(this)
        // Loaded before any composable reads a colour, so the first frame is
        // already in the user's chosen theme instead of flashing the default.
        Appearance.load(this)
        // Same reasoning, plus the VpnService reads these when it builds a
        // tunnel - which can happen from a notification action without any
        // Activity ever having been created.
        RoutingStore.load(this)
        RoutingController.sync(this)
        Diag.log(
            Diag.Cat.ROUTE, "startupState",
            "smartEnabled" to RoutingStore.smartEnabled,
            "autoEnabled" to RoutingStore.autoEnabled,
            "smartSites" to RoutingStore.sites.size,
            "smartConfigs" to RoutingStore.smartConfigIds.size,
            "autoConfigs" to RoutingStore.autoConfigIds.size,
        )
        FileLog.i("Application.onCreate")
    }
}
