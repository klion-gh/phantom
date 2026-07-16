package com.phantom.vpn

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import mobile.Mobile
import mobile.ProxyHandle

/**
 * Independent per-config local SOCKS5 proxy - unrelated to the full-tunnel VPN
 * (PhantomVpnService/VpnStateHolder); mirrors windows/proxymanager.go. Point another
 * app's own SOCKS5 proxy setting (e.g. Telegram's) at 127.0.0.1:<port> to route just
 * that one app through Phantom, without needing the full-tunnel VPN active at all -
 * and without conflicting with it if it happens to be active too, for this config or
 * any other (this dials its own separate pool of connections to the same server).
 *
 * State lives here (a plain object, not a Service) rather than in PhantomVpnService -
 * unlike the full VPN, a SOCKS5 listener needs no special Android permission/lifecycle,
 * so it's tracked for as long as this process is alive. If Android kills the app process
 * while backgrounded, any running proxy stops along with it (same tradeoff as most
 * auxiliary, non-foreground-service features) - reopening the app doesn't auto-resume
 * it, matching how the full VPN's own "Connect" state doesn't persist across restarts.
 */
object ProxyManager {
    private val handles = mutableMapOf<String, ProxyHandle>()

    /**
     * Starts (or, if already running, just reports) configId's proxy. requestedPort, if
     * non-zero, is the exact port to bind - the UI's own port field is only editable
     * while off, pre-filled with whatever port this config used successfully last time,
     * so failing to bind it comes back as a real [Result.failure] rather than silently
     * substituting a different port - the caller shows that to the user instead of
     * quietly picking something else. requestedPort == 0 means "any free port".
     */
    suspend fun start(configId: String, yaml: String, requestedPort: Int): Result<Int> = withContext(Dispatchers.IO) {
        handles[configId]?.let { return@withContext Result.success(it.port().toInt()) }
        runCatching {
            val handle = Mobile.startProxy(yaml, requestedPort.toLong())
            handles[configId] = handle
            handle.port().toInt()
        }
    }

    fun stop(configId: String) {
        handles.remove(configId)?.stop()
    }

    fun isRunning(configId: String): Boolean = handles.containsKey(configId)

    fun port(configId: String): Int? = handles[configId]?.port()?.toInt()

    /** Tears down every running proxy - called when the app process is really exiting. */
    fun stopAll() {
        val all = handles.toMap()
        handles.clear()
        all.values.forEach { it.stop() }
    }
}
