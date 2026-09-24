package com.phantom.vpn

import android.os.Build
import android.util.Log

/**
 * Structured diagnostic logging, separate from [FileLog] (which stays the
 * user-facing "Посмотреть лог" feed and shouldn't be drowned in per-frame
 * render traces).
 *
 * Everything here writes `KEY=value` pairs under one logcat tag so a whole
 * session can be pulled and parsed mechanically:
 *
 *     adb logcat -d -s PhantomDiag
 *     adb logcat -d -s PhantomDiag | grep "cat=GLASS"
 *
 * [ENABLED] is the single switch for the whole layer. Diagnostic builds ship
 * with it on; flip it off to compile every call below down to a cheap boolean
 * check (the message strings are built inside lambdas, so they aren't even
 * constructed when disabled).
 */
object Diag {
    const val ENABLED = true

    private const val TAG = "PhantomDiag"

    /** Categories, so a noisy area can be grepped out without losing the rest. */
    object Cat {
        const val APP = "APP"       // process/activity lifecycle, one-time environment facts
        const val GLASS = "GLASS"   // Эффект прозрачности: state, per-tile geometry, blur params
        const val BG = "BG"         // animated backdrop: style changes, clock, canvas sizing
        const val UI = "UI"         // screen/section navigation, dialogs, settings changes
        const val VPN = "VPN"       // tunnel lifecycle, connect/disconnect, network changes
        const val ROUTE = "ROUTE"   // smart routing, auto-select, per-config probes
    }

    /**
     * One structured line. [fields] are appended as `k=v` pairs in order.
     * Kept as a vararg of pairs rather than a map so call sites stay short and
     * ordering is stable across lines (which makes diffing two runs readable).
     */
    fun log(category: String, event: String, vararg fields: Pair<String, Any?>) {
        if (!ENABLED) return
        val line = buildString {
            append("cat=").append(category)
            append(" ev=").append(event)
            for ((k, v) in fields) {
                append(' ').append(k).append('=').append(format(v))
            }
        }
        Log.d(TAG, line)
        // Into the shareable log too: on a phone with no adb, logcat alone
        // is invisible, and these are exactly the lines a bug report needs.
        FileLog.d(line)
    }

    /**
     * Per-frame call sites (draw scopes, animation ticks) use this instead:
     * it drops everything but roughly one line per [everyMs] per [key], so a
     * 60fps redraw doesn't produce 60 lines a second and push everything else
     * out of logcat's ring buffer.
     */
    private val lastEmit = mutableMapOf<String, Long>()

    fun sampled(key: String, category: String, event: String, everyMs: Long = 2000, fields: () -> Array<out Pair<String, Any?>>) {
        if (!ENABLED) return
        val now = System.currentTimeMillis()
        val last = synchronized(lastEmit) { lastEmit[key] }
        if (last != null && now - last < everyMs) return
        synchronized(lastEmit) { lastEmit[key] = now }
        log(category, event, *fields())
    }

    /** Dumped once at startup - most "why does it look different on my device"
     *  questions start with one of these values. */
    fun logEnvironment() {
        if (!ENABLED) return
        log(
            Cat.APP, "environment",
            "sdk" to Build.VERSION.SDK_INT,
            "release" to Build.VERSION.RELEASE,
            "manufacturer" to Build.MANUFACTURER,
            "model" to Build.MODEL,
            "product" to Build.PRODUCT,
            "hardware" to Build.HARDWARE,
            // "ranchu"/"goldfish" here means the Android emulator, whose GPU
            // path differs enough from real hardware to matter for blur.
            "isEmulator" to (Build.HARDWARE.contains("ranchu") || Build.HARDWARE.contains("goldfish") || Build.FINGERPRINT.contains("generic")),
            "appVersion" to BuildConfig.VERSION_NAME,
        )
    }

    private fun format(v: Any?): String = when (v) {
        null -> "null"
        is Float -> String.format("%.1f", v)
        is Double -> String.format("%.1f", v)
        else -> v.toString()
    }
}
