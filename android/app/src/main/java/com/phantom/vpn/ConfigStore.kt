package com.phantom.vpn

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

/** One saved client.yaml, shown as its own tile on the main screen. */
data class SavedConfig(val id: String, val yaml: String)

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
                SavedConfig(obj.getString("id"), obj.getString("yaml"))
            }
        }.getOrDefault(emptyList())
    }

    fun saveAll(context: Context, configs: List<SavedConfig>) {
        val arr = JSONArray()
        configs.forEach { cfg ->
            arr.put(JSONObject().apply { put("id", cfg.id); put("yaml", cfg.yaml) })
        }
        prefs(context).edit().putString(CONFIGS_KEY, arr.toString()).apply()
    }

    fun add(context: Context, yaml: String): SavedConfig {
        val cfg = SavedConfig(id = UUID.randomUUID().toString(), yaml = yaml)
        saveAll(context, loadAll(context) + cfg)
        return cfg
    }

    fun update(context: Context, id: String, yaml: String) {
        saveAll(context, loadAll(context).map { if (it.id == id) it.copy(yaml = yaml) else it })
    }

    fun delete(context: Context, id: String) {
        saveAll(context, loadAll(context).filterNot { it.id == id })
    }

    fun loadLastActiveId(context: Context): String? = prefs(context).getString(LAST_ACTIVE_KEY, null)

    fun saveLastActiveId(context: Context, id: String?) {
        prefs(context).edit().putString(LAST_ACTIVE_KEY, id).apply()
    }
}
