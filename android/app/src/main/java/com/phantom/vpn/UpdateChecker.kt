package com.phantom.vpn

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.core.content.FileProvider
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

private const val GITHUB_RELEASES_API = "https://api.github.com/repos/klion-gh/phantom/releases/latest"
private const val APK_ASSET_NAME = "phantom.apk"
private const val UPDATE_APK_FILE_NAME = "phantom-update.apk"
private const val PREFS_NAME = "phantom_update_prefs"
private const val PREF_DOWNLOADED_TAG = "downloaded_tag"

/** What checkForUpdate found: the release tag (for display) and the direct download
 * URL for its phantom.apk asset. */
data class UpdateInfo(val tag: String, val downloadUrl: String)

/**
 * Checks GitHub for a release newer than [currentVersion] (pass
 * BuildConfig.VERSION_NAME), returning its tag and phantom.apk download URL, or null
 * if already current, offline, rate-limited, or the release has no matching asset -
 * never treated as an error worth surfacing.
 */
suspend fun checkForUpdate(currentVersion: String): UpdateInfo? = withContext(Dispatchers.IO) {
    runCatching {
        val conn = URL(GITHUB_RELEASES_API).openConnection() as HttpURLConnection
        conn.connectTimeout = 6000
        conn.readTimeout = 6000
        conn.setRequestProperty("Accept", "application/vnd.github+json")
        val body = conn.inputStream.bufferedReader().use { it.readText() }
        val json = JSONObject(body)
        val tag = json.optString("tag_name").takeIf { it.isNotBlank() } ?: return@runCatching null
        if (!isNewerVersion(tag, currentVersion)) return@runCatching null

        val assets = json.optJSONArray("assets") ?: return@runCatching null
        for (i in 0 until assets.length()) {
            val asset = assets.getJSONObject(i)
            if (asset.optString("name") == APK_ASSET_NAME) {
                val url = asset.optString("browser_download_url").takeIf { it.isNotBlank() } ?: return@runCatching null
                return@runCatching UpdateInfo(tag, url)
            }
        }
        FileLog.i("update check: release $tag has no $APK_ASSET_NAME asset")
        null
    }.getOrNull()
}

/** Numeric "vX.Y.Z"/"X.Y.Z" comparison - a plain string compare would treat "1.9.0" as
 * newer than "1.10.0". */
private fun isNewerVersion(latest: String, current: String): Boolean {
    val l = parseVersion(latest)
    val c = parseVersion(current)
    for (i in 0..2) {
        if (l[i] != c[i]) return l[i] > c[i]
    }
    return false
}

private fun parseVersion(v: String): IntArray {
    val parts = v.trim().removePrefix("v").split(".")
    return IntArray(3) { i -> parts.getOrNull(i)?.trim()?.toIntOrNull() ?: 0 }
}

/** True once Android will actually let this app hand another APK to the installer
 * without a detour through Settings first - granted per-app, persists across app
 * restarts/updates, so this is only ever false on a genuinely first-ever attempt. */
fun canInstallPackages(context: Context): Boolean {
    return Build.VERSION.SDK_INT < Build.VERSION_CODES.O || context.packageManager.canRequestPackageInstalls()
}

/** Sends the user to the one-time "allow Phantom to install apps" toggle - there's no
 * way to skip this system screen or grant it programmatically. */
fun requestInstallPermission(context: Context) {
    val intent = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${context.packageName}"))
    context.startActivity(intent)
}

private fun updateApkFile(context: Context): File = File(context.getExternalFilesDir(null), UPDATE_APK_FILE_NAME)

/** Whether [info]'s APK is already sitting on disk from a previous attempt - e.g. the
 * user dismissed the install prompt without installing it, or the app was
 * backgrounded mid-flow - so downloadAndInstallUpdate can skip straight to
 * re-launching the installer instead of fetching the whole thing again. */
fun isUpdateAlreadyDownloaded(context: Context, info: UpdateInfo): Boolean {
    val file = updateApkFile(context)
    if (!file.exists() || file.length() <= 0L) return false
    val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
    return prefs.getString(PREF_DOWNLOADED_TAG, null) == info.tag
}

/**
 * Downloads [info]'s APK (skipping the download entirely if isUpdateAlreadyDownloaded
 * already returned true for it) and hands it to the system installer. Android never
 * lets a non-Play-Store app install silently - this still ends at a native "install
 * this update?" confirmation the user has to tap, same as any sideloaded APK - but
 * this gets them straight to that one tap instead of a browser download plus manually
 * finding the file. Returns false if the download itself failed (network, disk); the
 * install step failing/being cancelled by the user isn't observable from here.
 */
suspend fun downloadAndInstallUpdate(context: Context, info: UpdateInfo): Boolean {
    val file = updateApkFile(context)

    if (!isUpdateAlreadyDownloaded(context, info)) {
        val downloaded = withContext(Dispatchers.IO) {
            runCatching {
                val conn = URL(info.downloadUrl).openConnection() as HttpURLConnection
                conn.connectTimeout = 10_000
                conn.readTimeout = 120_000
                conn.instanceFollowRedirects = true
                conn.inputStream.use { input ->
                    file.outputStream().use { output -> input.copyTo(output) }
                }
                true
            }.getOrElse {
                FileLog.e("update download failed", it)
                file.delete()
                false
            }
        }
        if (!downloaded) return false
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .edit().putString(PREF_DOWNLOADED_TAG, info.tag).apply()
    }

    if (!canInstallPackages(context)) {
        requestInstallPermission(context)
        return true // downloaded fine; installing is up to the user granting the permission and trying again
    }

    val uri = FileProvider.getUriForFile(context, "${context.packageName}.fileprovider", file)
    val intent = Intent(Intent.ACTION_VIEW).apply {
        setDataAndType(uri, "application/vnd.android.package-archive")
        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
    context.startActivity(intent)
    return true
}
