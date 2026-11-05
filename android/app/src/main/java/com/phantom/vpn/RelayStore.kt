package com.phantom.vpn

import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

/** One relay (cmd/relay) in the list. [ip]/[country]/[countryCode] are what
 * its tile shows, cached the same way a config's are. */
data class SavedRelay(
    val id: String,
    val address: String,
    val ip: String? = null,
    val country: String? = null,
    val countryCode: String? = null,
)

/**
 * The relays on the Конфигурации page: a list of them, one picked, and the
 * "Использовать ретранслятор" switch. While it's on, every saved config
 * connects through the picked relay. That's a setting about the network the
 * phone is on, not about any one server, so it lives here rather than in each
 * config - the configs stay as imported, and the Go core adds the relay on
 * the way in (mobile/relay.go).
 */
object RelayStore {
    private const val PREFS = "phantom_settings"
    private const val RELAYS_KEY = "relays_json"
    // 2.5.0-beta.3 kept a single relay in these two.
    private const val LEGACY_ENABLED_KEY = "relay_enabled"
    private const val LEGACY_ADDRESS_KEY = "relay_address"

    var enabled by mutableStateOf(false)
        private set

    var selectedId by mutableStateOf<String?>(null)
        private set

    var relays by mutableStateOf<List<SavedRelay>>(emptyList())
        private set

    /** The relay connections go through right now, or "" for none. */
    val activeAddress: String
        get() = if (enabled) relays.firstOrNull { it.id == selectedId }?.address.orEmpty() else ""

    /** Reads the setting and hands it to the core. Called from
     * [PhantomApplication.onCreate], before anything can start a tunnel. */
    fun load(context: Context) {
        val p = prefs(context)
        val raw = p.getString(RELAYS_KEY, null)
        if (raw != null) {
            runCatching {
                val o = JSONObject(raw)
                enabled = o.optBoolean("enabled")
                selectedId = o.optString("selected").ifEmpty { null }
                val arr = o.optJSONArray("relays") ?: JSONArray()
                relays = (0 until arr.length()).map { i ->
                    val r = arr.getJSONObject(i)
                    SavedRelay(
                        id = r.getString("id"),
                        address = r.getString("address"),
                        ip = r.optString("ip").ifEmpty { null },
                        country = r.optString("country").ifEmpty { null },
                        countryCode = r.optString("countryCode").ifEmpty { null },
                    )
                }
            }
        } else {
            val old = p.getString(LEGACY_ADDRESS_KEY, "").orEmpty()
            if (old.isNotEmpty()) {
                val r = SavedRelay(UUID.randomUUID().toString(), old)
                relays = listOf(r)
                selectedId = r.id
                enabled = p.getBoolean(LEGACY_ENABLED_KEY, false)
                save(context)
            }
        }
        applyToCore()
    }

    fun setEnabled(context: Context, on: Boolean) = change(context) { enabled = on }

    fun select(context: Context, id: String) = change(context) { selectedId = id }

    /** Adds a relay - picked straight away when it's the first. Returns false,
     * changing nothing, when [address] isn't an address. */
    fun add(context: Context, address: String): Boolean {
        val normalized = normalize(address) ?: return false
        change(context) {
            val r = SavedRelay(UUID.randomUUID().toString(), normalized)
            relays = relays + r
            if (selectedId == null || relays.none { it.id == selectedId }) selectedId = r.id
        }
        return true
    }

    fun update(context: Context, id: String, address: String): Boolean {
        val normalized = normalize(address) ?: return false
        change(context) {
            relays = relays.map {
                // A different relay: what was cached about the old one is not about it.
                if (it.id == id && it.address != normalized) SavedRelay(id, normalized) else it
            }
        }
        RelayPings.forget(id)
        return true
    }

    /** Removes a relay. Removing the picked one picks the first left. */
    fun delete(context: Context, id: String) = change(context) {
        relays = relays.filter { it.id != id }
        if (selectedId == id) selectedId = relays.firstOrNull()?.id
        RelayPings.forget(id)
    }

    fun setGeo(context: Context, id: String, ip: String, country: String?, countryCode: String?) {
        relays = relays.map { if (it.id == id) it.copy(ip = ip, country = country, countryCode = countryCode) else it }
        save(context)
    }

    /** Applies [block], saves, and - when that moved the active relay -
     * rebuilds a running tunnel so the change applies at once. */
    private fun change(context: Context, block: () -> Unit) {
        val before = activeAddress
        block()
        save(context)
        val after = activeAddress
        if (before != after) {
            applyToCore()
            Diag.log(Diag.Cat.VPN, "relayChanged", "active" to after.isNotEmpty())
            // Every ping so far measured the other path.
            PingStore.forgetAll()
            RoutingController.reconnectActive(context)
            RoutingController.probeNow()
        }
    }

    private fun normalize(address: String): String? =
        runCatching { Mobile.normalizeRelayAddr(address) }.getOrNull()

    private fun applyToCore() {
        val effective = activeAddress
        runCatching { Mobile.setRelay(effective) }
            .onFailure { FileLog.e("relay: the core refused $effective", it) }
    }

    private fun save(context: Context) {
        val arr = JSONArray()
        relays.forEach { r ->
            arr.put(JSONObject().apply {
                put("id", r.id)
                put("address", r.address)
                r.ip?.let { put("ip", it) }
                r.country?.let { put("country", it) }
                r.countryCode?.let { put("countryCode", it) }
            })
        }
        val o = JSONObject().apply {
            put("enabled", enabled)
            put("selected", selectedId ?: "")
            put("relays", arr)
        }
        prefs(context).edit().putString(RELAYS_KEY, o.toString()).apply()
    }

    private fun prefs(context: Context) = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}

/**
 * A relay's ping: one TCP connect, around the VPN - a relay holds no keys, so
 * there is no handshake to make with it. Taken on a tap, like a config's, and
 * also how a relay's IP and country get found.
 */
object RelayPings {
    private val results = mutableStateMapOf<String, PingInfo>()
    private val inFlight = mutableStateMapOf<String, Boolean>()

    operator fun get(id: String): PingInfo? = results[id]

    fun isChecking(id: String): Boolean = inFlight[id] == true

    fun forget(id: String) {
        results.remove(id)
    }

    suspend fun check(context: Context, relay: SavedRelay) {
        if (inFlight[relay.id] == true) return
        inFlight[relay.id] = true
        try {
            val json = withContext(Dispatchers.IO) {
                runCatching { JSONObject(Mobile.pingRelay(relay.address)) }.getOrNull()
            }
            val ip = json?.optString("ip")?.takeIf { it.isNotBlank() }
            val latency = json?.optLong("latency_ms", -1L)?.takeIf { it >= 0 }
            results[relay.id] = if (ip != null && latency != null) {
                PingInfo(ip = ip, latencyMs = latency)
            } else {
                PingInfo(ip = results[relay.id]?.ip, failed = true)
            }
            if (ip != null && (ip != relay.ip || relay.countryCode.isNullOrEmpty())) {
                val geo = lookupCountry(ip)
                RelayStore.setGeo(context, relay.id, ip, geo?.first, geo?.second)
            }
        } finally {
            inFlight.remove(relay.id)
        }
    }
}

const val RELAY_PING_GOOD_MS = 150L
const val RELAY_PING_SLOW_MS = 400L

/** [pingTone] for a relay: a bare TCP connect reads far lower than a server's
 * full handshake, so it has its own thresholds. */
fun relayPingTone(info: PingInfo?, checking: Boolean): androidx.compose.ui.graphics.Color? {
    if (checking) return null
    if (info?.failed == true) return Danger
    val ms = info?.latencyMs ?: return null
    return when {
        ms <= RELAY_PING_GOOD_MS -> Success
        ms <= RELAY_PING_SLOW_MS -> Warning
        else -> Danger
    }
}
