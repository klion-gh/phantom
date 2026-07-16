package com.phantom.vpn

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withContext
import mobile.Mobile
import mobile.Protector
import mobile.ProxyHandle

/**
 * Independent per-config local SOCKS5 proxy - unrelated to the full-tunnel VPN
 * (PhantomVpnService/VpnStateHolder); mirrors windows/proxymanager.go. Point another
 * app's own SOCKS5 proxy setting (e.g. Telegram's) at 127.0.0.1:<port> to route just
 * that one app through Phantom, without needing the full-tunnel VPN active at all -
 * and without conflicting with it if it happens to be active too, for this config or
 * any other (this dials its own separate pool of connections to the same server).
 *
 * The sockets themselves live here (a plain object), not inside PhantomVpnService - a
 * SOCKS5 listener needs no special Android permission the way VpnService does. But the
 * *process* they run in still needs to be exempt from Android's background network
 * throttling (Doze/App Standby), same as the VPN - MainActivity tells the service to
 * become/stay foregrounded whenever [hasAnyRunning] is true (see
 * PhantomVpnService.ACTION_PROXY_STATE_CHANGED), even if the VPN itself isn't
 * connected. Without that, a backgrounded proxy would run for a while but become
 * increasingly unreliable/high-latency as Android throttles its network access, then
 * eventually stop working - exactly like a plain background service with no foreground
 * exemption.
 */
object ProxyManager {
    private val handles = mutableMapOf<String, ProxyHandle>()

    // The single source of truth for "which configs have a running proxy, on which
    // port" - the UI collects this instead of keeping its own copy, because its own
    // copy dies with the Activity while this object (and the proxies themselves,
    // kept alive by the foreground service) survive it: reopening the app from the
    // notification used to show every PROXY toggle grey even though the proxy was
    // still running. Also what lets the notification's own Proxy actions (handled
    // inside PhantomVpnService, no Activity involved at all) reflect in the UI.
    private val _runningPorts = MutableStateFlow<Map<String, Int>>(emptyMap())
    val runningPorts: StateFlow<Map<String, Int>> = _runningPorts

    // start() mutates from Dispatchers.IO, the notification actions from
    // PhantomVpnService's executor thread, stop() from the main thread - so every
    // access to handles goes through this lock, and publish() recomputes the flow
    // value from scratch under it rather than trying to patch increments in.
    private fun publish() {
        _runningPorts.value = synchronized(handles) {
            handles.entries.associate { it.key to it.value.port().toInt() }
        }
    }

    /**
     * Starts (or, if already running, just reports) configId's proxy. requestedPort, if
     * non-zero, is the exact port to bind - the UI's own port field is only editable
     * while off, pre-filled with whatever port this config used successfully last time,
     * so failing to bind it comes back as a real [Result.failure] rather than silently
     * substituting a different port - the caller shows that to the user instead of
     * quietly picking something else. requestedPort == 0 means "any free port".
     *
     * protector (nilable) exempts this proxy's connections from the full-tunnel VPN's
     * own routing - see PhantomVpnService.lazyProtector's doc for why this is needed
     * even though the proxy has nothing to do with the VPN otherwise.
     */
    suspend fun start(configId: String, yaml: String, requestedPort: Int, protector: Protector?): Result<Int> = withContext(Dispatchers.IO) {
        synchronized(handles) { handles[configId] }?.let { return@withContext Result.success(it.port().toInt()) }
        runCatching {
            val handle = Mobile.startProxy(yaml, requestedPort.toLong(), protector)
            val existing = synchronized(handles) {
                // Lost a race with a concurrent start for the same config - keep
                // whichever one registered first, discard this one.
                handles[configId] ?: run { handles[configId] = handle; null }
            }
            if (existing != null) {
                handle.stop()
                existing.port().toInt()
            } else {
                publish()
                handle.port().toInt()
            }
        }
    }

    fun stop(configId: String) {
        synchronized(handles) { handles.remove(configId) }?.stop()
        publish()
    }

    /**
     * Forces every running proxy to redial its pooled connections at once -
     * called on a network change (see PhantomVpnService's proxy network watch)
     * so a Wi-Fi<->cellular switch recovers immediately instead of on the next
     * request after the dead sockets time out.
     */
    fun reconnectAll() {
        synchronized(handles) { handles.values.toList() }.forEach { it.reconnect() }
    }

    fun isRunning(configId: String): Boolean = synchronized(handles) { handles.containsKey(configId) }

    fun hasAnyRunning(): Boolean = synchronized(handles) { handles.isNotEmpty() }

    fun port(configId: String): Int? = synchronized(handles) { handles[configId] }?.port()?.toInt()

    /** Tears down every running proxy - the notification's "Отключить Proxy" action,
     *  and the app process really exiting. */
    fun stopAll() {
        val all = synchronized(handles) {
            val copy = handles.toMap()
            handles.clear()
            copy
        }
        publish()
        all.values.forEach { it.stop() }
    }
}
