package com.phantom.vpn

import android.content.Context
import android.util.Log
import java.io.File
import java.io.FileWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/**
 * Plain-file logger so problems can be inspected without adb: the app has no
 * other way to surface diagnostics on a phone with no USB debugging set up.
 * Also installs a global uncaught-exception handler so a startup crash gets
 * written here before the process dies, instead of vanishing.
 *
 * Kept as hourly segment files (logs/phantom-yyyyMMdd-HH.log, UTC) holding only
 * the last [RETENTION_MS] - the same layout as the Windows client's
 * internal/logfile. It used to be one phantom.log appended to forever, which
 * with the diagnostics added since grew without bound; expiring history is now
 * deleting whole files, which can't corrupt the part being kept. The old file
 * is deleted by [init].
 *
 * Everything funnels through here: the app's own lines ([i]/[e]), structured
 * diagnostics ([Diag]), and every line the Go core logs ([go], via
 * Mobile.setLogSink) - so one shared file holds the whole picture.
 */
object FileLog {
    private const val TAG = "Phantom"
    private const val RETENTION_MS = 24L * 60 * 60 * 1000
    private const val HOUR_MS = 60L * 60 * 1000
    // A runaway loop logging non-stop fills at most one segment this big per
    // hour instead of the disk (mirrors maxSegmentBytes on Windows).
    private const val MAX_SEGMENT_BYTES = 20L * 1024 * 1024
    private const val PREFIX = "phantom-"
    private const val SUFFIX = ".log"

    private lateinit var dir: File
    private val lineTime = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US)
    private val segmentStamp = SimpleDateFormat("yyyyMMdd-HH", Locale.US).apply {
        timeZone = TimeZone.getTimeZone("UTC")
    }

    private var currentHour: String? = null
    private var writer: FileWriter? = null
    private var written = 0L
    private var capped = false

    fun init(context: Context) {
        dir = File(context.filesDir, "logs").apply { mkdirs() }
        File(context.filesDir, "phantom.log").delete()
        synchronized(this) { prune(System.currentTimeMillis()) }
        val previousHandler = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            try {
                e("uncaught exception on thread ${thread.name}", throwable)
            } catch (_: Throwable) {
                // never let logging itself take down the crash handler
            }
            previousHandler?.uncaughtException(thread, throwable)
        }
        i("FileLog initialized, writing hourly segments to ${dir.absolutePath}")
    }

    fun i(message: String) = write("I", message, null)
    fun e(message: String, t: Throwable? = null) = write("E", message, t)

    /** Structured diagnostic lines - see [Diag]. */
    fun d(message: String) = write("D", message, null)

    /** A line from the Go core (tunnel, routing, DNS) - see Mobile.setLogSink. */
    fun go(message: String) = write("G", message, null)

    fun path(): String = if (::dir.isInitialized) dir.absolutePath else "(not initialized)"

    /**
     * The newest [maxChars] of the log, starting on a whole line - what the
     * in-app viewer shows. A day of diagnostics is far too much text for one
     * screen to lay out, and the newest part is what's being looked at.
     */
    fun readTail(maxChars: Int): String {
        val files = synchronized(this) { flushAndList() }
        if (files.isEmpty()) return "(no log file yet)"
        val chunks = ArrayDeque<String>()
        var total = 0
        for (f in files.asReversed()) {
            if (total >= maxChars) break
            val text = runCatching { f.readText() }.getOrNull() ?: continue
            chunks.addFirst(text)
            total += text.length
        }
        var s = chunks.joinToString("")
        if (s.length > maxChars) {
            s = s.substring(s.length - maxChars)
            val nl = s.indexOf('\n')
            if (nl >= 0) s = s.substring(nl + 1)
        }
        return s
    }

    /**
     * Writes the whole retained log (the last day) into one file under the
     * cache dir, for sharing as an attachment. Sharing it as intent text, as
     * before, hit Android's ~1 MB binder limit once the log got big - the
     * share sheet then failed outright.
     */
    fun exportForShare(context: Context): File {
        val files = synchronized(this) { flushAndList() }
        val outDir = File(context.cacheDir, "logs").apply { mkdirs() }
        outDir.listFiles()?.forEach { it.delete() } // only ever the latest export
        val stamp = SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(Date())
        val out = File(outDir, "phantom-log-$stamp.txt")
        out.outputStream().use { os ->
            for (f in files) {
                runCatching { f.inputStream().use { it.copyTo(os) } }
            }
        }
        return out
    }

    @Synchronized
    private fun flushAndList(): List<File> {
        runCatching { writer?.flush() }
        return segments()
    }

    private fun write(level: String, message: String, t: Throwable?) {
        when (level) {
            "E" -> Log.e(TAG, message, t)
            "D" -> Log.d(TAG, message)
            else -> Log.i(TAG, message)
        }
        if (!::dir.isInitialized) return
        val now = System.currentTimeMillis()
        val line = buildString {
            synchronized(lineTime) { append(lineTime.format(Date(now))) }
            append(' ').append(level).append(' ').append(message)
            if (t != null) {
                append('\n').append(Log.getStackTraceString(t))
            }
            append('\n')
        }
        synchronized(this) {
            try {
                roll(now)
                if (capped) return
                if (written + line.length > MAX_SEGMENT_BYTES) {
                    capped = true
                    val stamp = synchronized(lineTime) { lineTime.format(Date(now)) }
                    writer?.write("$stamp I logfile: this hour's segment reached " +
                        "${MAX_SEGMENT_BYTES shr 20} MB - dropping further lines until the next hour\n")
                    writer?.flush()
                    return
                }
                writer?.write(line)
                writer?.flush()
                written += line.length
            } catch (_: Throwable) {
                // best-effort only
            }
        }
    }

    /** Switches to the current hour's segment if the hour changed, pruning
     * expired segments as it does. Caller holds the lock. */
    private fun roll(now: Long) {
        val hour = segmentStamp.format(Date(now))
        if (writer != null && hour == currentHour) return
        runCatching { writer?.close() }
        val file = File(dir, "$PREFIX$hour$SUFFIX")
        written = if (file.exists()) file.length() else 0L
        capped = written >= MAX_SEGMENT_BYTES
        writer = FileWriter(file, true)
        currentHour = hour
        prune(now)
    }

    /** Deletes every segment whose whole hour ended more than [RETENTION_MS]
     * ago. A segment still partly inside the window is kept whole, so the log
     * always covers at least the last day and at most one hour more. */
    private fun prune(now: Long) {
        val cutoff = now - RETENTION_MS
        for (f in segments()) {
            val start = hourOf(f) ?: continue
            if (start + HOUR_MS < cutoff) f.delete()
        }
    }

    private fun segments(): List<File> =
        (dir.listFiles() ?: emptyArray())
            .filter { it.isFile && it.name.startsWith(PREFIX) && it.name.endsWith(SUFFIX) && hourOf(it) != null }
            .sortedBy { hourOf(it) }

    private fun hourOf(f: File): Long? = runCatching {
        synchronized(segmentStamp) {
            segmentStamp.parse(f.name.removePrefix(PREFIX).removeSuffix(SUFFIX))?.time
        }
    }.getOrNull()
}
