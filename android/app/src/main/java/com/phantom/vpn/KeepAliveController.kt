package com.phantom.vpn

import android.content.Context
import android.content.Intent
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import mobile.KeepAlive
import mobile.KeepAliveListener
import mobile.Tunnel
import org.json.JSONObject

/**
 * "Поддерживать соединение" - the switch under "Использовать ретранслятор".
 *
 * While it's on, the Go side (mobile/keepalive.go, internal/linkguard) checks
 * that internet actually gets through the connected config and, when it
 * stops: tells a dead network apart from a dead tunnel, reconnects the
 * config once, and if that doesn't help moves to the fastest config that does
 * carry internet - all checks through the relay when one is in use. Works the
 * same for a config connected by hand, Умный VPN and "Выбирать лучшую".
 *
 * An object, like [RoutingController], because it has to outlive the
 * Activity: the moment it exists for is usually one when nobody is looking at
 * the app. The VPN service reports every connect to it ([onConnectStart],
 * [onTunnelUp], [onConnectFailed], [onUserDisconnect]); it asks the service to
 * connect through the same intent a manual connect uses.
 */
object KeepAliveController {
    private const val PREFS = "phantom_settings"
    private const val KEY = "keep_alive"

    var enabled by mutableStateOf(false)
        private set

    @Volatile
    private var guard: KeepAlive? = null
    private lateinit var appContext: Context

    // What the service last reported, so a guard created while a tunnel is
    // already up (the switch flipped mid-session) starts from the truth.
    @Volatile private var wantedId: String? = null
    @Volatile private var tunnelId: String? = null
    @Volatile private var tunnel: Tunnel? = null

    fun load(context: Context) {
        appContext = context.applicationContext
        enabled = appContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(KEY, false)
        sync()
    }

    fun setEnabled(context: Context, on: Boolean) {
        appContext = context.applicationContext
        enabled = on
        appContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putBoolean(KEY, on).apply()
        Diag.log(Diag.Cat.VPN, "setKeepAlive", "enabled" to on)
        sync()
    }

    @Synchronized
    private fun sync() {
        if (!enabled) {
            guard?.stop()
            guard = null
            return
        }
        if (guard != null) return
        val g = KeepAlive(KeepAliveListener { configId -> connect(configId) })
        guard = g
        refreshCandidates()
        wantedId?.let { g.setWanted(it) }
        val t = tunnel
        val tid = tunnelId
        if (t != null && tid != null) g.setTunnel(tid, t)
        g.start()
    }

    /** Which configs the guard may move the tunnel to: the driving mode's own
     *  set while Умный VPN or "Выбирать лучшую" is on, otherwise all of them. */
    fun refreshCandidates() {
        val g = guard ?: return
        val configs = RoutingController.candidates(appContext).ifEmpty { ConfigStore.loadAll(appContext) }
        configs.forEach { g.addCandidate(it.id, it.yaml) }
        g.apply()
    }

    private fun connect(configId: String) {
        val config = ConfigStore.loadAll(appContext).find { it.id == configId } ?: run {
            guard?.connectFailed(configId)
            return
        }
        FileLog.i("keep-alive: connecting $configId")
        runCatching {
            appContext.startService(Intent(appContext, PhantomVpnService::class.java).apply {
                action = PhantomVpnService.ACTION_CONNECT
                putExtra(PhantomVpnService.EXTRA_CONFIG_ID, config.id)
                putExtra(PhantomVpnService.EXTRA_CONFIG_YAML, config.yaml)
            })
        }.onFailure {
            FileLog.e("keep-alive: could not start the VPN service", it)
            guard?.connectFailed(configId)
        }
    }

    // --- reported by PhantomVpnService ---------------------------------------

    fun onConnectStart(configId: String) {
        wantedId = configId
        refreshCandidates()
        guard?.setWanted(configId)
    }

    fun onTunnelUp(configId: String, t: Tunnel) {
        wantedId = configId
        tunnelId = configId
        tunnel = t
        guard?.setTunnel(configId, t)
    }

    fun onConnectFailed(configId: String) {
        tunnelId = null
        tunnel = null
        guard?.connectFailed(configId)
    }

    fun onUserDisconnect() {
        wantedId = null
        tunnelId = null
        tunnel = null
        guard?.setWanted("")
    }

    /** The phone moved to another network: check now rather than at the
     *  next round. */
    fun kick() {
        guard?.kick()
    }

    /** The guard's state, for the line under the switch. */
    fun status(): KeepAliveStatus {
        val raw = guard?.statusJSON() ?: return KeepAliveStatus("off")
        return runCatching {
            val o = JSONObject(raw)
            KeepAliveStatus(
                state = o.optString("state", "off"),
                configId = o.optString("configId").ifEmpty { null },
                switchedAt = o.optLong("switchedAt", 0L),
                switchedTo = o.optString("switchedTo").ifEmpty { null },
            )
        }.getOrDefault(KeepAliveStatus("off"))
    }
}

data class KeepAliveStatus(
    val state: String,
    val configId: String? = null,
    val switchedAt: Long = 0L,
    val switchedTo: String? = null,
)
