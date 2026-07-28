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

    private const val SECURE_PREFS = "phantom_secure_prefs"
    private const val PLAIN_PREFS = "phantom_plain_prefs"

    @Volatile
    private var cached: SharedPreferences? = null

    /**
     * True when configs are being kept in plain, unencrypted preferences because
     * the Android Keystore refused to initialise.
     *
     * This matters more here than in most apps: a saved config holds the PSK and
     * the server's address, and for a censorship-circumvention tool that pair is
     * itself the evidence of use - it identifies the device as a client of a
     * specific server. Anyone with access to the device's app-private storage (a
     * rooted device, an ADB backup, a forensic image) reads it straight out of XML.
     *
     * The fallback still exists - refusing to start at all would be worse - but it
     * is no longer silent: [MainActivity] surfaces this to the user.
     */
    @Volatile
    var storageIsPlaintext: Boolean = false
        private set

    @Synchronized
    private fun prefs(context: Context): SharedPreferences {
        cached?.let { return it }

        // EncryptedSharedPreferences touches the Android Keystore and can throw on
        // some devices/ROMs; fall back to plain prefs rather than taking the app down.
        val secure = try {
            val masterKey = MasterKey.Builder(context)
                .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
                .build()
            EncryptedSharedPreferences.create(
                context, SECURE_PREFS, masterKey,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
            )
        } catch (t: Throwable) {
            FileLog.e("EncryptedSharedPreferences init failed, falling back to plain prefs", t)
            null
        }

        val opened = if (secure != null) {
            // Keystore is working now. If an earlier run degraded to plain storage,
            // move everything back and wipe the plaintext copy - without this a
            // single transient Keystore failure downgraded the app permanently,
            // since nothing ever looked at the plain store again.
            migratePlainToSecure(context, secure)
            storageIsPlaintext = false
            secure
        } else {
            storageIsPlaintext = true
            context.getSharedPreferences(PLAIN_PREFS, Context.MODE_PRIVATE)
        }

        cached = opened
        return opened
    }

    /** Moves any leftover plaintext entries into [secure] and erases the plain store. */
    private fun migratePlainToSecure(context: Context, secure: SharedPreferences) {
        val plain = context.getSharedPreferences(PLAIN_PREFS, Context.MODE_PRIVATE)
        val all = plain.all
        if (all.isEmpty()) return

        FileLog.e("recovering ${all.size} entries from plaintext storage back into the Keystore-backed store", null)
        val editor = secure.edit()
        for ((key, value) in all) {
            when (value) {
                is String -> editor.putString(key, value)
                is Boolean -> editor.putBoolean(key, value)
                is Int -> editor.putInt(key, value)
                is Long -> editor.putLong(key, value)
                is Float -> editor.putFloat(key, value)
            }
        }
        editor.apply()
        // commit(), not apply(): the plaintext copy must actually be gone before
        // this returns, not queued behind whatever else the app does next.
        plain.edit().clear().commit()
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
