package com.phantom.vpn

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import mobile.Mobile
import mobile.Protector
import mobile.Tunnel
import java.util.concurrent.Executors

class PhantomVpnService : VpnService() {

    companion object {
        const val ACTION_CONNECT = "com.phantom.vpn.CONNECT"
        const val ACTION_DISCONNECT = "com.phantom.vpn.DISCONNECT"
        const val EXTRA_CONFIG_YAML = "config_yaml"

        private const val CHANNEL_ID = "phantom_vpn"
        private const val NOTIFICATION_ID = 1
        private const val MTU = 1500
    }

    private val executor = Executors.newSingleThreadExecutor()
    private var tunInterface: ParcelFileDescriptor? = null
    private var tunnel: Tunnel? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        FileLog.i("onStartCommand action=${intent?.action}")
        when (intent?.action) {
            ACTION_DISCONNECT -> {
                disconnect()
                return START_NOT_STICKY
            }
            ACTION_CONNECT -> {
                val yaml = intent.getStringExtra(EXTRA_CONFIG_YAML)
                if (yaml.isNullOrBlank()) {
                    FileLog.e("connect requested with empty config")
                    VpnStateHolder.update(ConnectionStatus.ERROR, "Missing client.yaml contents")
                    stopSelf()
                    return START_NOT_STICKY
                }
                connect(yaml)
            }
        }
        return START_STICKY
    }

    private fun connect(configYaml: String) {
        FileLog.i("connect: establishing tunnel")
        VpnStateHolder.update(ConnectionStatus.CONNECTING, "Establishing tunnel...")
        startForeground(NOTIFICATION_ID, buildNotification("Connecting..."))

        executor.execute {
            try {
                val pfd = Builder()
                    .setSession("Phantom")
                    .addAddress("10.10.0.2", 32)
                    .addRoute("0.0.0.0", 0)
                    .addRoute("::", 0)
                    .addDnsServer("1.1.1.1")
                    .addDnsServer("8.8.8.8")
                    .setMtu(MTU)
                    .establish()

                if (pfd == null) {
                    FileLog.e("VpnService.Builder.establish() returned null (permission not granted)")
                    VpnStateHolder.update(ConnectionStatus.ERROR, "VPN permission not granted")
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
                VpnStateHolder.update(ConnectionStatus.CONNECTED, "Connected")
                updateNotification("Connected")
            } catch (e: Throwable) {
                FileLog.e("connect failed", e)
                VpnStateHolder.update(ConnectionStatus.ERROR, e.message ?: "connection failed")
                disconnect()
            }
        }
    }

    private fun disconnect() {
        executor.execute {
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
            stopForeground(STOP_FOREGROUND_REMOVE)
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

    private fun buildNotification(text: String): Notification {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID, "Phantom VPN", NotificationManager.IMPORTANCE_LOW
            )
            getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
        }

        val openAppIntent = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE
        )

        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle("Phantom VPN")
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentIntent(openAppIntent)
            .setOngoing(true)
            .build()
    }

    private fun updateNotification(text: String) {
        val manager = getSystemService(NotificationManager::class.java)
        manager.notify(NOTIFICATION_ID, buildNotification(text))
    }
}
