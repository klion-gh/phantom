package com.phantom.vpn

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

private const val GITHUB_RELEASES_API = "https://api.github.com/repos/klion-gh/phantom/releases/latest"
private const val GITHUB_RELEASES_PAGE = "https://github.com/klion-gh/phantom/releases/latest"

/**
 * Checks GitHub for a release newer than [currentVersion] (pass
 * BuildConfig.VERSION_NAME) and returns the releases page URL to open if one
 * exists, or null otherwise (already current, offline, rate-limited, no
 * tag_name in the response - never treated as an error worth surfacing).
 * Unlike the Windows app, Android can't silently replace its own installed
 * APK, so this just hands the user off to the browser to download and
 * install the new one themselves.
 */
suspend fun checkForUpdate(currentVersion: String): String? = withContext(Dispatchers.IO) {
    runCatching {
        val conn = URL(GITHUB_RELEASES_API).openConnection() as HttpURLConnection
        conn.connectTimeout = 6000
        conn.readTimeout = 6000
        conn.setRequestProperty("Accept", "application/vnd.github+json")
        val body = conn.inputStream.bufferedReader().use { it.readText() }
        val tag = JSONObject(body).optString("tag_name").takeIf { it.isNotBlank() }
            ?: return@runCatching null
        if (isNewerVersion(tag, currentVersion)) GITHUB_RELEASES_PAGE else null
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
