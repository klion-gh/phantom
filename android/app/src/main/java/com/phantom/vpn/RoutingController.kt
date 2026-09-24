package com.phantom.vpn

import android.content.Context
import android.content.Intent
import mobile.AutoSelector
import mobile.SwitchListener
import org.json.JSONArray

/**
 * Owns the Go-side smart selector and keeps it in step with what the user has
 * chosen in the UI.
 *
 * There is exactly one selector for the whole app rather than one per mode:
 * "Автоматически" and Умный VPN never both drive the tunnel (auto wins - see
 * [candidateIds]), so a second instance would only ever duplicate probes
 * against the same servers.
 *
 * Lives as an object, not inside a composable, because the selection has to
 * survive the Activity: the tunnel keeps running when the app is backgrounded,
 * and a server going down while the user isn't looking is precisely the case
 * this exists for.
 */
object RoutingController {

    @Volatile
    private var selector: AutoSelector? = null

    /** Set while the controller itself is driving a reconnect, so the
     *  resulting state change isn't mistaken for the user toggling a config. */
    @Volatile
    var switchInProgress: Boolean = false
        private set

    /**
     * Which configs the selector is allowed to choose between right now.
     *
     * "Автоматически" outranks Умный VPN (the user's own call): while it's on,
     * the whole device is tunnelled, so its list is the one that matters and
     * the smart list is inert.
     *
     * An empty selection under "Автоматически" means every saved config is
     * fair game - the point of the toggle is not having to choose, so
     * demanding a choice before it does anything would defeat it. Smart mode
     * is the opposite: it is explicitly about routing certain sites through
     * certain servers, so an empty list there means "not configured yet".
     */
    private fun candidates(context: Context): List<SavedConfig> {
        val all = ConfigStore.loadAll(context)
        return when {
            RoutingStore.autoEnabled -> {
                val chosen = RoutingStore.autoConfigIds
                if (chosen.isEmpty()) all else all.filter { it.id in chosen }
            }
            RoutingStore.smartEnabled -> all.filter { it.id in RoutingStore.smartConfigIds }
            else -> emptyList()
        }
    }

    /**
     * Rebuilds the selector's candidate set from the current settings and
     * starts or stops it to match. Safe to call on every relevant change -
     * it's cheap, and being the single place that reconciles UI state with the
     * Go side is what keeps the two from drifting.
     */
    @Synchronized
    fun sync(context: Context) {
        val appContext = context.applicationContext

        val configs = candidates(appContext)
        Diag.log(
            Diag.Cat.ROUTE, "sync",
            "candidates" to configs.size,
            "autoEnabled" to RoutingStore.autoEnabled,
            "smartEnabled" to RoutingStore.smartEnabled,
            "selectorWasRunning" to (selector != null),
        )
        if (configs.isEmpty()) {
            selector?.stop()
            selector = null
            return
        }

        val active = selector ?: AutoSelector(SwitchListener { configId ->
            onSelectorChose(appContext, configId)
        }).also {
            selector = it
            it.start()
        }

        configs.forEach { active.addCandidate(it.id, it.yaml) }
        active.apply()
    }

    /**
     * The selector decided the tunnel should be on [configId]. Reconnecting is
     * how that takes effect: the VPN service tears down the current tunnel and
     * dials the new config, which is the same path a manual switch takes.
     */
    private fun onSelectorChose(context: Context, configId: String) {
        val config = ConfigStore.loadAll(context).find { it.id == configId } ?: return

        // Already on it (the first pick often lands on whatever is connected
        // anyway) - reconnecting would drop every live connection for nothing.
        if (VpnStateHolder.state.value.activeConfigId == configId &&
            VpnStateHolder.state.value.status == ConnectionStatus.CONNECTED
        ) {
            return
        }
        // Only actually move the tunnel when a mode that owns it is on. With
        // smart mode the tunnel may legitimately be down, and connecting
        // behind the user's back is not this feature's job.
        if (!RoutingStore.autoEnabled && !RoutingStore.smartEnabled) return

        FileLog.i("routing: switching tunnel to config $configId")
        switchInProgress = true
        try {
            context.startService(Intent(context, PhantomVpnService::class.java).apply {
                action = PhantomVpnService.ACTION_CONNECT
                putExtra(PhantomVpnService.EXTRA_CONFIG_ID, config.id)
                putExtra(PhantomVpnService.EXTRA_CONFIG_YAML, config.yaml)
            })
        } finally {
            switchInProgress = false
        }
    }

    /** Forces an immediate probe round - worth doing right after the device's
     *  network changed, where waiting for the next tick would leave the tunnel
     *  on a config that just became unreachable. */
    fun probeNow() {
        selector?.probe()
    }

    /**
     * Reconnects whatever config is currently active, so a smart-VPN site-list
     * edit made while connected takes effect for connections that were already
     * open - see [RoutingStore.sitesDirty]. Reuses the exact same
     * ACTION_CONNECT path a manual reconnect or a config switch takes:
     * PhantomVpnService.connect() tears down the live tunnel before dialing
     * again, which is what actually forces the browser to open fresh
     * connections.
     *
     * A no-op if nothing is connected - there's nothing to reconnect, and the
     * "Применить" prompt that calls this is only ever shown while connected.
     */
    fun reconnectActive(context: Context) {
        val state = VpnStateHolder.state.value
        if (state.status != ConnectionStatus.CONNECTED) return
        val configId = state.activeConfigId ?: return
        val config = ConfigStore.loadAll(context).find { it.id == configId } ?: return

        FileLog.i("routing: reconnecting $configId to apply routing changes")
        context.startService(Intent(context, PhantomVpnService::class.java).apply {
            action = PhantomVpnService.ACTION_CONNECT
            putExtra(PhantomVpnService.EXTRA_CONFIG_ID, config.id)
            putExtra(PhantomVpnService.EXTRA_CONFIG_YAML, config.yaml)
        })
    }

    private var lastHealthLogged: String? = null

    /** Per-config health for the UI, keyed by config id. */
    fun health(): Map<String, ConfigHealth> {
        val active = selector ?: return emptyMap()
        val current = active.current()
        val result = runCatching {
            val arr = JSONArray(active.healthJSON())
            (0 until arr.length()).associate { i ->
                val obj = arr.getJSONObject(i)
                val id = obj.getString("id")
                id to ConfigHealth(
                    alive = obj.optBoolean("alive", false),
                    latencyMs = obj.optLong("latency_ms", 0L),
                    probed = obj.optBoolean("probed", false),
                    active = id == current,
                )
            }
        }.getOrDefault(emptyMap())
        // Polled by the UI every 2s, so written only when the picture changes:
        // which configs have been probed, which answered, which one carries
        // traffic. Latency is left out of the comparison - it jitters every
        // probe and would defeat the point. The selector's own probeRound
        // line (Go side) has the numbers.
        val snapshot = "current=${current.take(8)} " + result.entries.joinToString(",") {
            "${it.key.take(8)}:${if (it.value.probed) "p" else "-"}${if (it.value.alive) "a" else "-"}"
        }
        if (snapshot != lastHealthLogged) {
            lastHealthLogged = snapshot
            Diag.log(
                Diag.Cat.ROUTE, "health",
                "configs" to result.size,
                "probed" to result.values.count { it.probed },
                "alive" to result.values.count { it.alive },
                "detail" to snapshot,
            )
        }
        return result
    }

    /**
     * Pushes the current smart-routing settings into a running tunnel.
     *
     * Called both when the tunnel starts and whenever the user edits the list,
     * so the two stay in step without a reconnect - the Go side swaps the
     * routing decision live (see mobile.Tunnel.SetSmartRouting).
     *
     * With "Автоматически" on, smart routing is explicitly *off* at the Go
     * level whatever the UI's smart toggle says: whole-device routing means
     * everything is tunnelled, and leaving a site filter installed underneath
     * it would quietly contradict that.
     */
    fun applyToTunnel(tunnel: mobile.Tunnel?) {
        val t = tunnel ?: return
        val smartActive = RoutingStore.smartEnabled && !RoutingStore.autoEnabled
        t.setSmartRouting(smartActive, RoutingStore.sitesPayload())
    }
}
