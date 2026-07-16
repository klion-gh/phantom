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
import java.util.concurrent.Executors

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

        private const val CHANNEL_ID = "phantom_vpn"
        private const val NOTIFICATION_ID = 1
        private const val MTU = 1500

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
    }

    override fun onCreate() {
        super.onCreate()
        activeInstance = this
    }

    private val executor = Executors.newSingleThreadExecutor()
    private var tunInterface: ParcelFileDescriptor? = null
    private var tunnel: Tunnel? = null

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
    private var watchRegisteredAtMs: Long = 0
    private var activeConfigId: String? = null
    private var activeConfigYaml: String? = null
    private val reconnectHandler = Handler(Looper.getMainLooper())
    private var pendingReconnect: Runnable? = null

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

    private fun connect(configId: String, configYaml: String) {
        FileLog.i("connect: establishing tunnel")
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
            try {
                tunnel?.stop()
            } catch (e: Throwable) {
                FileLog.e("tunnel stop error (switching config)", e)
            }
            tunnel = null
            try {
                tunInterface?.close()
            } catch (e: Throwable) {
                FileLog.e("tun close error (switching config)", e)
            }
            tunInterface = null

            try {
                val cm = getSystemService(ConnectivityManager::class.java)
                val underlyingNetwork = cm?.activeNetwork

                val builder = Builder()
                    .setSession("Phantom")
                    .addAddress("10.10.0.2", 32)
                    .addRoute("0.0.0.0", 0)
                    .addRoute("::", 0)
                    .addDnsServer("1.1.1.1")
                    .addDnsServer("8.8.8.8")
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

                FileLog.i("Mobile.start returned, tunnel connected")
                VpnStateHolder.update(ConnectionStatus.CONNECTED, "Connected", configId)
                showPersistentNotification(ConnectionStatus.CONNECTED)
                registerNetworkCallback(cm)
            } catch (e: Throwable) {
                FileLog.e("connect failed", e)
                VpnStateHolder.update(ConnectionStatus.ERROR, e.message ?: "connection failed", configId)
                disconnect()
            }
        }
    }

    // Watches for the underlying *physical* network changing (Wi-Fi <-> cellular,
    // Wi-Fi disappearing entirely, a different Wi-Fi network taking over, etc.)
    // and reconnects from scratch when it does - a live TCP/TLS socket doesn't
    // migrate itself to a new interface, it just dies, so without this the
    // tunnel would silently stop passing any traffic until the user noticed and
    // reconnected manually. NOT_VPN excludes our own tunnel from ever
    // triggering this itself - see the field comment above for why that matters.
    private fun registerNetworkCallback(cm: ConnectivityManager?) {
        if (cm == null) return
        connectivityManager = cm
        watchRegisteredAtMs = System.currentTimeMillis()

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()

        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = onPhysicalNetworkEvent()
            override fun onLost(network: Network) = onPhysicalNetworkEvent()
        }
        networkCallback = callback
        try {
            cm.registerNetworkCallback(request, callback)
        } catch (e: Throwable) {
            FileLog.e("registerNetworkCallback failed", e)
        }
    }

    // registerNetworkCallback immediately replays onAvailable for every
    // currently-qualifying physical network as soon as it's registered (i.e.
    // whatever was already connected when we just dialed out on it) - a burst
    // of events that reflects existing state, not a change, so it must not
    // itself count as "the network changed". A short grace period is simpler
    // and more robust here than trying to track exactly how many initial
    // replay events to expect.
    private fun onPhysicalNetworkEvent() {
        if (System.currentTimeMillis() - watchRegisteredAtMs < 2000) return
        FileLog.i("underlying network changed, scheduling reconnect")
        scheduleReconnect()
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
        pendingReconnect?.let { reconnectHandler.removeCallbacks(it) }
        pendingReconnect = null
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

    // Same initial-replay grace + debounce reasoning as onPhysicalNetworkEvent/
    // scheduleReconnect above; here the action is just "redial the proxy pools"
    // (cheap) rather than "rebuild the tunnel", and it runs on the executor
    // since ProxyManager.reconnectAll closes sockets.
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

    // Debounced rather than immediate - a real Wi-Fi<->cellular handover fires
    // several rapid onAvailable/onLost events while things settle, and dialing
    // a fresh tunnel on every single one of them would just race itself.
    private fun scheduleReconnect() {
        pendingReconnect?.let { reconnectHandler.removeCallbacks(it) }
        val runnable = Runnable {
            val id = activeConfigId
            val yaml = activeConfigYaml
            if (id != null && yaml != null) {
                FileLog.i("reconnecting after network change")
                connect(id, yaml)
            }
        }
        pendingReconnect = runnable
        reconnectHandler.postDelayed(runnable, 1500)
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

    private fun disconnect() {
        executor.execute {
            unregisterNetworkCallback()
            activeConfigId = null
            activeConfigYaml = null
            try {
                tunnel?.stop()
            } catch (e: Throwable) {
                FileLog.e("tunnel stop error", e)
            }
            tunnel = null

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
            ConnectionStatus.CONNECTED -> "активен"
            ConnectionStatus.CONNECTING -> "подключение..."
            ConnectionStatus.ERROR -> "ошибка"
            ConnectionStatus.IDLE -> "неактивен"
        }
        val proxyRunning = ProxyManager.hasAnyRunning()
        val text = "VPN: $vpnText | Proxy: ${if (proxyRunning) "активен" else "неактивен"}"

        val vpnAction = when (status) {
            ConnectionStatus.CONNECTED -> "Отключить VPN" to disconnectPendingIntent()
            ConnectionStatus.CONNECTING -> "Отменить" to disconnectPendingIntent()
            else -> "Подключить VPN" to connectPendingIntent()
        }
        val proxyAction =
            if (proxyRunning) "Отключить Proxy" to proxyDisconnectPendingIntent()
            else "Подключить Proxy" to proxyConnectPendingIntent()

        val openAppIntent = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE
        )

        val actionIcon = Icon.createWithResource(this, R.drawable.ic_notification)

        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle("Phantom VPN")
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentIntent(openAppIntent)
            .addAction(Notification.Action.Builder(actionIcon, vpnAction.first, vpnAction.second).build())
            .addAction(Notification.Action.Builder(actionIcon, proxyAction.first, proxyAction.second).build())
            .setOngoing(true)
            .build()
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
        val intent = Intent(this, PhantomVpnService::class.java).apply { action = ACTION_DISCONNECT }
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
