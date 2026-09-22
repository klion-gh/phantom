package com.phantom.vpn

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateMapOf
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive

/**
 * Latest ping result per saved config, keyed by config id - the one source both
 * the Конфигурации tiles and the Маршрутизация config list read, so the two
 * pages always show the same number for the same server instead of two
 * separately-measured ones drifting apart.
 *
 * Filled by [PingPoller]; Compose state, so a tile recomposes on its own when
 * its entry changes.
 */
object PingStore {
    private val results = mutableStateMapOf<String, PingInfo>()

    operator fun get(configId: String): PingInfo? = results[configId]

    /** A failed ping keeps the last-known IP on screen and only clears the
     * latency - the address hasn't stopped being the address. */
    fun record(configId: String, result: Pair<String, Long>?) {
        results[configId] = if (result != null) {
            PingInfo(result.first, result.second)
        } else {
            results[configId]?.copy(latencyMs = null) ?: PingInfo()
        }
    }
}

/**
 * Pings every config in [configs] on its own jittered timer while [enabled],
 * writing into [PingStore]. One loop per config for the whole app, rather than
 * one per tile that shows it - which is what lets a second page show the same
 * value without pinging the same server twice as often.
 */
@Composable
fun PingPoller(configs: List<SavedConfig>, enabled: Boolean) {
    for (config in configs) {
        key(config.id) {
            LaunchedEffectPing(config.yaml, enabled) { result -> PingStore.record(config.id, result) }
        }
    }
}

/**
 * Runs fetchPing on a repeating timer for as long as the calling composable is alive
 * AND [pingEnabled] is true, restarting whenever [yaml] or [pingEnabled] changes -
 * [pingEnabled] going false immediately cancels the loop rather than just skipping a
 * cycle, and going true again resumes with an immediate check rather than waiting
 * out a full interval. Deliberately does *not* reset when only [pingEnabled] flips
 * (a separate effect below handles the real "yaml changed" reset) - otherwise every
 * pause/resume (minimize, switch pager page) would flash the tile to "—" instead of
 * keeping the last-known value on screen, same as the Windows app's visibility-based
 * pause.
 */
@Composable
private fun LaunchedEffectPing(yaml: String, pingEnabled: Boolean, onResult: suspend (Pair<String, Long>?) -> Unit) {
    LaunchedEffect(yaml) {
        onResult(null)
    }
    LaunchedEffect(yaml, pingEnabled) {
        if (!pingEnabled) return@LaunchedEffect
        while (isActive) {
            onResult(fetchPing(yaml))
            // Jittered, not a flat 6s. Each ping is a full TCP+TLS+handshake to the
            // server, so a fixed interval put a perfectly periodic connection every
            // 6.000 seconds on the wire for as long as the app was open. Real
            // browsing produces nothing like that regularity, and a metronome is
            // exactly the kind of behavioural signature traffic analysis looks for -
            // no amount of per-connection disguise hides it. 6-10s keeps the tile
            // feeling live while making the cadence irregular.
            delay(6000L + (0..4000L).random())
        }
    }
}
