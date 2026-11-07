package com.phantom.vpn

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONObject

/** Snapshot of what a config tile shows about its server: the last check's latency only -
 * country/flag are resolved once and cached on the SavedConfig itself. [failed] means
 * the last check got no answer, as opposed to no check having been made yet. */
data class PingInfo(
    val ip: String? = null,
    val latencyMs: Long? = null,
    val failed: Boolean = false,
)

/**
 * The colour a ping result is shown in, or null while there is none (not
 * checked yet, or a check running). What it measures is a full handshake -
 * TCP, TLS and the auth exchange - so a healthy server reads 80-250 ms.
 */
fun pingTone(info: PingInfo?, checking: Boolean): Color? {
    if (checking) return null
    if (info?.failed == true) return Danger
    val ms = info?.latencyMs ?: return null
    return when {
        ms <= PING_GOOD_MS -> Success
        ms <= PING_SLOW_MS -> Warning
        else -> Danger
    }
}

const val PING_GOOD_MS = 250L
const val PING_SLOW_MS = 600L

/** The label every ping readout shares - tile and routing list alike. */
fun pingLabel(info: PingInfo?, checking: Boolean): String = when {
    checking -> "${I18n.t("ping")}: …"
    info?.latencyMs != null -> "${I18n.t("ping")}: ${info.latencyMs} ${I18n.t("ms")}"
    info?.failed == true -> "${I18n.t("ping")}: ${I18n.t("no_answer")}"
    else -> "${I18n.t("ping")}: —"
}

/** The ping pill a config or relay tile shows: tap to measure, neutral until
 * a result is in, then tinted by [tone]. */
@Composable
fun PingPill(label: String, tone: Color?, checking: Boolean, onClick: () -> Unit) {
    val pillShape = RoundedCornerShape(50)
    Text(
        text = label,
        color = tone ?: if (checking) TextMuted else TextSecondary,
        fontSize = 12.5.sp,
        fontWeight = FontWeight.Medium,
        modifier = Modifier
            .clip(pillShape)
            .background((tone ?: SurfaceHigh).copy(alpha = if (tone != null) 0.16f else 1f))
            .border(1.dp, (tone ?: SurfaceOutline).copy(alpha = if (tone != null) 0.45f else 1f), pillShape)
            .clickable(
                enabled = !checking,
                onClickLabel = I18n.t("check_ping"),
                role = Role.Button,
                onClick = onClick,
            )
            .padding(horizontal = 10.dp, vertical = 3.dp),
    )
}

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
 * (unreachable server, bad config, timeout).
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
suspend fun lookupPlace(ip: String): Place? = withContext(Dispatchers.IO) {
    runCatching {
        val json = JSONObject(Mobile.lookupCountry(ip))
        val code = json.optString("country_code").takeIf { it.isNotBlank() } ?: return@withContext null
        Place(
            country = json.optString("country").takeIf { it.isNotBlank() }.orEmpty(),
            countryCode = code,
            city = json.optString("city").takeIf { it.isNotBlank() },
            provider = json.optString("provider").takeIf { it.isNotBlank() },
        )
    }.getOrNull()
}

/** Where a server is, as a tile shows it: country, then city and provider. */
data class Place(val country: String, val countryCode: String, val city: String?, val provider: String?)

/**
 * The top of a config or relay tile: the name with its flag and "City,
 * Country" after it, then the IP, then the hosting provider.
 */
@Composable
fun TileHeading(
    title: String,
    countryCode: String?,
    country: String?,
    ip: String,
    city: String?,
    provider: String?,
) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(
            text = title,
            color = TextPrimary,
            fontSize = 16.sp,
            fontWeight = FontWeight.SemiBold,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f, fill = false),
        )
        val label = listOfNotNull(city, country ?: countryCode).filter { it.isNotBlank() }.joinToString(", ")
        if (label.isNotBlank()) {
            val flag = countryCode?.let { countryCodeToFlag(it) }.orEmpty()
            Spacer(modifier = Modifier.width(7.dp))
            Text(
                text = if (flag.isNotEmpty()) "$flag $label" else label,
                color = TextMuted,
                fontSize = 12.5.sp,
                maxLines = 1,
            )
        }
    }
    Spacer(modifier = Modifier.height(2.dp))
    Text(
        text = ip,
        color = TextSecondary,
        fontSize = 13.sp,
        fontFamily = FontFamily.Monospace,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
    )
    // The hosting provider gets a line of its own: after the IP it rarely fit.
    if (!provider.isNullOrBlank()) {
        Text(
            text = provider,
            color = TextMuted,
            fontSize = 12.5.sp,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

/**
 * One tile on the main screen: a saved config's domain/IP/ping/location on the left,
 * a gear (edit/delete) and the connect switch on the right - the same layout as the
 * Windows tile. The ping label is a button: tapping it takes one ping (see
 * [PingStore]), shared with the Маршрутизация page's config list so both show the
 * same measurement. Long-press and drag is the caller's (reordering - see
 * [modifier]).
 */
@Composable
fun ConfigInfoCard(
    config: SavedConfig,
    status: ConnectionStatus,
    onToggle: () -> Unit,
    onEdit: () -> Unit,
    modifier: Modifier = Modifier,
    dragging: Boolean = false,
) {
    val domain = parseYamlField(config.yaml, "domain") ?: ""
    val server = parseYamlField(config.yaml, "server") ?: ""
    val info = PingStore[config.id]
    val checking = PingStore.isChecking(config.id)
    val scope = rememberCoroutineScope()

    val cardShape = RoundedCornerShape(18.dp)
    val isConnected = status == ConnectionStatus.CONNECTED
    val connectedGradient = Brush.linearGradient(colors = BrandGradient)
    val idleBorder = if (dragging) Accent else SurfaceOutline.copy(alpha = 0.6f)

    Tile(
        color = Surface,
        shape = cardShape,
        borderBrush = if (isConnected) connectedGradient else Brush.linearGradient(listOf(idleBorder, idleBorder)),
        modifier = modifier.fillMaxWidth(),
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(modifier = Modifier.weight(1f)) {
                TileHeading(
                    title = domain.ifBlank { server.ifBlank { "—" } },
                    countryCode = config.countryCode,
                    country = config.country,
                    ip = info?.ip ?: config.ip ?: server,
                    city = config.city,
                    provider = config.provider,
                )
                // The ping is a pill you tap - there is no automatic ping. It is
                // neutral until a result is in, then takes the result's colour
                // (pingTone): green, amber, or red for slow or no answer.
                Spacer(modifier = Modifier.height(6.dp))
                PingPill(
                    label = pingLabel(info, checking),
                    tone = pingTone(info, checking),
                    checking = checking,
                ) { scope.launch { PingStore.check(config) } }
            }
            Spacer(modifier = Modifier.width(4.dp))
            IconButton(onClick = onEdit) {
                Image(
                    painter = painterResource(R.drawable.ic_settings_gear),
                    contentDescription = I18n.t("edit"),
                    colorFilter = ColorFilter.tint(TextSecondary),
                    modifier = Modifier.size(20.dp),
                )
            }
            Spacer(modifier = Modifier.width(4.dp))
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
