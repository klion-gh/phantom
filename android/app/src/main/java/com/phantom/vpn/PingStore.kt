package com.phantom.vpn

import androidx.compose.runtime.mutableStateMapOf

/**
 * Latest ping result per saved config, keyed by config id - the one source both
 * the Конфигурации tiles and the Маршрутизация config list read, so the two
 * pages always show the same number for the same server instead of two
 * separately-measured ones drifting apart.
 *
 * Filled only by [check], which runs when someone taps a tile's ping label. A
 * ping is a full TCP+TLS+handshake to the server; on a timer for every saved
 * config while the app was open, that came to hundreds of short sessions an
 * hour to each server - more than any visitor of an ordinary site produces,
 * and the kind of regularity traffic analysis looks for. On demand it is one
 * handshake per tap. Compose state, so a tile recomposes on its own when its
 * entry changes.
 */
object PingStore {
    private val results = mutableStateMapOf<String, PingInfo>()
    private val inFlight = mutableStateMapOf<String, Boolean>()

    operator fun get(configId: String): PingInfo? = results[configId]

    fun isChecking(configId: String): Boolean = inFlight[configId] == true

    /** Pings [config]'s server once. A second tap while one is running does
     * nothing. A failed ping keeps the last-known IP on screen and only clears
     * the latency - the address hasn't stopped being the address. */
    suspend fun check(config: SavedConfig) {
        if (inFlight[config.id] == true) return
        inFlight[config.id] = true
        try {
            val result = fetchPing(config.yaml)
            results[config.id] = if (result != null) {
                PingInfo(result.first, result.second)
            } else {
                (results[config.id] ?: PingInfo()).copy(latencyMs = null, failed = true)
            }
        } finally {
            inFlight.remove(config.id)
        }
    }

    /** Drops what is known about [configId] - after an edit that may point the
     * config at a different server, or a delete. */
    fun forget(configId: String) {
        results.remove(configId)
    }
}
