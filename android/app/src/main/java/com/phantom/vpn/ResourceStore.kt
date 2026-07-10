package com.phantom.vpn

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

/**
 * One "is this reachable" tile - separate from SavedConfig, purely diagnostic (just a
 * name + URL). Reachability is checked directly against the URL from the app's own
 * process, which goes through Android's normal networking - once the VPN tunnel is up,
 * VpnService routes this app's own traffic through it too (same as every other app on
 * the device), so a normally-blocked resource starting to respond is the whole point.
 */
data class PingResource(val id: String, val name: String, val url: String)

/** Mirrors ConfigStore's storage pattern exactly, just for the smaller resource-tile model. */
object ResourceStore {
    private const val RESOURCES_KEY = "ping_resources" // JSON array: [{"id":..,"name":..,"url":..}]

    private val defaults = listOf(
        "YouTube" to "https://www.youtube.com",
        "Discord" to "https://discord.com",
        "ChatGPT" to "https://chatgpt.com",
        "Claude" to "https://claude.ai",
    )

    @Volatile
    private var cached: SharedPreferences? = null

    @Synchronized
    private fun prefs(context: Context): SharedPreferences {
        cached?.let { return it }
        val opened = try {
            val masterKey = MasterKey.Builder(context)
                .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
                .build()
            EncryptedSharedPreferences.create(
                context, "phantom_secure_prefs", masterKey,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
            )
        } catch (t: Throwable) {
            FileLog.e("EncryptedSharedPreferences init failed, falling back to plain prefs", t)
            null
        } ?: context.getSharedPreferences("phantom_plain_prefs", Context.MODE_PRIVATE)
        cached = opened
        return opened
    }

    fun loadAll(context: Context): List<PingResource> {
        val p = prefs(context)
        val raw = p.getString(RESOURCES_KEY, null)
        if (raw == null) {
            // First run: seed the built-in defaults so the page isn't empty and the
            // user can delete/customize from there on.
            val seeded = defaults.map { (name, url) -> PingResource(UUID.randomUUID().toString(), name, url) }
            saveAll(context, seeded)
            return seeded
        }
        return runCatching {
            val arr = JSONArray(raw)
            (0 until arr.length()).map { i ->
                val obj = arr.getJSONObject(i)
                PingResource(obj.getString("id"), obj.getString("name"), obj.getString("url"))
            }
        }.getOrDefault(emptyList())
    }

    fun saveAll(context: Context, resources: List<PingResource>) {
        val arr = JSONArray()
        resources.forEach { r ->
            arr.put(JSONObject().apply { put("id", r.id); put("name", r.name); put("url", r.url) })
        }
        prefs(context).edit().putString(RESOURCES_KEY, arr.toString()).apply()
    }

    fun add(context: Context, name: String, url: String): PingResource {
        val r = PingResource(id = UUID.randomUUID().toString(), name = name, url = url)
        saveAll(context, loadAll(context) + r)
        return r
    }

    fun delete(context: Context, id: String) {
        saveAll(context, loadAll(context).filterNot { it.id == id })
    }
}
