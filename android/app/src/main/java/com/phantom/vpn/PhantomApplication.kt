package com.phantom.vpn

import android.app.Application

class PhantomApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        FileLog.init(this)
        // Load the UI language early (before any Activity or the VpnService), so
        // both Compose and the service's notifications render in the right one.
        I18n.load(this)
        FileLog.i("Application.onCreate")
    }
}
