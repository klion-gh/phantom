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
 * [country]/[countryCode] are copied out of the config's own yaml - there is no
 * geo-IP lookup anywhere in the app any more, since that used to hand the
 * server's address to a third party on a timer. [ip] comes from a Ping to the
 * operator's own server.
 *
 * They are resolved once and pinned, not refreshed on every ping cycle: the
 * server behind a saved config essentially never moves. See
 * [ConfigStore.setCountry] and [ConfigStore.setServerIP].
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

    /**
     * Persists the country label and ISO code a config's yaml carries.
     *
     * Deliberately separate from [setServerIP]. These two values used to be written
     * together, after a Ping - which meant the country, which comes straight out of
     * the yaml and needs no network whatsoever, was silently discarded whenever that
     * Ping failed. Add a config with no connectivity and the label was simply never
     * stored, permanently, until the config happened to be edited again.
     */
    fun setCountry(context: Context, id: String, country: String?, countryCode: String?) {
        saveAll(context, loadAll(context).map {
            if (it.id == id) it.copy(country = country, countryCode = countryCode) else it
        })
    }

    /** Persists the server IP resolved by a Ping - the part that does need the network. */
    fun setServerIP(context: Context, id: String, ip: String) {
        saveAll(context, loadAll(context).map {
            if (it.id == id) it.copy(ip = ip) else it
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
