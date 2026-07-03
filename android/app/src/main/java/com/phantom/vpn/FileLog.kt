package com.phantom.vpn

import android.content.Context
import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Plain-file logger so crashes/errors can be inspected without adb: the app
 * has no other way to surface diagnostics on a phone with no USB debugging
 * set up. Also installs a global uncaught-exception handler so a startup
 * crash gets written here before the process dies, instead of vanishing.
 */
object FileLog {
    private const val TAG = "Phantom"
    private lateinit var logFile: File
    private val timeFormat = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US)

    fun init(context: Context) {
        logFile = File(context.filesDir, "phantom.log")
        val previousHandler = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            try {
                e("uncaught exception on thread ${thread.name}", throwable)
            } catch (_: Throwable) {
                // never let logging itself take down the crash handler
            }
            previousHandler?.uncaughtException(thread, throwable)
        }
        i("FileLog initialized, writing to ${logFile.absolutePath}")
    }

    fun i(message: String) = write("I", message, null)
    fun e(message: String, t: Throwable? = null) = write("E", message, t)

    fun readAll(): String =
        if (::logFile.isInitialized && logFile.exists()) logFile.readText() else "(no log file yet)"

    fun path(): String = if (::logFile.isInitialized) logFile.absolutePath else "(not initialized)"

    private fun write(level: String, message: String, t: Throwable?) {
        val line = buildString {
            append(timeFormat.format(Date()))
            append(" ")
            append(level)
            append(" ")
            append(message)
            if (t != null) {
                append("\n")
                append(Log.getStackTraceString(t))
            }
            append("\n")
        }
        if (level == "E") Log.e(TAG, message, t) else Log.i(TAG, message)
        try {
            if (::logFile.isInitialized) {
                logFile.appendText(line)
            }
        } catch (_: Throwable) {
            // best-effort only
        }
    }
}
