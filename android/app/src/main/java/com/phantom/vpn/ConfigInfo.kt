package com.phantom.vpn

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONObject

/** Snapshot of what a config tile shows about its server: live latency only - country/flag
 * are resolved once and cached on the SavedConfig itself, not polled on a timer. */
data class PingInfo(
    val ip: String? = null,
    val latencyMs: Long? = null,
)

/**
 * Reads a top-level `key: value` line out of a raw client.yaml string. No YAML dependency
 * is pulled in for this - the app never needs to touch anything but a couple of scalar
 * fields for display, and the actual parsing/validation happens in the Go core.
 */
fun parseYamlField(yaml: String, key: String): String? {
    val quoted = Regex("""(?m)^\s*$key\s*:\s*"([^"]*)"\s*$""").find(yaml)
    if (quoted != null) return quoted.groupValues[1].trim()
    val bare = Regex("""(?m)^\s*$key\s*:\s*(\S+)\s*$""").find(yaml)
    return bare?.groupValues?.get(1)?.trim()
}

// countryCodeToFlag turns a two-letter ISO code ("RU") into its flag emoji
// (regional-indicator pair, "🇷🇺") - rendered as a real flag on
// modern Android. Returns "" for anything that isn't two A-Z letters. This
// replaces the old flagcdn.com image fetch, removing that third-party call.
fun countryCodeToFlag(code: String): String {
    if (code.length != 2) return ""
    val cc = code.uppercase()
    if (!cc.all { it in 'A'..'Z' }) return ""
    val a = Character.toChars(0x1F1E6 + (cc[0] - 'A'))
    val b = Character.toChars(0x1F1E6 + (cc[1] - 'A'))
    return String(a) + String(b)
}

/**
 * Calls the Go core's Ping (one real disguised handshake, no tunnel built) on the IO
 * dispatcher and parses its {"ip":...,"latency_ms":...} JSON. Returns null on any failure
 * (unreachable server, bad config, timeout) - the caller treats that as "no data yet"
 * rather than a hard error, since this runs on a background timer.
 */
suspend fun fetchPing(yaml: String): Pair<String, Long>? = withContext(Dispatchers.IO) {
    runCatching {
        val json = JSONObject(Mobile.ping(yaml))
        val ip = json.optString("ip").takeIf { it.isNotBlank() } ?: return@withContext null
        val latency = json.optLong("latency_ms", -1L).takeIf { it >= 0 } ?: return@withContext null
        ip to latency
    }.getOrNull()
}

/**
 * Resolves which country a server IP sits in, returning (name, ISO code) or null
 * so the caller can retry.
 *
 * This asks a third-party geolocation service through the shared Go core
 * (internal/geoip), which necessarily tells that service the address of the
 * user's server. Done once per saved config and pinned - never on a timer, which
 * is what made the earlier version of this worth removing.
 */
suspend fun lookupCountry(ip: String): Pair<String, String>? = withContext(Dispatchers.IO) {
    runCatching {
        val json = JSONObject(Mobile.lookupCountry(ip))
        val code = json.optString("country_code").takeIf { it.isNotBlank() } ?: return@withContext null
        json.optString("country").takeIf { it.isNotBlank() }.orEmpty() to code
    }.getOrNull()
}

/**
 * One tile on the main screen: a saved config's domain/IP/live ping/location on the
 * left, a connect button on the right. The ping comes from [PingStore], filled by the
 * app-wide [PingPoller] - shared with the Маршрутизация page's config list, so both
 * always show the same measurement. Long-press anywhere on the tile (outside the
 * button itself) opens it for editing/deletion.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ConfigInfoCard(
    config: SavedConfig,
    status: ConnectionStatus,
    onToggle: () -> Unit,
    onLongPress: () -> Unit,
) {
    val domain = parseYamlField(config.yaml, "domain") ?: ""
    val server = parseYamlField(config.yaml, "server") ?: ""
    val info = PingStore[config.id]

    val cardShape = RoundedCornerShape(18.dp)
    val isConnected = status == ConnectionStatus.CONNECTED
    val connectedGradient = Brush.linearGradient(colors = BrandGradient)

    Tile(
        color = Surface,
        shape = cardShape,
        borderBrush = if (isConnected) connectedGradient else Brush.linearGradient(listOf(SurfaceOutline.copy(alpha = 0.6f), SurfaceOutline.copy(alpha = 0.6f))),
        modifier = Modifier
            .fillMaxWidth()
            .combinedClickable(onClick = {}, onLongClick = onLongPress),
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = domain.ifBlank { server.ifBlank { "—" } },
                    color = TextPrimary,
                    fontSize = 16.sp,
                    fontWeight = FontWeight.SemiBold,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Spacer(modifier = Modifier.height(2.dp))
                Text(
                    text = info?.ip ?: config.ip ?: server,
                    color = TextSecondary,
                    fontSize = 13.sp,
                    fontFamily = FontFamily.Monospace,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Spacer(modifier = Modifier.height(6.dp))
                Text(
                    text = info?.latencyMs?.let { "${I18n.t("ping")}: $it ${I18n.t("ms")}" } ?: "${I18n.t("ping")}: —",
                    color = TextSecondary,
                    fontSize = 13.sp,
                )
                val countryLabel = config.country ?: config.countryCode
                if (!countryLabel.isNullOrBlank()) {
                    Spacer(modifier = Modifier.height(4.dp))
                    val flag = config.countryCode?.let { countryCodeToFlag(it) } ?: ""
                    Text(
                        text = if (flag.isNotEmpty()) "$flag $countryLabel" else countryLabel,
                        color = TextSecondary,
                        fontSize = 13.sp,
                    )
                }
            }
            Spacer(modifier = Modifier.width(8.dp))
            ConnectSwitch(status = status, onClick = onToggle)
        }
    }
}

/**
 * Classic on/off switch for the full-tunnel VPN connection - checked while connected
 * or actively connecting (disabled mid-connect to prevent double-taps); [onClick] is
 * invoked on any tap regardless of the new value Switch itself would compute, since
 * the caller (MainActivity's toggleConnection) already decides connect-vs-disconnect
 * from the current [status] itself.
 */
@Composable
private fun ConnectSwitch(status: ConnectionStatus, onClick: () -> Unit) {
    val checked = status == ConnectionStatus.CONNECTED || status == ConnectionStatus.CONNECTING
    GradientSwitch(
        checked = checked,
        onCheckedChange = { onClick() },
        enabled = status != ConnectionStatus.CONNECTING,
    )
}
