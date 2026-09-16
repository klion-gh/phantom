package com.phantom.vpn

import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import org.json.JSONArray

/**
 * One entry in the smart-VPN list: a site the user can't reach from where they
 * are, which should therefore be routed through the tunnel while everything
 * else keeps its normal path.
 *
 * [pattern] is whatever the user typed - a domain ("youtube.com", matching its
 * subdomains too), a literal IP, or a CIDR block. It is normalized on the Go
 * side (internal/routing), not here, so the two clients can't drift apart on
 * what an entry means.
 */
data class RoutedSite(val pattern: String)

/**
 * Everything the Маршрутизация section persists, as Compose state so the UI
 * reacts the moment any of it changes - the same mechanism [Appearance] and
 * [I18n] use.
 *
 * Kept in plain SharedPreferences rather than the encrypted store used for
 * configs: a list of sites the user wants unblocked is a preference, not a
 * credential, and the PSK/server address that *are* sensitive live in
 * ConfigStore.
 */
object RoutingStore {
    private const val PREFS = "phantom_settings"
    private const val SMART_ENABLED_KEY = "smart_vpn_enabled"
    private const val SITES_KEY = "smart_vpn_sites"
    private const val SMART_CONFIG_IDS_KEY = "smart_vpn_config_ids"
    private const val AUTO_ENABLED_KEY = "auto_config_enabled"
    private const val AUTO_CONFIG_IDS_KEY = "auto_config_ids"

    /** A starting point, not a recommendation - trivially editable, and only
     *  ever used to seed the very first run so the list isn't an empty box
     *  with no hint of what belongs in it. */
    private val defaultSites = listOf(
        "youtube.com",
        "googlevideo.com", // YouTube's media hosts; without it video stalls
        "discord.com",
        "discordapp.com",
    )

    var smartEnabled by mutableStateOf(false)
        private set

    var sites by mutableStateOf<List<RoutedSite>>(emptyList())
        private set

    /** Configs the user picked for smart mode to choose between. */
    var smartConfigIds by mutableStateOf<Set<String>>(emptySet())
        private set

    /** "Автоматически" on the Конфигурации page: whole-device traffic, with
     *  the server picked (and re-picked) automatically. Outranks smart mode -
     *  see the doc on [autoEnabled]'s use in MainActivity. */
    var autoEnabled by mutableStateOf(false)
        private set

    var autoConfigIds by mutableStateOf<Set<String>>(emptySet())
        private set

    /**
     * True once the routed-sites list has changed since the tunnel was last
     * (re)connected. Editing the list already reaches brand-new connections
     * right away - [applyToTunnel] swaps the live decision, no reconnect
     * needed - but a browser tab already open to a site before it was added
     * is very likely sitting on a warm connection dialed under the old list,
     * and nothing re-evaluates an established connection's routing on its
     * own. This is what drives the "Применить" prompt that offers to force
     * one (see [RoutingController.reconnectActive]).
     *
     * Not persisted: it only ever describes whether the *currently running*
     * tunnel is stale relative to the list, which has no meaning across a
     * process restart - a fresh connect always starts from the latest list.
     */
    var sitesDirty by mutableStateOf(false)
        private set

    fun markSitesDirty() { sitesDirty = true }

    fun clearSitesDirty() { sitesDirty = false }

    fun load(context: Context) {
        val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        smartEnabled = p.getBoolean(SMART_ENABLED_KEY, false)
        autoEnabled = p.getBoolean(AUTO_ENABLED_KEY, false)
        sites = decodeList(p.getString(SITES_KEY, null))
            ?.map { RoutedSite(it) }
            ?: defaultSites.map { RoutedSite(it) }.also { saveSites(context, it) }
        smartConfigIds = decodeList(p.getString(SMART_CONFIG_IDS_KEY, null))?.toSet() ?: emptySet()
        autoConfigIds = decodeList(p.getString(AUTO_CONFIG_IDS_KEY, null))?.toSet() ?: emptySet()
    }

    fun setSmartEnabled(context: Context, value: Boolean) {
        smartEnabled = value
        prefs(context).edit().putBoolean(SMART_ENABLED_KEY, value).apply()
    }

    fun setAutoEnabled(context: Context, value: Boolean) {
        autoEnabled = value
        prefs(context).edit().putBoolean(AUTO_ENABLED_KEY, value).apply()
    }

    fun addSite(context: Context, pattern: String) {
        val cleaned = pattern.trim()
        if (cleaned.isEmpty()) return
        // Compared case-insensitively because the Go side lowercases anyway -
        // letting both "YouTube.com" and "youtube.com" sit in the list would
        // just be two rows that behave as one.
        if (sites.any { it.pattern.equals(cleaned, ignoreCase = true) }) return
        saveSites(context, sites + RoutedSite(cleaned))
    }

    fun removeSite(context: Context, pattern: String) {
        saveSites(context, sites.filterNot { it.pattern == pattern })
    }

    /**
     * Whether every domain a catalogue entry needs is already listed. All of
     * them, not any: a service whose media host is missing looks "on" in the
     * picker while still not working, which is worse than looking off.
     */
    fun hasAll(domains: List<String>): Boolean =
        domains.all { d -> sites.any { it.pattern.equals(d, ignoreCase = true) } }

    /**
     * Adds or removes a catalogue entry wholesale - one tap in the picker is
     * one service, however many domains that service actually needs.
     */
    fun togglePopular(context: Context, domains: List<String>) {
        if (hasAll(domains)) {
            val drop = domains.map { it.lowercase() }.toSet()
            saveSites(context, sites.filterNot { it.pattern.lowercase() in drop })
        } else {
            val existing = sites.map { it.pattern.lowercase() }.toSet()
            val additions = domains.filterNot { it.lowercase() in existing }.map { RoutedSite(it) }
            saveSites(context, sites + additions)
        }
    }

    fun toggleSmartConfig(context: Context, id: String) {
        val next = smartConfigIds.toMutableSet().apply { if (!add(id)) remove(id) }
        smartConfigIds = next
        prefs(context).edit().putString(SMART_CONFIG_IDS_KEY, encodeList(next)).apply()
    }

    fun toggleAutoConfig(context: Context, id: String) {
        val next = autoConfigIds.toMutableSet().apply { if (!add(id)) remove(id) }
        autoConfigIds = next
        prefs(context).edit().putString(AUTO_CONFIG_IDS_KEY, encodeList(next)).apply()
    }

    /** Drops a deleted config from both selections, so a stale id can't keep
     *  being offered to the selector after the config itself is gone. */
    fun forgetConfig(context: Context, id: String) {
        if (id in smartConfigIds) toggleSmartConfig(context, id)
        if (id in autoConfigIds) toggleAutoConfig(context, id)
    }

    /** The site list in the newline-separated form the Go side expects. */
    fun sitesPayload(): String = sites.joinToString("\n") { it.pattern }

    private fun saveSites(context: Context, value: List<RoutedSite>) {
        sites = value
        prefs(context).edit().putString(SITES_KEY, encodeList(value.map { it.pattern })).apply()
    }

    private fun prefs(context: Context) = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private fun encodeList(values: Collection<String>): String =
        JSONArray().also { arr -> values.forEach { arr.put(it) } }.toString()

    private fun decodeList(raw: String?): List<String>? {
        if (raw == null) return null
        return runCatching {
            val arr = JSONArray(raw)
            (0 until arr.length()).map { arr.getString(it) }
        }.getOrNull()
    }
}
