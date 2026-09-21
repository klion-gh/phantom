package com.phantom.vpn

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.core.content.FileProvider
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

// Two endpoints, one per update channel - the split is GitHub's own, not
// something this app invents on top of it:
//
//  - /releases/latest deliberately skips anything marked as a prerelease (and
//    any draft), so it is exactly the stable channel with no filtering needed.
//  - /releases lists everything, newest first, each entry carrying its own
//    prerelease/draft flags - which is what the beta channel reads, taking the
//    newest non-draft whether it's marked prerelease or not (a beta user should
//    still get a stable release that supersedes the last beta).
//
// So publishing a beta is just ticking "Set as a pre-release" on the GitHub
// release - no special asset names, no parallel tagging scheme to keep in sync.
private const val GITHUB_LATEST_RELEASE_API = "https://api.github.com/repos/klion-gh/phantom/releases/latest"
private const val GITHUB_RELEASE_LIST_API = "https://api.github.com/repos/klion-gh/phantom/releases?per_page=20"
private const val APK_ASSET_NAME = "phantom.apk"
private const val UPDATE_APK_FILE_NAME = "phantom-update.apk"
private const val PREFS_NAME = "phantom_update_prefs"
private const val PREF_DOWNLOADED_TAG = "downloaded_tag"

/** What checkForUpdate found: the release tag (for display), the direct download
 * URL for its phantom.apk asset, and whether GitHub marks it as a prerelease -
 * the last so the UI can say "beta" rather than presenting a test build as an
 * ordinary update. */
data class UpdateInfo(val tag: String, val downloadUrl: String, val prerelease: Boolean = false)

/**
 * Checks GitHub for a release newer than [currentVersion] (pass
 * BuildConfig.VERSION_NAME), returning its tag and phantom.apk download URL, or null
 * if already current, offline, rate-limited, or the release has no matching asset -
 * never treated as an error worth surfacing.
 *
 * [allowBeta] picks the channel (see the endpoint constants above): false reads
 * only stable releases, true takes the newest published release of either kind.
 */
suspend fun checkForUpdate(currentVersion: String, allowBeta: Boolean = false): UpdateInfo? = withContext(Dispatchers.IO) {
    runCatching {
        val endpoint = if (allowBeta) GITHUB_RELEASE_LIST_API else GITHUB_LATEST_RELEASE_API
        val conn = URL(endpoint).openConnection() as HttpURLConnection
        conn.connectTimeout = 6000
        conn.readTimeout = 6000
        conn.setRequestProperty("Accept", "application/vnd.github+json")
        val body = conn.inputStream.bufferedReader().use { it.readText() }

        // The list endpoint returns an array, newest first; drafts are skipped
        // (a draft's assets are still being attached - see the release
        // workflow, which publishes one deliberately). The latest endpoint
        // returns a single object that is already the right one.
        val json = if (allowBeta) {
            val arr = JSONArray(body)
            (0 until arr.length())
                .map { arr.getJSONObject(it) }
                .firstOrNull { !it.optBoolean("draft", false) }
                ?: return@runCatching null
        } else {
            JSONObject(body)
        }

        val tag = json.optString("tag_name").takeIf { it.isNotBlank() } ?: return@runCatching null
        val prerelease = json.optBoolean("prerelease", false)
        if (!isNewerVersion(tag, currentVersion)) return@runCatching null
        Diag.log(
            Diag.Cat.APP, "updateFound",
            "tag" to tag, "prerelease" to prerelease,
            "channel" to if (allowBeta) "beta" else "stable",
            "current" to currentVersion,
        )

        val assets = json.optJSONArray("assets") ?: return@runCatching null
        for (i in 0 until assets.length()) {
            val asset = assets.getJSONObject(i)
            if (asset.optString("name") == APK_ASSET_NAME) {
                val url = asset.optString("browser_download_url").takeIf { it.isNotBlank() } ?: return@runCatching null
                return@runCatching UpdateInfo(tag, url, prerelease)
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
    // Drop any prerelease/build suffix ("1.18.0-beta.2" -> "1.18.0") so a beta
    // tag compares on its numeric version alone. Consequence worth knowing: a
    // beta must carry a numerically higher version than the stable it
    // supersedes, because "1.18.0-beta.1" and "1.18.0" compare equal here -
    // which is right for "the stable of the same version isn't an update for
    // someone already on its beta", but means betas can't be tagged off the
    // current stable's own number.
    val numeric = v.trim().removePrefix("v").substringBefore('-').substringBefore('+')
    val parts = numeric.split(".")
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

/** Streams input to output in chunks, reporting percent complete as it goes -
 * total <= 0 (server didn't send Content-Length) just means onProgress never
 * fires, the copy itself is unaffected. */
private fun copyWithProgress(
    input: java.io.InputStream,
    output: java.io.OutputStream,
    total: Long,
    onProgress: (percent: Int) -> Unit,
) {
    val buffer = ByteArray(8 * 1024)
    var copied = 0L
    var lastPct = -1
    while (true) {
        val n = input.read(buffer)
        if (n < 0) break
        output.write(buffer, 0, n)
        copied += n
        if (total > 0) {
            val pct = (copied * 100 / total).toInt().coerceIn(0, 100)
            if (pct != lastPct) {
                lastPct = pct
                onProgress(pct)
            }
        }
    }
}

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
suspend fun downloadAndInstallUpdate(
    context: Context,
    info: UpdateInfo,
    // 0-100 while the APK streams to disk - lets the caller show a bar under
    // the logo instead of a bare "downloading" state with no feedback for
    // however long the transfer takes. Never called if the APK was already
    // sitting on disk from a previous attempt (see isUpdateAlreadyDownloaded).
    onProgress: (percent: Int) -> Unit = {},
): Boolean {
    val file = updateApkFile(context)

    if (!isUpdateAlreadyDownloaded(context, info)) {
        val downloaded = withContext(Dispatchers.IO) {
            runCatching {
                val conn = URL(info.downloadUrl).openConnection() as HttpURLConnection
                conn.connectTimeout = 10_000
                conn.readTimeout = 120_000
                conn.instanceFollowRedirects = true
                val total = conn.contentLengthLong
                conn.inputStream.use { input ->
                    file.outputStream().use { output -> copyWithProgress(input, output, total, onProgress) }
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
