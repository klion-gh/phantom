package com.phantom.vpn

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.graphics.drawable.Icon
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import kotlinx.coroutines.runBlocking
import mobile.Mobile
import mobile.Protector
import mobile.Tunnel
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class PhantomVpnService : VpnService() {

    companion object {
        const val ACTION_CONNECT = "com.phantom.vpn.CONNECT"
        const val ACTION_DISCONNECT = "com.phantom.vpn.DISCONNECT"
        const val ACTION_SHOW_STATUS = "com.phantom.vpn.SHOW_STATUS"
        // Sent by MainActivity right after any ProxyManager.start/stop call (success or
        // not) so this service can re-evaluate whether it needs to be a foreground
        // service - see showPersistentNotification and the class doc on ProxyManager.
        const val ACTION_PROXY_STATE_CHANGED = "com.phantom.vpn.PROXY_STATE_CHANGED"
        // The notification's own Proxy action buttons - unlike the UI's per-tile
        // toggles, these operate on "the proxy" as a whole: connect resumes the
        // last-active config (same fallback as ACTION_CONNECT's notification path),
        // disconnect stops every running one.
        const val ACTION_PROXY_CONNECT = "com.phantom.vpn.PROXY_CONNECT"
        const val ACTION_PROXY_DISCONNECT = "com.phantom.vpn.PROXY_DISCONNECT"
        const val EXTRA_CONFIG_YAML = "config_yaml"
        const val EXTRA_CONFIG_ID = "config_id"
        // Marks a disconnect that came from the notification's own button - see
        // ACTION_DISCONNECT's handling for why that one has to do more.
        const val EXTRA_FROM_NOTIFICATION = "from_notification"

        private const val CHANNEL_ID = "phantom_vpn"
        private const val NOTIFICATION_ID = 1
        private const val MTU = 1500
        // How long a tunnel gets to stop before it's abandoned and its
        // interface closed anyway - see stopTunnelBounded().
        private const val TUNNEL_STOP_DEADLINE_MS = 2500L
        // How long disconnect() waits for its whole graceful path before
        // forcing the interface down itself - see forceDisconnect(). Longer
        // than TUNNEL_STOP_DEADLINE_MS, which that path is already bounded by;
        // this is the net for anything else that ever jams the executor.
        private const val DISCONNECT_FORCE_TIMEOUT_MS = 5000L
        // How long network events settle before the tunnel acts on them - a
        // real Wi-Fi<->cellular handover fires a burst of them.
        private const val NETWORK_SETTLE_MS = 1500L
        // Storm guard (see stormBackoffMs): this many network switches within
        // a minute means something is flapping, and each further evaluation
        // waits longer before acting.
        private const val STORM_SWITCHES_PER_MINUTE = 4
        private const val STORM_BACKOFF_BASE_MS = 10_000L
        private const val STORM_BACKOFF_MAX_MS = 60_000L

        @Volatile
        private var activeInstance: PhantomVpnService? = null

        /**
         * A [Protector] for ProxyManager's independent proxy, evaluated lazily at each
         * protect() call (not captured once at proxy start) because the proxy redials
         * over its whole lifetime - pool self-healing, session refresh after a network
         * change - and both whether a VPN is up and which service instance is alive
         * change underneath it.
         *
         * It only actually calls VpnService.protect() while a full tunnel is really
         * established (CONNECTED/CONNECTING); otherwise it's a no-op success. Two
         * reasons:
         *
         *  - When no tunnel is up there's nothing capturing the proxy's sockets, so
         *    protection is simply unnecessary.
         *  - VpnService.protect() only works for the *active* system VPN. This service
         *    is often foregrounded purely for the proxy, with no establish() call, so
         *    it isn't the active VPN - and protect() then returns false, which fails
         *    the dial. That's what left the proxy dead after a Wi-Fi<->cellular switch
         *    until an app restart: the pool's fresh redial kept getting its socket
         *    "protected" by a non-active VPN, i.e. rejected, forever.
         *
         * When a tunnel *is* up, protection is essential: without it, turning the full
         * VPN on (for this config or any other) would capture the proxy's own
         * connections into that tunnel and break them, since they were never part of
         * its own protected dial. protect() applies per-process, not per-component, so
         * the active VpnService instance can exempt them regardless of which component
         * owns the sockets.
         */
        val lazyProtector: Protector = object : Protector {
            override fun protect(fd: Long): Boolean {
                val instance = activeInstance ?: return true
                val vpnActive = VpnStateHolder.state.value.status.let {
                    it == ConnectionStatus.CONNECTED || it == ConnectionStatus.CONNECTING
                }
                if (!vpnActive) return true
                return instance.protect(fd.toInt())
            }
        }

        /**
         * Pushes the current routing settings into whatever tunnel is running,
         * if any. Lets the UI edit the site list or flip the smart toggle and
         * have it take effect immediately - the Go side swaps the decision
         * live, so nothing needs to reconnect.
         *
         * A no-op when no tunnel is up, which is the common case while the
         * user is still setting the list up.
         */
        fun applyRoutingToActiveTunnel() {
            RoutingController.applyToTunnel(activeInstance?.tunnel)
            // Doesn't reconnect anything by itself, so nothing would otherwise
            // rebuild the notification - and its VPN/Умный VPN label depends on
            // exactly the flags this just changed.
            activeInstance?.showPersistentNotification(VpnStateHolder.state.value.status)
        }
    }

    override fun onCreate() {
        super.onCreate()
        activeInstance = this
    }

    // var, not val: forceDisconnect() swaps this out for a fresh executor when
    // the old one is permanently wedged inside a stuck tunnel.stop() call - see
    // its class doc.
    private var executor = Executors.newSingleThreadExecutor()
    // @Volatile: forceDisconnect() (main thread, via a Handler) can now touch
    // these concurrently with the executor thread's own graceful teardown.
    @Volatile private var tunInterface: ParcelFileDescriptor? = null
    @Volatile private var tunnel: Tunnel? = null

    // Reconnect-on-network-change: a plain TCP/TLS socket bound to (say) Wi-Fi
    // doesn't migrate itself when Wi-Fi disappears and cellular takes over - it
    // just dies, and since nothing was watching for that, the whole tunnel would
    // silently stop passing traffic instead of reconnecting.
    //
    // This deliberately does NOT use registerDefaultNetworkCallback: that reports
    // *this app's own* perceived default network, and once our own tunnel is up,
    // Android considers our own VPN interface to be this app's new default
    // (we're not excluded from our own tunnel) - so its very first callback
    // fires reporting our own just-created VPN network, which looks exactly
    // like "the network changed" and triggers an immediate reconnect. That
    // reconnect creates a new VPN interface, which repeats the same thing again -
    // an infinite reconnect loop even with a rock-stable Wi-Fi. Filtering the
    // request to NET_CAPABILITY_NOT_VPN sidesteps this entirely: it only ever
    // reports genuine physical networks (Wi-Fi, cellular, ethernet), never our
    // own tunnel, so every event it fires is an actual signal worth reacting to.
    private var connectivityManager: ConnectivityManager? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null
    private var activeConfigId: String? = null
    private var activeConfigYaml: String? = null
    private val reconnectHandler = Handler(Looper.getMainLooper())

    // The physical network the tunnel is running over right now - the one its
    // connection to the server and every direct flow go out on. Network events
    // are judged against it (see evaluateNetwork): only losing it, or a better
    // one appearing, moves the tunnel. Anything else - notably a Samsung phone
    // bringing a mobile-data network up and down in the background while on
    // Wi-Fi - is none of the tunnel's business.
    @Volatile private var boundNetwork: Network? = null
    private val networkEvaluationPending = java.util.concurrent.atomic.AtomicBoolean(false)
    // When the tunnel last switched networks, for the storm guard.
    private val recentSwitches = ArrayDeque<Long>()

    // A separate, lightweight physical-network watch for the independent proxy,
    // active only while a proxy is running. The VPN's own watch above rebuilds
    // the tunnel on a network change, but a proxy running with no full VPN has
    // no tunnel and no watch of its own - its pooled sockets to the server just
    // die on a Wi-Fi<->cellular switch. This nudges the pool to redial at once
    // (ProxyManager.reconnectAll) instead of recovering only lazily on the next
    // request after the dead sockets time out. Same NET_CAPABILITY_NOT_VPN
    // filter and debounce as the VPN watch; kept in its own fields so the two
    // watches never interfere.
    private var proxyConnectivityManager: ConnectivityManager? = null
    private var proxyNetworkCallback: ConnectivityManager.NetworkCallback? = null
    private var proxyWatchRegisteredAtMs: Long = 0
    private val proxyReconnectHandler = Handler(Looper.getMainLooper())
    private var pendingProxyReconnect: Runnable? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        FileLog.i("onStartCommand action=${intent?.action}")
        when (intent?.action) {
            ACTION_DISCONNECT -> {
                if (intent.getBooleanExtra(EXTRA_FROM_NOTIFICATION, false)) {
                    releaseDrivingModeFromNotification()
                }
                disconnect()
                return START_NOT_STICKY
            }
            ACTION_CONNECT -> {
                var id = intent.getStringExtra(EXTRA_CONFIG_ID)
                var yaml = intent.getStringExtra(EXTRA_CONFIG_YAML)?.takeIf { it.isNotBlank() }

                if (yaml == null) {
                    // The notification's own "Подключить" action carries no fresh extras
                    // (its PendingIntent is built once) - resume the last-active config,
                    // falling back to the first saved one if there's no prior session.
                    val saved = ConfigStore.loadAll(this)
                    val resumed = ConfigStore.loadLastActiveId(this)?.let { last -> saved.find { it.id == last } }
                        ?: saved.firstOrNull()
                    id = resumed?.id
                    yaml = resumed?.yaml
                }

                if (yaml == null || id == null) {
                    FileLog.e("connect requested with no saved config")
                    VpnStateHolder.update(ConnectionStatus.ERROR, "Missing client.yaml contents")
                    showPersistentNotification(ConnectionStatus.ERROR)
                    return START_NOT_STICKY
                }
                ConfigStore.saveLastActiveId(this, id)
                connect(id, yaml)
            }
            ACTION_SHOW_STATUS -> {
                // Just (re)posts the notification for whatever the current state already
                // is - never touches the tunnel, so it's safe to call on every app launch.
                showPersistentNotification(VpnStateHolder.state.value.status)
                return START_NOT_STICKY
            }
            ACTION_PROXY_CONNECT -> {
                connectProxyFromNotification()
                return START_NOT_STICKY
            }
            ACTION_PROXY_DISCONNECT -> {
                executor.execute {
                    ProxyManager.stopAll()
                    ensureProxyNetworkWatch()
                    showPersistentNotification(VpnStateHolder.state.value.status)
                    val vpnActive = VpnStateHolder.state.value.status.let {
                        it == ConnectionStatus.CONNECTED || it == ConnectionStatus.CONNECTING
                    }
                    if (!vpnActive) stopSelf()
                }
                return START_NOT_STICKY
            }
            ACTION_PROXY_STATE_CHANGED -> {
                ensureProxyNetworkWatch()
                showPersistentNotification(VpnStateHolder.state.value.status)
                val vpnActive = VpnStateHolder.state.value.status.let {
                    it == ConnectionStatus.CONNECTED || it == ConnectionStatus.CONNECTING
                }
                if (!vpnActive && !ProxyManager.hasAnyRunning()) {
                    // Nothing left to keep this service (or its foreground exemption)
                    // alive for - showPersistentNotification above already dropped out
                    // of foreground state for us.
                    stopSelf()
                }
                return START_NOT_STICKY
            }
        }
        return START_STICKY
    }

    /**
     * "Отключить" in the notification, while Умный VPN or "Выбирать лучшую" is
     * what holds the tunnel up, has to switch that mode off - the same thing
     * the app's own toggle does before disconnecting. Tearing down only the
     * tunnel left the mode flag on: the app's toggle kept showing it enabled,
     * and the selector (still running with that mode's candidates) could put
     * the tunnel straight back up on its next pick.
     *
     * Only for the notification's button: the app's own disconnects already
     * set the flags themselves before calling in, and a manually-connected
     * config's disconnect must not touch a mode that wasn't driving it.
     */
    private fun releaseDrivingModeFromNotification() {
        val released = when {
            RoutingStore.autoEnabled -> {
                RoutingStore.setAutoEnabled(this, false)
                "auto"
            }
            RoutingStore.smartEnabled -> {
                RoutingStore.setSmartEnabled(this, false)
                "smart"
            }
            else -> return
        }
        RoutingController.sync(this)
        Diag.log(Diag.Cat.VPN, "notificationDisconnect", "modeTurnedOff" to released)
    }

    // Builds the tunnel from scratch - for an explicit connect or a config
    // switch. A network change doesn't come through here any more: that moves
    // the running tunnel instead (see evaluateNetwork).
    private fun connect(configId: String, configYaml: String) {
        FileLog.i("connect: establishing tunnel")
        val connectStartedAt = System.currentTimeMillis()
        Diag.log(
            Diag.Cat.VPN, "connectStart",
            "configId" to configId,
            "smartEnabled" to RoutingStore.smartEnabled,
            "autoEnabled" to RoutingStore.autoEnabled,
        )
        VpnStateHolder.update(ConnectionStatus.CONNECTING, "Establishing tunnel...", configId)
        showPersistentNotification(ConnectionStatus.CONNECTING)
        activeConfigId = configId
        activeConfigYaml = configYaml

        executor.execute {
            // Tear down any previous tunnel first - switching from one saved config to
            // another reuses this same connect() call, not a separate disconnect step.
            // Re-registering the network callback fresh below (rather than trying to
            // reuse the old one) is what resets its "which network did we just dial on"
            // baseline after a reconnect, so it doesn't immediately re-trigger itself.
            unregisterNetworkCallback()
            stopTunnelBounded(tunnel, "switching config")
            tunnel = null
            Mobile.setPingProtector(null)
            try {
                tunInterface?.close()
            } catch (e: Throwable) {
                FileLog.e("tun close error (switching config)", e)
            }
            tunInterface = null

            try {
                val cm = getSystemService(ConnectivityManager::class.java)
                // Picked from the physical networks directly, not
                // cm.activeNetwork: mid-reconnect that returned this app's own
                // previous VPN (it isn't excluded from itself), which then got
                // declared as the new VPN's underlying network and left split
                // DNS with no resolver to use.
                val underlyingNetwork = pickPhysicalNetwork(cm)
                boundNetwork = underlyingNetwork
                logNetwork("underlyingNetwork", cm, underlyingNetwork)

                val builder = Builder()
                    .setSession("Phantom")
                    .addAddress("10.10.0.2", 32)
                    .addRoute("0.0.0.0", 0)
                    .addRoute("::", 0)
                    // Not a real resolver - nothing listens here. A well-known public
                    // one (1.1.1.1, 8.8.8.8) used to trigger Android's "Private DNS:
                    // Automatic" opportunistic upgrade to DNS-over-TLS against that
                    // same address, since both are recognized DoT providers; once that
                    // happens DNS leaves as encrypted port-853 traffic this app can
                    // never see the plaintext of, so smart-routing's domain matching
                    // silently stops working. mobile.go's netstack.Tunnel.SetDNSUpstream
                    // rewrites queries aimed here to a real upstream over the tunnel.
                    .addDnsServer("10.10.0.1")
                    .setMtu(MTU)
                // Tells Android which physical network the tunnel's own uplink traffic
                // rides on (metered-status inheritance, and lets the system correctly
                // attribute this VPN to that network) - best-effort, connect() still
                // works without it if there's genuinely no active network to report yet.
                underlyingNetwork?.let { builder.setUnderlyingNetworks(arrayOf(it)) }
                val pfd = builder.establish()

                if (pfd == null) {
                    FileLog.e("VpnService.Builder.establish() returned null (permission not granted)")
                    VpnStateHolder.update(ConnectionStatus.ERROR, "VPN permission not granted")
                    showPersistentNotification(ConnectionStatus.ERROR)
                    stopForeground(STOP_FOREGROUND_DETACH)
                    stopSelf()
                    return@execute
                }
                tunInterface = pfd
                FileLog.i("tun established, fd=${pfd.fd}, calling Mobile.start")

                // The Go core dials the real Phantom server itself; without protecting
                // that socket it would get captured by the 0.0.0.0/0 route we just set up
                // and loop back into the tunnel it's trying to establish.
                val protector = object : Protector {
                    override fun protect(fd: Long): Boolean = this@PhantomVpnService.protect(fd.toInt())
                }

                tunnel = Mobile.start(configYaml, pfd.fd.toLong(), MTU.toLong(), protector)
                // Config pings and auto-select probes run in this same process,
                // so while this tunnel is up they'd be captured by it too and
                // measure the path through the current server instead of the
                // direct one - see mobile/pingpath.go. Cleared wherever the
                // tunnel is torn down.
                Mobile.setPingProtector(protector)
                // Where smart mode's non-listed names resolve (split DNS): the
                // physical network's own resolver, captured before our tunnel
                // became the default - exactly where the query would have gone
                // with the VPN off. See mobile.Tunnel.SetDirectDNS.
                val directDns = runCatching {
                    cm?.getLinkProperties(underlyingNetwork)?.dnsServers
                        ?.joinToString(",") { it.hostAddress ?: "" }
                }.getOrNull().orEmpty()
                tunnel?.setDirectDNS(directDns)

                // Applied right after the tunnel exists and before it's
                // announced as connected, so the very first flow already sees
                // the user's routing choice rather than briefly carrying
                // everything while the UI catches up.
                RoutingController.applyToTunnel(tunnel)
                // A tunnel that was just (re)built - for any reason: a fresh
                // connect, a config switch, the smart selector moving servers,
                // a network-change reconnect, or the user's own "Применить" -
                // is by definition carrying the current site list already, so
                // there is nothing left to apply.
                RoutingStore.clearSitesDirty()

                FileLog.i("Mobile.start returned, tunnel connected")
                // Wall-clock cost of the whole connect path - the number to
                // look at for any "connecting feels slow" report, and the one
                // that made the Windows DNS stall visible earlier.
                Diag.log(
                    Diag.Cat.VPN, "connectOk",
                    "configId" to configId,
                    "ms" to (System.currentTimeMillis() - connectStartedAt),
                )
                VpnStateHolder.update(ConnectionStatus.CONNECTED, "Connected", configId)
                showPersistentNotification(ConnectionStatus.CONNECTED)
                registerNetworkCallback(cm)
            } catch (e: Throwable) {
                FileLog.e("connect failed", e)
                Diag.log(
                    Diag.Cat.VPN, "connectFail",
                    "configId" to configId,
                    "ms" to (System.currentTimeMillis() - connectStartedAt),
                    "error" to (e.message ?: e::class.java.simpleName),
                )
                VpnStateHolder.update(ConnectionStatus.ERROR, e.message ?: "connection failed", configId)
                disconnect()
            }
        }
    }

    // Watches the physical networks (NOT_VPN - never our own tunnel, see the
    // field comment above) so the tunnel can follow the one it runs over: a
    // live TCP/TLS socket doesn't migrate to a new interface, it just dies.
    // Every event only schedules evaluateNetwork, which decides whether
    // anything actually needs to move - including the burst of onAvailable
    // replays registering fires for networks that already exist.
    private fun registerNetworkCallback(cm: ConnectivityManager?) {
        if (cm == null) return
        connectivityManager = cm

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()

        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                logNetwork("netAvailable", cm, network)
                scheduleNetworkEvaluation()
            }
            override fun onLost(network: Network) {
                Diag.log(
                    Diag.Cat.VPN, "netLost",
                    "network" to network.toString(),
                    "wasBound" to (network == boundNetwork),
                )
                scheduleNetworkEvaluation()
            }
            // Wi-Fi coming back usually arrives unvalidated and only becomes
            // the better network once Android has checked it - this is where
            // that shows up. Also fires for things like signal strength, which
            // is fine: evaluation only acts when the best network changes.
            override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
                scheduleNetworkEvaluation()
            }
        }
        networkCallback = callback
        try {
            cm.registerNetworkCallback(request, callback)
        } catch (e: Throwable) {
            FileLog.e("registerNetworkCallback failed", e)
        }
    }

    /**
     * One line describing a physical network as the tunnel sees it. Several of
     * these fields can each take the whole device offline on their own, which
     * is why they're logged rather than inferred later:
     *  - privateDnsActive/privateDnsServer: a strict Private DNS hostname sends
     *    DNS over TLS to that host, around this app's DNS entirely;
     *  - lockdown ("Block connections without VPN"): while the tunnel is down
     *    or reconnecting, Android drops *all* traffic, not just listed sites;
     *  - validated: Android itself has decided the network has no internet.
     */
    private fun logNetwork(event: String, cm: ConnectivityManager?, network: Network?) {
        if (cm == null || network == null) {
            Diag.log(Diag.Cat.VPN, event, "network" to "none")
            return
        }
        val caps = runCatching { cm.getNetworkCapabilities(network) }.getOrNull()
        val lp = runCatching { cm.getLinkProperties(network) }.getOrNull()
        // VPN checked first: a VPN network also carries its underlying
        // network's transport, so checked last it logged as "wifi".
        val transport = when {
            caps == null -> "unknown"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "vpn"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            else -> "other"
        }
        Diag.log(
            Diag.Cat.VPN, event,
            "network" to network.toString(),
            "transport" to transport,
            "validated" to caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED),
            "metered" to (caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) == false),
            "privateDnsActive" to (if (Build.VERSION.SDK_INT >= 28) lp?.isPrivateDnsActive else null),
            "privateDnsServer" to (if (Build.VERSION.SDK_INT >= 28) lp?.privateDnsServerName else null),
            "dnsServers" to lp?.dnsServers?.joinToString(",") { it.hostAddress ?: "?" },
            "alwaysOn" to (if (Build.VERSION.SDK_INT >= 29) runCatching { isAlwaysOn }.getOrNull() else null),
            "lockdown" to (if (Build.VERSION.SDK_INT >= 29) runCatching { isLockdownEnabled }.getOrNull() else null),
        )
    }

    /**
     * The physical network the tunnel should run over: validated beats
     * unvalidated, then ethernet > Wi-Fi > cellular. Stays on [boundNetwork]
     * unless something is strictly better, so two equally good networks can't
     * bounce the tunnel between them.
     */
    private fun pickPhysicalNetwork(cm: ConnectivityManager?): Network? {
        if (cm == null) return null
        @Suppress("DEPRECATION")
        val all = runCatching { cm.allNetworks.toList() }.getOrDefault(emptyList())
        val scored = all.mapNotNull { n ->
            val caps = runCatching { cm.getNetworkCapabilities(n) }.getOrNull() ?: return@mapNotNull null
            val physical = !caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) &&
                caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN) &&
                caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            if (physical) n to networkScore(caps) else null
        }
        if (scored.isEmpty()) return null
        val best = scored.maxBy { it.second }
        val bound = boundNetwork
        val boundScore = scored.firstOrNull { it.first == bound }?.second
        return if (bound != null && boundScore != null && boundScore >= best.second) bound else best.first
    }

    private fun networkScore(caps: NetworkCapabilities): Int {
        var score = 0
        if (caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)) score += 100
        score += when {
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> 20
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> 10
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> 5
            else -> 0
        }
        return score
    }

    // Debounced and not reset by later events: a burst of events leads to one
    // evaluation, and a steady trickle (onCapabilitiesChanged) can't postpone
    // it forever.
    private fun scheduleNetworkEvaluation() {
        if (!networkEvaluationPending.compareAndSet(false, true)) return
        reconnectHandler.postDelayed({
            networkEvaluationPending.set(false)
            evaluateNetwork()
        }, NETWORK_SETTLE_MS + stormBackoffMs())
    }

    /**
     * Moves the tunnel to the best physical network if that's no longer the
     * one it's on - by telling Android the new underlying network and asking
     * the core to redial the server on it (Tunnel.networkChanged). The VPN
     * interface itself is left alone.
     *
     * This used to rebuild the whole VPN on *any* physical network event.
     * Besides dropping every connection on the device each time, on some
     * phones it fed itself: each new VPN interface made the system bring up a
     * mobile-data network for a few seconds, whose disappearance was another
     * "network change" - measured in the field as a full reconnect every ~7s,
     * the internet effectively down for as long as it lasted.
     */
    private fun evaluateNetwork() {
        val cm = connectivityManager ?: return
        val live = tunnel ?: return
        val bound = boundNetwork
        val best = pickPhysicalNetwork(cm)
        if (best == null) {
            // Nothing usable right now. The tunnel stays as it is; the next
            // network to appear is evaluated like any other change.
            Diag.log(Diag.Cat.VPN, "noPhysicalNetwork", "bound" to bound?.toString())
            return
        }
        if (best == bound) return

        val switches = recordSwitch()
        if (switches >= STORM_SWITCHES_PER_MINUTE) {
            Diag.log(
                Diag.Cat.VPN, "networkStorm",
                "switchesLastMinute" to switches,
                "nextEvaluationDelayMs" to NETWORK_SETTLE_MS + stormBackoffMs(),
            )
        }
        boundNetwork = best
        Diag.log(Diag.Cat.VPN, "networkSwitch", "from" to bound?.toString(), "to" to best.toString())
        logNetwork("underlyingNetwork", cm, best)
        runCatching { setUnderlyingNetworks(arrayOf(best)) }
            .onFailure { FileLog.e("setUnderlyingNetworks failed", it) }
        val dns = runCatching {
            cm.getLinkProperties(best)?.dnsServers?.joinToString(",") { it.hostAddress ?: "" }
        }.getOrNull().orEmpty()
        executor.execute {
            runCatching { live.networkChanged(dns) }
                .onFailure { FileLog.e("networkChanged failed", it) }
            // Whatever the selector last measured was measured from the old
            // network - re-probe rather than wait out a full tick.
            RoutingController.probeNow()
        }
    }

    /** Records a switch now and returns how many happened in the last minute. */
    private fun recordSwitch(): Int = synchronized(recentSwitches) {
        val now = System.currentTimeMillis()
        recentSwitches.addLast(now)
        while (recentSwitches.isNotEmpty() && now - recentSwitches.first() > 60_000) recentSwitches.removeFirst()
        recentSwitches.size
    }

    /** Extra delay before acting on network events while they're flapping:
     *  0 normally, then 10s, 20s, 40s... capped at a minute. A flapping
     *  network still gets followed, just not on every single flap. */
    private fun stormBackoffMs(): Long = synchronized(recentSwitches) {
        val now = System.currentTimeMillis()
        val recent = recentSwitches.count { now - it <= 60_000 }
        if (recent < STORM_SWITCHES_PER_MINUTE) return 0L
        val steps = (recent - STORM_SWITCHES_PER_MINUTE).coerceAtMost(3)
        (STORM_BACKOFF_BASE_MS shl steps).coerceAtMost(STORM_BACKOFF_MAX_MS)
    }

    private fun unregisterNetworkCallback() {
        networkCallback?.let { cb ->
            try {
                connectivityManager?.unregisterNetworkCallback(cb)
            } catch (e: Throwable) {
                FileLog.e("unregisterNetworkCallback error", e)
            }
        }
        networkCallback = null
        connectivityManager = null
    }

    // Registers the proxy's physical-network watch when at least one proxy is
    // running and tears it down when none are - idempotent, safe to call after
    // any proxy start/stop. See the field comment above for why the proxy needs
    // its own watch separate from the VPN's.
    private fun ensureProxyNetworkWatch() {
        if (!ProxyManager.hasAnyRunning()) {
            unregisterProxyNetworkWatch()
            return
        }
        if (proxyNetworkCallback != null) return
        val cm = getSystemService(ConnectivityManager::class.java) ?: return
        proxyConnectivityManager = cm
        proxyWatchRegisteredAtMs = System.currentTimeMillis()

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()

        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = onProxyPhysicalNetworkEvent()
            override fun onLost(network: Network) = onProxyPhysicalNetworkEvent()
        }
        proxyNetworkCallback = callback
        try {
            cm.registerNetworkCallback(request, callback)
        } catch (e: Throwable) {
            FileLog.e("proxy registerNetworkCallback failed", e)
            proxyNetworkCallback = null
        }
    }

    // Initial-replay grace + debounce: registering replays onAvailable for
    // networks that already exist, and a real handover fires a burst of
    // events. The action is just "redial the proxy pools" (cheap), run on the
    // executor since ProxyManager.reconnectAll closes sockets.
    private fun onProxyPhysicalNetworkEvent() {
        if (System.currentTimeMillis() - proxyWatchRegisteredAtMs < 2000) return
        pendingProxyReconnect?.let { proxyReconnectHandler.removeCallbacks(it) }
        val runnable = Runnable {
            FileLog.i("underlying network changed, reconnecting proxy pools")
            executor.execute { ProxyManager.reconnectAll() }
        }
        pendingProxyReconnect = runnable
        proxyReconnectHandler.postDelayed(runnable, 1500)
    }

    private fun unregisterProxyNetworkWatch() {
        proxyNetworkCallback?.let { cb ->
            try {
                proxyConnectivityManager?.unregisterNetworkCallback(cb)
            } catch (e: Throwable) {
                FileLog.e("proxy unregisterNetworkCallback error", e)
            }
        }
        proxyNetworkCallback = null
        proxyConnectivityManager = null
        pendingProxyReconnect?.let { proxyReconnectHandler.removeCallbacks(it) }
        pendingProxyReconnect = null
    }

    // The notification's "Подключить Proxy" - no Activity involved, so config choice
    // mirrors ACTION_CONNECT's own notification path: the last-active config, falling
    // back to the first saved one, with its remembered port (or any free port if this
    // config never ran a proxy before). Runs on the executor since it's a real dial.
    private fun connectProxyFromNotification() {
        executor.execute {
            val saved = ConfigStore.loadAll(this)
            val resumed = ConfigStore.loadLastActiveId(this)?.let { last -> saved.find { it.id == last } }
                ?: saved.firstOrNull()
            if (resumed == null) {
                FileLog.e("proxy connect from notification: no saved config")
                return@execute
            }
            runBlocking {
                ProxyManager.start(resumed.id, resumed.yaml, resumed.proxyPort ?: 0, lazyProtector)
                    .onSuccess { port -> ConfigStore.setProxyPort(this@PhantomVpnService, resumed.id, port) }
                    .onFailure { e -> FileLog.e("proxy connect from notification failed", e) }
            }
            // Success or not, re-render so the action button/status text match reality
            // (and the service becomes foreground if the proxy did start).
            ensureProxyNetworkWatch()
            showPersistentNotification(VpnStateHolder.state.value.status)
        }
    }

    /**
     * Stops [old] on a thread of its own and waits for it at most
     * [TUNNEL_STOP_DEADLINE_MS]; past that it's abandoned, stop call and all,
     * and the caller carries on and closes the interface.
     *
     * A config switch used to call tunnel.stop() directly, with no deadline,
     * and only close the old interface once it returned. When it wedged, the
     * VPN interface stayed up in front of a tunnel that could no longer carry
     * anything, and the whole device was offline until the user turned smart
     * mode off - disconnect() had a watchdog, a switch didn't. The Go side
     * bounds its own teardown now too; this is here so the interface comes
     * down on time even if something there ever wedges again.
     */
    private fun stopTunnelBounded(old: Tunnel?, reason: String) {
        if (old == null) return
        val started = System.currentTimeMillis()
        val done = CountDownLatch(1)
        Thread({
            try {
                old.stop()
            } catch (e: Throwable) {
                FileLog.e("tunnel stop error ($reason)", e)
            }
            done.countDown()
        }, "phantom-tunnel-stop").apply { isDaemon = true }.start()
        if (!done.await(TUNNEL_STOP_DEADLINE_MS, TimeUnit.MILLISECONDS)) {
            Diag.log(Diag.Cat.VPN, "tunnelStopAbandoned", "reason" to reason, "waitedMs" to (System.currentTimeMillis() - started))
        }
    }

    /**
     * The Go core's tunnel.stop() used to wedge and never return. It ran on
     * [executor] - a single-thread executor - so that jammed the thread forever,
     * and every future connect()/disconnect() call (which all go through the
     * same executor) silently queued up behind it: from the user's side, the
     * app just stopped responding to the disconnect toggle. The stop itself is
     * bounded now (stopTunnelBounded); the [DISCONNECT_FORCE_TIMEOUT_MS]
     * watchdog below stays as the net for anything else that ever jams the
     * executor - see forceDisconnect().
     */
    private fun disconnect() {
        val forceRunnable = Runnable { forceDisconnect() }
        reconnectHandler.postDelayed(forceRunnable, DISCONNECT_FORCE_TIMEOUT_MS)

        executor.execute {
            reconnectHandler.removeCallbacks(forceRunnable)
            unregisterNetworkCallback()
            activeConfigId = null
            activeConfigYaml = null
            stopTunnelBounded(tunnel, "disconnect")
            tunnel = null
            Mobile.setPingProtector(null)

            try {
                tunInterface?.close()
            } catch (e: Throwable) {
                FileLog.e("tun close error", e)
            }
            tunInterface = null

            VpnStateHolder.update(ConnectionStatus.IDLE, "")
            // Refresh the notification to its idle/"Подключить" form and only then detach
            // from foreground - DETACH (not REMOVE) leaves the ongoing notification posted
            // so it stays in the shade after this service instance stops.
            showPersistentNotification(ConnectionStatus.IDLE)
            stopForeground(STOP_FOREGROUND_DETACH)
            stopSelf()
        }
    }

    /**
     * Escalation path for [disconnect]: fires only if the graceful teardown on
     * [executor] hasn't finished within [DISCONNECT_FORCE_TIMEOUT_MS], which means
     * that thread is stuck - behind some earlier task, since its own tunnel stop
     * is bounded well within that. Runs on the main
     * thread (this is a Handler(Looper.getMainLooper()) callback), so it doesn't
     * wait on the wedged executor at all: it closes the OS-level tun interface
     * directly - the part that actually matters to the user - resets visible
     * state, and swaps in a fresh executor so connect()/disconnect() work again
     * immediately. The old executor thread, and whatever it's stuck in, is
     * simply abandoned.
     */
    private fun forceDisconnect() {
        FileLog.e("disconnect did not finish within ${DISCONNECT_FORCE_TIMEOUT_MS}ms - forcing it")
        executor = Executors.newSingleThreadExecutor()
        unregisterNetworkCallback()
        activeConfigId = null
        activeConfigYaml = null
        tunnel = null
        Mobile.setPingProtector(null)
        try {
            tunInterface?.close()
        } catch (e: Throwable) {
            FileLog.e("tun close error (forced)", e)
        }
        tunInterface = null

        VpnStateHolder.update(ConnectionStatus.IDLE, "")
        showPersistentNotification(ConnectionStatus.IDLE)
        stopForeground(STOP_FOREGROUND_DETACH)
        stopSelf()
    }

    override fun onDestroy() {
        if (activeInstance == this) activeInstance = null
        unregisterProxyNetworkWatch()
        disconnect()
        super.onDestroy()
    }

    override fun onRevoke() {
        disconnect()
        super.onRevoke()
    }

    /**
     * Posts/refreshes the always-visible connect/disconnect notification for [status],
     * and puts this service in (or out of) foreground state to match - not just for
     * the VPN itself, but also while any independent proxy is running (see
     * ACTION_PROXY_STATE_CHANGED/ProxyManager.hasAnyRunning): a proxy alone doesn't
     * need VpnService's own privileges, but its sockets still live in this process, and
     * without a foreground service Android's background network throttling would make
     * it increasingly unreliable once the app is backgrounded.
     */
    private fun showPersistentNotification(status: ConnectionStatus) {
        val notification = buildNotification(status)
        val shouldBeForeground = status == ConnectionStatus.CONNECTING ||
            status == ConnectionStatus.CONNECTED ||
            ProxyManager.hasAnyRunning()
        if (shouldBeForeground) {
            startForeground(NOTIFICATION_ID, notification)
        } else {
            // Only meaningful if we were previously foregrounded (e.g. the last
            // running proxy just stopped) - a harmless no-op otherwise. DETACH
            // leaves the notification itself posted, matching disconnect()'s own
            // "stays visible in the shade" behavior.
            stopForeground(STOP_FOREGROUND_DETACH)
            getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification)
        }
    }

    private fun buildNotification(status: ConnectionStatus): Notification {
        ensureChannel()

        // Two independent facts, two independent action buttons - the full-tunnel VPN
        // and the standalone proxy are unrelated features that just share this one
        // notification (one process, one foreground exemption - see the class docs).
        val vpnText = when (status) {
            ConnectionStatus.CONNECTED -> I18n.t("active")
            ConnectionStatus.CONNECTING -> I18n.t("connecting")
            ConnectionStatus.ERROR -> I18n.t("error_short")
            ConnectionStatus.IDLE -> I18n.t("inactive")
        }
        // "Умный VPN" only when it's actually what's driving the tunnel right now
        // (see RoutingController.applyToTunnel) - a manual config or "Выбирать
        // лучшую" both still tunnel the whole device, so they keep the plain
        // "VPN" label. Read live rather than cached so a mode change that
        // doesn't itself reconnect (see applyRoutingToActiveTunnel) still shows
        // up correctly the next time this rebuilds.
        val smartDriving = status == ConnectionStatus.CONNECTED &&
            RoutingStore.smartEnabled && !RoutingStore.autoEnabled
        val vpnLabel = if (smartDriving) I18n.t("smart_vpn") else "VPN"
        val countrySuffix = activeConfigId
            ?.let { id -> ConfigStore.loadAll(this).find { it.id == id } }
            ?.let { cfg ->
                val name = cfg.country ?: cfg.countryCode ?: return@let null
                val flag = cfg.countryCode?.let { countryCodeToFlag(it) }.orEmpty()
                "$flag $name".trim()
            }
        val vpnLine = if (status == ConnectionStatus.CONNECTED && countrySuffix != null) {
            "$vpnLabel: $vpnText | $countrySuffix"
        } else {
            "$vpnLabel: $vpnText"
        }
        val proxyRunning = ProxyManager.hasAnyRunning()
        // Mirrors Settings' "show proxy settings" toggle - when the proxy controls
        // are hidden from the config tiles, they should not leak back in here either.
        val showProxy = Appearance.showProxySettings
        val text = if (showProxy) {
            "$vpnLine | Proxy: ${if (proxyRunning) I18n.t("active") else I18n.t("inactive")}"
        } else {
            vpnLine
        }

        val vpnAction = when (status) {
            ConnectionStatus.CONNECTED -> I18n.t("disconnect_vpn") to disconnectPendingIntent()
            ConnectionStatus.CONNECTING -> I18n.t("cancel_action") to disconnectPendingIntent()
            else -> I18n.t("connect_vpn") to connectPendingIntent()
        }
        val proxyAction =
            if (proxyRunning) I18n.t("disconnect_proxy") to proxyDisconnectPendingIntent()
            else I18n.t("connect_proxy") to proxyConnectPendingIntent()

        val openAppIntent = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE
        )

        val actionIcon = Icon.createWithResource(this, R.drawable.ic_notification)

        val builder = Notification.Builder(this, CHANNEL_ID)
            .setContentTitle("Phantom VPN")
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentIntent(openAppIntent)
            .addAction(Notification.Action.Builder(actionIcon, vpnAction.first, vpnAction.second).build())
            .setOngoing(true)
        if (showProxy) {
            builder.addAction(Notification.Action.Builder(actionIcon, proxyAction.first, proxyAction.second).build())
        }
        return builder.build()
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID, "Phantom VPN", NotificationManager.IMPORTANCE_LOW
            )
            getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
        }
    }

    private fun connectPendingIntent(): PendingIntent {
        val intent = Intent(this, PhantomVpnService::class.java).apply { action = ACTION_CONNECT }
        return PendingIntent.getService(this, 1, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    private fun disconnectPendingIntent(): PendingIntent {
        val intent = Intent(this, PhantomVpnService::class.java).apply {
            action = ACTION_DISCONNECT
            putExtra(EXTRA_FROM_NOTIFICATION, true)
        }
        return PendingIntent.getService(this, 2, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    private fun proxyConnectPendingIntent(): PendingIntent {
        val intent = Intent(this, PhantomVpnService::class.java).apply { action = ACTION_PROXY_CONNECT }
        return PendingIntent.getService(this, 3, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    private fun proxyDisconnectPendingIntent(): PendingIntent {
        val intent = Intent(this, PhantomVpnService::class.java).apply { action = ACTION_PROXY_DISCONNECT }
        return PendingIntent.getService(this, 4, intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }
}
