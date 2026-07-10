package com.phantom.vpn

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.graphics.drawable.Icon
import android.net.ConnectivityManager
import android.net.Network
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import mobile.Mobile
import mobile.Protector
import mobile.Tunnel
import java.util.concurrent.Executors

class PhantomVpnService : VpnService() {

    companion object {
        const val ACTION_CONNECT = "com.phantom.vpn.CONNECT"
        const val ACTION_DISCONNECT = "com.phantom.vpn.DISCONNECT"
        const val ACTION_SHOW_STATUS = "com.phantom.vpn.SHOW_STATUS"
        const val EXTRA_CONFIG_YAML = "config_yaml"
        const val EXTRA_CONFIG_ID = "config_id"

        private const val CHANNEL_ID = "phantom_vpn"
        private const val NOTIFICATION_ID = 1
        private const val MTU = 1500
    }

    private val executor = Executors.newSingleThreadExecutor()
    private var tunInterface: ParcelFileDescriptor? = null
    private var tunnel: Tunnel? = null

    // Reconnect-on-network-change: a plain TCP/TLS socket bound to (say) Wi-Fi
    // doesn't migrate itself when Wi-Fi disappears and cellular takes over - it
    // just dies, and since nothing was watching for that, the whole tunnel would
    // silently stop passing traffic instead of reconnecting. lastNetwork is
    // primed with whatever network we actually dialed on, so the callback's own
    // first invocation (which just reports that same network back) doesn't
    // trigger a spurious reconnect right after connecting.
    private var connectivityManager: ConnectivityManager? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null
    private var lastNetwork: Network? = null
    private var activeConfigId: String? = null
    private var activeConfigYaml: String? = null
    private val reconnectHandler = Handler(Looper.getMainLooper())
    private var pendingReconnect: Runnable? = null

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
                registerNetworkCallback(cm, underlyingNetwork)
            } catch (e: Throwable) {
                FileLog.e("connect failed", e)
                VpnStateHolder.update(ConnectionStatus.ERROR, e.message ?: "connection failed", configId)
                disconnect()
            }
        }
    }

    // Watches for the device's active default network changing underneath the
    // tunnel (Wi-Fi <-> cellular, Wi-Fi disappearing entirely, etc.) and
    // reconnects from scratch when it does - a live TCP/TLS socket doesn't
    // migrate itself to a new interface, it just dies, so without this the
    // tunnel would silently stop passing any traffic until the user noticed
    // and reconnected manually.
    private fun registerNetworkCallback(cm: ConnectivityManager?, initialNetwork: Network?) {
        if (cm == null) return
        connectivityManager = cm
        lastNetwork = initialNetwork

        val callback = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                val previous = lastNetwork
                lastNetwork = network
                if (previous == null || previous == network) return
                FileLog.i("underlying network changed, scheduling reconnect")
                scheduleReconnect()
            }
        }
        networkCallback = callback
        try {
            cm.registerDefaultNetworkCallback(callback)
        } catch (e: Throwable) {
            FileLog.e("registerDefaultNetworkCallback failed", e)
        }
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
        lastNetwork = null
        pendingReconnect?.let { reconnectHandler.removeCallbacks(it) }
        pendingReconnect = null
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
        disconnect()
        super.onDestroy()
    }

    override fun onRevoke() {
        disconnect()
        super.onRevoke()
    }

    /** Posts/refreshes the always-visible connect/disconnect notification for [status]. */
    private fun showPersistentNotification(status: ConnectionStatus) {
        val notification = buildNotification(status)
        if (status == ConnectionStatus.CONNECTING || status == ConnectionStatus.CONNECTED) {
            startForeground(NOTIFICATION_ID, notification)
        } else {
            getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification)
        }
    }

    private fun buildNotification(status: ConnectionStatus): Notification {
        ensureChannel()

        val (text, actionLabel, actionIntent) = when (status) {
            ConnectionStatus.CONNECTED -> Triple("Подключено", "Отключить", disconnectPendingIntent())
            ConnectionStatus.CONNECTING -> Triple("Подключение...", "Отменить", disconnectPendingIntent())
            ConnectionStatus.ERROR -> Triple("Ошибка подключения", "Подключить", connectPendingIntent())
            ConnectionStatus.IDLE -> Triple("Отключено", "Подключить", connectPendingIntent())
        }

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
            .addAction(Notification.Action.Builder(actionIcon, actionLabel, actionIntent).build())
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
}
