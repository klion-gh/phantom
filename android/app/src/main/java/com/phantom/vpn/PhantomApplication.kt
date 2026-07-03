package com.phantom.vpn

import android.app.Application

class PhantomApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        FileLog.init(this)
        FileLog.i("Application.onCreate")
    }
}
