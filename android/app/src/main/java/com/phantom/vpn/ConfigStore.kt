package com.phantom.vpn

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

/**
 * One saved client.yaml, shown as its own tile on the main screen.
 *
 * [ip]/[country]/[countryCode] are resolved once (a Ping + a geo-IP lookup)
 * right after the config is added or edited, not on every ping cycle - the
 * server behind a saved config essentially never moves, so re-resolving its
 * location every few seconds on a timer was just wasted third-party calls
 * (and is what rate-limited the geo-IP provider into 429s during
 * development). They're null until [ConfigStore.setGeo] is called once.
 */
data class SavedConfig(
    val id: String,
    val yaml: String,
    val ip: String? = null,
    val country: String? = null,
    val countryCode: String? = null,
    // The independent SOCKS5 proxy's port (see ProxyManager), remembered once it's
    // first assigned so it stays the same across restarts/toggles - otherwise
    // whatever else points at it (e.g. Telegram's own proxy settings) would need
    // reconfiguring every time.
    val proxyPort: Int? = null,
)

/**
 * Single place that knows how to open (and gracefully degrade) the app's saved
 * configs. Shared by MainActivity (editing/saving) and PhantomVpnService
 * (reconnecting from the persistent notification's "Подключить" action, which
 * has no fresh yaml to pass as an intent extra - it resumes the last-active id).
 */
object ConfigStore {
    private const val CONFIGS_KEY = "client_configs" // JSON array: [{"id":..,"yaml":..}]
    private const val LEGACY_YAML_KEY = "client_yaml" // pre-multi-config single entry
    private const val LAST_ACTIVE_KEY = "last_active_config_id"

    @Volatile
    private var cached: SharedPreferences? = null

    @Synchronized
    private fun prefs(context: Context): SharedPreferences {
        cached?.let { return it }
        // EncryptedSharedPreferences touches the Android Keystore and can throw on some
        // devices/ROMs; fall back to plain prefs rather than taking the app down.
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

    fun loadAll(context: Context): List<SavedConfig> {
        val p = prefs(context)
        val raw = p.getString(CONFIGS_KEY, null)
        if (raw == null) {
            // One-time migration from the old single-config storage.
            val legacy = p.getString(LEGACY_YAML_KEY, "")?.takeIf { it.isNotBlank() } ?: return emptyList()
            val migrated = listOf(SavedConfig(UUID.randomUUID().toString(), legacy))
            saveAll(context, migrated)
            p.edit().remove(LEGACY_YAML_KEY).apply()
            return migrated
        }
        return runCatching {
            val arr = JSONArray(raw)
            (0 until arr.length()).map { i ->
                val obj = arr.getJSONObject(i)
                SavedConfig(
                    id = obj.getString("id"),
                    yaml = obj.getString("yaml"),
                    ip = obj.optString("ip").takeIf { it.isNotBlank() },
                    country = obj.optString("country").takeIf { it.isNotBlank() },
                    countryCode = obj.optString("countryCode").takeIf { it.isNotBlank() },
                    proxyPort = obj.optInt("proxyPort", 0).takeIf { it > 0 },
                )
            }
        }.getOrDefault(emptyList())
    }

    fun saveAll(context: Context, configs: List<SavedConfig>) {
        val arr = JSONArray()
        configs.forEach { cfg ->
            arr.put(JSONObject().apply {
                put("id", cfg.id)
                put("yaml", cfg.yaml)
                cfg.ip?.let { put("ip", it) }
                cfg.country?.let { put("country", it) }
                cfg.countryCode?.let { put("countryCode", it) }
                cfg.proxyPort?.let { put("proxyPort", it) }
            })
        }
        prefs(context).edit().putString(CONFIGS_KEY, arr.toString()).apply()
    }

    fun add(context: Context, yaml: String): SavedConfig {
        val cfg = SavedConfig(id = UUID.randomUUID().toString(), yaml = yaml)
        saveAll(context, loadAll(context) + cfg)
        return cfg
    }

    /** Clears any previously cached geo data - the edited yaml may point at a different
     * server entirely, so the old ip/country would be stale until [setGeo] re-resolves it. */
    fun update(context: Context, id: String, yaml: String) {
        saveAll(context, loadAll(context).map {
            if (it.id == id) it.copy(yaml = yaml, ip = null, country = null, countryCode = null) else it
        })
    }

    fun delete(context: Context, id: String) {
        saveAll(context, loadAll(context).filterNot { it.id == id })
    }

    /** Persists the one-time-resolved IP/country/flag for a saved config - called right
     * after add/update, once a Ping and a geo-IP lookup have completed. */
    fun setGeo(context: Context, id: String, ip: String, country: String?, countryCode: String?) {
        saveAll(context, loadAll(context).map {
            if (it.id == id) it.copy(ip = ip, country = country, countryCode = countryCode) else it
        })
    }

    /** Persists the independent proxy's bound port - called the first time it's
     * started (or if its previously remembered port turned out to be unavailable and
     * a different one had to be used instead), so the next start reuses the same port. */
    fun setProxyPort(context: Context, id: String, port: Int) {
        saveAll(context, loadAll(context).map {
            if (it.id == id) it.copy(proxyPort = port) else it
        })
    }

    fun loadLastActiveId(context: Context): String? = prefs(context).getString(LAST_ACTIVE_KEY, null)

    fun saveLastActiveId(context: Context, id: String?) {
        prefs(context).edit().putString(LAST_ACTIVE_KEY, id).apply()
    }
}
