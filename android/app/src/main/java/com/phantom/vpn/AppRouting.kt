package com.phantom.vpn

import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.drawable.BitmapDrawable
import android.graphics.drawable.Drawable
import android.net.ConnectivityManager
import android.os.Build
import android.os.Process
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import java.net.InetAddress
import java.net.InetSocketAddress
import java.util.concurrent.ConcurrentHashMap

/** One installed app as the picker shows it. */
data class InstalledApp(val packageName: String, val label: String)

/**
 * "Приложения под обход": apps whose every connection goes through the tunnel
 * in Умный VPN, whatever site it is to.
 *
 * All traffic already reaches the tunnel's netstack in smart mode (the VPN
 * routes 0.0.0.0/0), which then decides per flow. For a flow the site list
 * sends direct, the Go side asks [Owner] whether it came from a listed app -
 * answered with [ConnectivityManager.getConnectionOwnerUid], which the active
 * VPN app is allowed to call (Android 10+; the tile is hidden below that).
 */
object AppRouting {

    /** getConnectionOwnerUid exists from Android 10. */
    val supported: Boolean get() = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q

    /** The tunnel's question, answered from the connection table. */
    class Owner(context: Context) : mobile.AppOwner {
        private val appContext = context.applicationContext
        private val cm = appContext.getSystemService(ConnectivityManager::class.java)
        private val pm = appContext.packageManager
        // A uid's packages don't change while the app is installed, and one
        // app opens many connections in a burst - so one lookup per uid.
        private val packagesByUid = ConcurrentHashMap<Int, Array<String>>()

        override fun isListed(proto: Long, source: String, target: String): Boolean {
            if (!supported) return false
            val listed = RoutingStore.apps
            if (listed.isEmpty()) return false
            val local = parse(source) ?: return false
            val remote = parse(target) ?: return false
            val uid = runCatching { cm.getConnectionOwnerUid(proto.toInt(), local, remote) }
                .getOrDefault(Process.INVALID_UID)
            if (uid == Process.INVALID_UID || uid < 0) return false
            val packages = packagesByUid.getOrPut(uid) {
                runCatching { pm.getPackagesForUid(uid) }.getOrNull() ?: emptyArray()
            }
            return packages.any { it in listed }
        }

        private fun parse(hostPort: String): InetSocketAddress? {
            val colon = hostPort.lastIndexOf(':')
            if (colon <= 0) return null
            val host = hostPort.substring(0, colon).removePrefix("[").removeSuffix("]")
            val port = hostPort.substring(colon + 1).toIntOrNull() ?: return null
            return runCatching { InetSocketAddress(InetAddress.getByName(host), port) }.getOrNull()
        }
    }

    /**
     * Apps the user can pick: everything with a launcher icon, this one left
     * out, sorted by name. Read off the main thread - a few hundred labels.
     */
    fun installedApps(context: Context): List<InstalledApp> {
        val pm = context.packageManager
        val intent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        val activities = runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                pm.queryIntentActivities(intent, PackageManager.ResolveInfoFlags.of(0))
            } else {
                @Suppress("DEPRECATION")
                pm.queryIntentActivities(intent, 0)
            }
        }.getOrDefault(emptyList())
        return activities
            .map { it.activityInfo.packageName to it.loadLabel(pm).toString() }
            .filter { it.first != context.packageName }
            .distinctBy { it.first }
            .map { InstalledApp(it.first, it.second) }
            .sortedBy { it.label.lowercase() }
    }

    fun label(context: Context, packageName: String): String = runCatching {
        val pm = context.packageManager
        pm.getApplicationLabel(pm.getApplicationInfo(packageName, 0)).toString()
    }.getOrDefault(packageName)

    private val iconCache = ConcurrentHashMap<String, ImageBitmap>()

    /** The app's icon as a bitmap for Compose, or null if it's gone. */
    fun icon(context: Context, packageName: String): ImageBitmap? {
        iconCache[packageName]?.let { return it }
        val drawable = runCatching { context.packageManager.getApplicationIcon(packageName) }.getOrNull()
            ?: return null
        val bitmap = drawable.toBitmap(96).asImageBitmap()
        iconCache[packageName] = bitmap
        return bitmap
    }

    private fun Drawable.toBitmap(size: Int): Bitmap {
        if (this is BitmapDrawable && bitmap != null) {
            return Bitmap.createScaledBitmap(bitmap, size, size, true)
        }
        val bmp = Bitmap.createBitmap(size, size, Bitmap.Config.ARGB_8888)
        val canvas = Canvas(bmp)
        setBounds(0, 0, size, size)
        draw(canvas)
        return bmp
    }
}
