package com.phantom.vpn

import androidx.compose.foundation.ExperimentalFoundationApi
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
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
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
 * One tile on the main screen: a saved config's domain/IP/live ping/location on the
 * left, a connect button on the right. Owns its own ping-polling loop (keyed on the
 * config's own yaml/id) so each tile refreshes independently of the others. Long-press
 * anywhere on the tile (outside the button itself) opens it for editing/deletion.
 *
 * [pingEnabled] should be true only while this tile's page is the one currently visible
 * in the pager *and* the app itself is in the foreground - see MainActivity.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ConfigInfoCard(
    config: SavedConfig,
    status: ConnectionStatus,
    pingEnabled: Boolean,
    proxyRunning: Boolean,
    proxyPort: Int?,
    onToggle: () -> Unit,
    onToggleProxy: (requestedPort: String) -> Unit,
    onLongPress: () -> Unit,
) {
    var pingInfo by remember(config.id) { mutableStateOf<PingInfo?>(null) }

    LaunchedEffectPing(config.yaml, pingEnabled) { result ->
        pingInfo = if (result != null) {
            val (ip, latency) = result
            PingInfo(ip, latency)
        } else {
            pingInfo?.copy(latencyMs = null)
        }
    }

    val domain = parseYamlField(config.yaml, "domain") ?: ""
    val server = parseYamlField(config.yaml, "server") ?: ""
    val info = pingInfo

    // The port field mirrors whatever's actually running once it starts (in case it
    // had to fall back... it doesn't anymore - see ProxyManager - but this also covers
    // the field simply being blank and the OS picking one), and is otherwise just
    // freely editable by the user while off.
    var portText by remember(config.id) { mutableStateOf(config.proxyPort?.toString() ?: "") }
    LaunchedEffect(proxyRunning, proxyPort) {
        if (proxyRunning && proxyPort != null) {
            portText = proxyPort.toString()
        }
    }

    val cardShape = RoundedCornerShape(20.dp)
    val isConnected = status == ConnectionStatus.CONNECTED
    val connectedGradient = Brush.linearGradient(
        colors = listOf(Color(0xFFA78BFA), Color(0xFFF472B6), Color(0xFF7DD3FC))
    )

    Card(
        colors = CardDefaults.cardColors(containerColor = BgSurface),
        shape = cardShape,
        modifier = Modifier
            .fillMaxWidth()
            .then(
                if (isConnected) Modifier.border(2.dp, connectedGradient, cardShape) else Modifier
            )
            .combinedClickable(onClick = {}, onLongClick = onLongPress),
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 20.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = domain.ifBlank { server.ifBlank { "—" } },
                    color = TextPrimary,
                    fontSize = 17.sp,
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
            // One column: the connect toggle on top, the independent-proxy controls
            // directly under it - two separate concerns (whole-device tunnel vs. this
            // one tile's own SOCKS5 proxy), but visually grouped since they're both
            // "controls for this config", not tied to each other's state.
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                ConnectSwitch(status = status, onClick = onToggle)
                Spacer(modifier = Modifier.height(6.dp))
                ProxyBlock(
                    running = proxyRunning,
                    portText = portText,
                    onPortTextChange = { portText = it },
                    onToggleClick = { onToggleProxy(portText) },
                )
            }
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
    androidx.compose.material3.Switch(
        checked = checked,
        onCheckedChange = { onClick() },
        enabled = status != ConnectionStatus.CONNECTING,
        colors = androidx.compose.material3.SwitchDefaults.colors(
            checkedThumbColor = AccentLavenderBright,
            checkedTrackColor = AccentPurpleDeep,
            checkedBorderColor = Color.Transparent,
            uncheckedThumbColor = TextSecondary,
            uncheckedTrackColor = BgSurfaceAlt,
            uncheckedBorderColor = TextSecondary.copy(alpha = 0.4f),
        ),
    )
}

/**
 * Independent per-config SOCKS5 proxy toggle - grey border when off, the same
 * gradient border the tile itself uses for "connected" when on - plus, right below
 * it, the port it's bound to. The port field is only actually editable while off
 * (see the disabled-look styling below); [onToggleClick] is invoked with whatever's
 * currently in it, and the caller (MainActivity's toggleProxy) decides what to do
 * with that - deliberately unrelated to [status]/the connect button, see ProxyManager.
 */
@Composable
private fun ProxyBlock(
    running: Boolean,
    portText: String,
    onPortTextChange: (String) -> Unit,
    onToggleClick: () -> Unit,
) {
    val shape = RoundedCornerShape(8.dp)
    val gradient = Brush.linearGradient(
        colors = listOf(Color(0xFFA78BFA), Color(0xFFF472B6), Color(0xFF7DD3FC))
    )
    Column(horizontalAlignment = Alignment.CenterHorizontally) {
        Box(
            modifier = Modifier
                .clip(shape)
                .then(
                    if (running) Modifier.border(2.dp, gradient, shape)
                    else Modifier.border(2.dp, TextSecondary.copy(alpha = 0.35f), shape)
                )
                .clickable(onClick = onToggleClick)
                .padding(horizontal = 8.dp, vertical = 5.dp),
        ) {
            Text(
                text = "PROXY",
                color = if (running) TextPrimary else TextSecondary,
                fontSize = 9.sp,
                fontWeight = FontWeight.Bold,
            )
        }
        Spacer(modifier = Modifier.height(4.dp))
        Box(contentAlignment = Alignment.Center) {
            if (portText.isEmpty()) {
                Text(I18n.t("port_ph"), color = TextSecondary.copy(alpha = 0.6f), fontSize = 10.sp)
            }
            BasicTextField(
                value = portText,
                onValueChange = { new -> if (new.length <= 5 && new.all(Char::isDigit)) onPortTextChange(new) },
                enabled = !running,
                singleLine = true,
                textStyle = TextStyle(
                    color = if (running) TextSecondary else TextPrimary,
                    fontSize = 10.sp,
                    textAlign = TextAlign.Center,
                ),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                modifier = Modifier
                    .width(46.dp)
                    .padding(bottom = 2.dp)
                    .border(
                        width = 1.dp,
                        color = if (running) Color.Transparent else TextSecondary.copy(alpha = 0.35f),
                        shape = RoundedCornerShape(4.dp),
                    )
                    .padding(vertical = 3.dp),
            )
        }
    }
}

/**
 * Runs fetchPing on a repeating timer for as long as the calling composable is alive
 * AND [pingEnabled] is true, restarting whenever [yaml] or [pingEnabled] changes -
 * [pingEnabled] going false immediately cancels the loop rather than just skipping a
 * cycle (see MainActivity's page/foreground wiring for when that happens), and going
 * true again resumes with an immediate check rather than waiting out a full interval.
 * Deliberately does *not* reset to null when only [pingEnabled] flips (a separate
 * effect below handles the real "yaml changed" reset) - otherwise every pause/resume
 * (minimize, switch pager page) would flash the tile to "—" instead of keeping the
 * last-known value on screen, same as the Windows app's visibility-based pause.
 */
@Composable
private fun LaunchedEffectPing(yaml: String, pingEnabled: Boolean, onResult: suspend (Pair<String, Long>?) -> Unit) {
    androidx.compose.runtime.LaunchedEffect(yaml) {
        onResult(null)
    }
    androidx.compose.runtime.LaunchedEffect(yaml, pingEnabled) {
        if (!pingEnabled) return@LaunchedEffect
        while (isActive) {
            onResult(fetchPing(yaml))
            delay(6000)
        }
    }
}
