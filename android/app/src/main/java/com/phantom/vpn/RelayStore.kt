package com.phantom.vpn

import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import mobile.Mobile

/**
 * "Использовать ретранслятор" on the Конфигурации page: one relay server
 * (cmd/relay) every saved config connects through while it's on. It is a
 * setting about the network the phone is on, not about any one server, so it
 * lives here rather than in each config - the configs stay as imported, and
 * the Go core adds the relay on the way in (mobile/relay.go).
 *
 * The address is kept when the switch goes off, so turning it back on is one
 * tap.
 */
object RelayStore {
    private const val PREFS = "phantom_settings"
    private const val ENABLED_KEY = "relay_enabled"
    private const val ADDRESS_KEY = "relay_address"

    var enabled by mutableStateOf(false)
        private set

    var address by mutableStateOf("")
        private set

    /** Reads the setting and hands it to the core. Called from
     * [PhantomApplication.onCreate], before anything can start a tunnel. */
    fun load(context: Context) {
        val p = prefs(context)
        enabled = p.getBoolean(ENABLED_KEY, false)
        address = p.getString(ADDRESS_KEY, "") ?: ""
        applyToCore()
    }

    /**
     * Saves the setting. Returns false - and changes nothing - when [newAddress]
     * isn't an address; turning the relay off never needs one. A tunnel that's
     * up is rebuilt so the change applies at once.
     */
    fun set(context: Context, newEnabled: Boolean, newAddress: String): Boolean {
        val normalized = if (newAddress.isBlank()) "" else
            runCatching { Mobile.normalizeRelayAddr(newAddress) }.getOrNull() ?: return false
        if (newEnabled && normalized.isEmpty()) return false

        val changed = enabled != newEnabled || (newEnabled && address != normalized)
        enabled = newEnabled
        address = normalized
        prefs(context).edit()
            .putBoolean(ENABLED_KEY, newEnabled)
            .putString(ADDRESS_KEY, normalized)
            .apply()
        applyToCore()
        Diag.log(Diag.Cat.VPN, "setRelay", "enabled" to newEnabled)

        if (changed) {
            // Every ping so far measured the other path.
            PingStore.forgetAll()
            RoutingController.reconnectActive(context)
            RoutingController.probeNow()
        }
        return true
    }

    private fun applyToCore() {
        val effective = if (enabled) address else ""
        runCatching { Mobile.setRelay(effective) }
            .onFailure { FileLog.e("relay: the core refused $effective", it) }
    }

    private fun prefs(context: Context) = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}
