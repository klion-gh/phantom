package com.phantom.vpn

import android.graphics.BitmapFactory
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.border
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
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
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/** Snapshot of what a config tile shows about its server: live latency + geo. */
data class PingInfo(
    val ip: String? = null,
    val latencyMs: Long? = null,
    val country: String? = null,
    val countryCode: String? = null,
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

private val flagBitmapCache = mutableMapOf<String, ImageBitmap?>()

/**
 * Fetches a small flag image for a two-letter ISO country code, cached in memory.
 * Uses a real image rather than the Unicode flag emoji (regional indicator pair) since
 * that renders as bare letters on some devices/system images that lack flag glyphs in
 * their emoji font - the same gap that showed up on Windows for this exact feature.
 */
suspend fun fetchFlagBitmap(countryCode: String): ImageBitmap? = withContext(Dispatchers.IO) {
    flagBitmapCache[countryCode]?.let { return@withContext it }
    val bitmap = runCatching {
        val conn = URL("https://flagcdn.com/48x36/${countryCode.lowercase()}.png").openConnection() as HttpURLConnection
        conn.connectTimeout = 4000
        conn.readTimeout = 4000
        conn.inputStream.use { BitmapFactory.decodeStream(it) }
    }.getOrNull()
    val imageBitmap = bitmap?.asImageBitmap()
    flagBitmapCache[countryCode] = imageBitmap
    imageBitmap
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
 * Looks up the country for the server's IP via a public geo-IP API - this is the one
 * place in the app that calls a third party, purely for the cosmetic country/flag label.
 * Best-effort: any failure (offline, rate limit) just leaves the location blank. Uses
 * ipwho.is rather than ipapi.co - the latter's free tier rate-limited itself into 429s
 * during development from repeated polling across both this app and the Windows one.
 */
suspend fun fetchGeo(ip: String): Pair<String, String>? = withContext(Dispatchers.IO) {
    runCatching {
        val conn = URL("https://ipwho.is/$ip").openConnection() as HttpURLConnection
        conn.connectTimeout = 4000
        conn.readTimeout = 4000
        conn.requestMethod = "GET"
        val body = conn.inputStream.bufferedReader().use { it.readText() }
        val json = JSONObject(body)
        if (!json.optBoolean("success", true)) return@withContext null
        val name = json.optString("country").takeIf { it.isNotBlank() }
        val code = json.optString("country_code").takeIf { it.isNotBlank() }
        if (name != null && code != null) name to code else null
    }.getOrNull()
}

/**
 * One tile on the main screen: a saved config's domain/IP/live ping/location on the
 * left, a connect button on the right. Owns its own ping-polling loop (keyed on the
 * config's own yaml/id) so each tile refreshes independently of the others. Long-press
 * anywhere on the tile (outside the button itself) opens it for editing/deletion.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ConfigInfoCard(
    config: SavedConfig,
    status: ConnectionStatus,
    onToggle: () -> Unit,
    onLongPress: () -> Unit,
) {
    var pingInfo by remember(config.id) { mutableStateOf<PingInfo?>(null) }
    // Retrying a failed geo lookup on every 6s ping cycle is what rate-limited ipapi.co
    // into 429s during development - only retry a failure every couple of minutes, but
    // always look up immediately when the resolved IP actually changes.
    var lastGeoAttemptAt by remember(config.id) { mutableStateOf(0L) }

    LaunchedEffectPing(config.yaml) { result ->
        val previous = pingInfo
        pingInfo = if (result != null) {
            val (ip, latency) = result
            var country = previous?.country
            var countryCode = previous?.countryCode
            val ipChanged = ip != previous?.ip
            val now = System.currentTimeMillis()
            if (ipChanged || (countryCode == null && now - lastGeoAttemptAt > 120_000)) {
                lastGeoAttemptAt = now
                fetchGeo(ip)?.let { (name, code) -> country = name; countryCode = code }
            }
            PingInfo(ip, latency, country, countryCode)
        } else {
            previous?.copy(latencyMs = null)
        }
    }

    val domain = parseYamlField(config.yaml, "domain") ?: ""
    val server = parseYamlField(config.yaml, "server") ?: ""
    val info = pingInfo

    var flagBitmap by remember(config.id) { mutableStateOf<ImageBitmap?>(null) }
    LaunchedEffect(info?.countryCode) {
        flagBitmap = info?.countryCode?.takeIf { it.isNotBlank() }?.let { fetchFlagBitmap(it) }
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
                )
                Spacer(modifier = Modifier.height(2.dp))
                Text(
                    text = info?.ip ?: server,
                    color = TextSecondary,
                    fontSize = 13.sp,
                    fontFamily = FontFamily.Monospace,
                )
                Spacer(modifier = Modifier.height(6.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        text = info?.latencyMs?.let { "Пинг: $it мс" } ?: "Пинг: —",
                        color = TextSecondary,
                        fontSize = 13.sp,
                    )
                    val code = info?.countryCode
                    if (!code.isNullOrBlank()) {
                        Spacer(modifier = Modifier.width(12.dp))
                        flagBitmap?.let { bitmap ->
                            Image(
                                bitmap = bitmap,
                                contentDescription = null,
                                modifier = Modifier
                                    .width(20.dp)
                                    .height(15.dp)
                                    .clip(RoundedCornerShape(2.dp)),
                            )
                            Spacer(modifier = Modifier.width(6.dp))
                        }
                        Text(
                            text = info?.country ?: "",
                            color = TextSecondary,
                            fontSize = 13.sp,
                        )
                    }
                }
            }
            Spacer(modifier = Modifier.width(12.dp))
            ConnectButton(status = status, onClick = onToggle, size = 68.dp)
        }
    }
}

/**
 * Runs fetchPing on a repeating timer for as long as the calling composable is alive,
 * restarting whenever [yaml] changes. Factored out of ConfigInfoCard purely to keep the
 * polling loop's plumbing (delay/isActive/withContext) out of the layout code above.
 */
@Composable
private fun LaunchedEffectPing(yaml: String, onResult: suspend (Pair<String, Long>?) -> Unit) {
    androidx.compose.runtime.LaunchedEffect(yaml) {
        onResult(null)
        while (isActive) {
            onResult(fetchPing(yaml))
            delay(6000)
        }
    }
}
