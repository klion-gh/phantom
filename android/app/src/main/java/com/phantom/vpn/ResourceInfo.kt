package com.phantom.vpn

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.IconButton
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
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import java.net.HttpURLConnection
import java.net.URL

/** Result of one reachability check: any real HTTP response (even an error status)
 * counts as reachable - only a network-level failure (timeout, refused, DNS, TLS) means
 * unreachable, mirroring the Windows app's fetch()-based check. */
data class ResourceCheckResult(val reachable: Boolean, val latencyMs: Long?)

private val faviconBitmapCache = mutableMapOf<String, ImageBitmap?>()

/**
 * Fetches a small favicon for a resource's URL, cached in memory by domain. Goes
 * through Google's favicon service rather than pulling /favicon.ico directly off the
 * site - that path is unreliable (SVG-only favicons BitmapFactory can't decode, missing
 * entirely, served from a non-standard location), while this endpoint normalizes
 * whatever icon a site actually has into a plain decodable bitmap regardless.
 */
suspend fun fetchFaviconBitmap(url: String): ImageBitmap? = withContext(Dispatchers.IO) {
    val domain = runCatching { URL(url).host }.getOrNull()?.takeIf { it.isNotBlank() }
        ?: return@withContext null
    faviconBitmapCache[domain]?.let { return@withContext it }
    val bitmap = runCatching {
        val conn = URL("https://www.google.com/s2/favicons?domain=$domain&sz=64").openConnection() as HttpURLConnection
        conn.connectTimeout = 4000
        conn.readTimeout = 4000
        conn.inputStream.use { BitmapFactory.decodeStream(it) }
    }.getOrNull()
    val imageBitmap = bitmap?.asImageBitmap()
    faviconBitmapCache[domain] = imageBitmap
    imageBitmap
}

/**
 * Checks one resource URL from this app's own process - which goes through Android's
 * normal networking stack, and therefore through the VpnService tunnel once it's up
 * (the tunnel routes all of the device's traffic, including this app's own, unless a
 * connection is explicitly protect()-ed the way the control connection to the Phantom
 * server is). A blocked site starting to respond once connected is the whole point.
 */
suspend fun checkResource(url: String): ResourceCheckResult = withContext(Dispatchers.IO) {
    val start = System.currentTimeMillis()
    runCatching {
        val conn = URL(url).openConnection() as HttpURLConnection
        conn.connectTimeout = 5000
        conn.readTimeout = 5000
        conn.requestMethod = "HEAD"
        conn.instanceFollowRedirects = true
        conn.connect()
        conn.responseCode // forces the actual request; any status code means reachable
        conn.disconnect()
        ResourceCheckResult(reachable = true, latencyMs = System.currentTimeMillis() - start)
    }.getOrElse { ResourceCheckResult(reachable = false, latencyMs = null) }
}

/**
 * One resource-reachability tile. [pingEnabled] gates the polling loop - false while the
 * resources page isn't the one currently showing, or while the app itself isn't in the
 * foreground (see MainActivity's lifecycle/pager wiring) - so this only ever checks
 * while the user is actually looking at it.
 */
@Composable
fun ResourceCard(
    resource: PingResource,
    pingEnabled: Boolean,
    onDelete: () -> Unit,
) {
    var result by remember(resource.id) { mutableStateOf<ResourceCheckResult?>(null) }
    var favicon by remember(resource.id) { mutableStateOf<ImageBitmap?>(null) }

    LaunchedEffect(resource.url, pingEnabled) {
        if (!pingEnabled) return@LaunchedEffect
        while (isActive) {
            result = checkResource(resource.url)
            delay(8000)
        }
    }

    LaunchedEffect(resource.url) {
        favicon = fetchFaviconBitmap(resource.url)
    }

    Card(
        colors = CardDefaults.cardColors(containerColor = BgSurface),
        shape = RoundedCornerShape(16.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Box(modifier = Modifier.fillMaxWidth().padding(14.dp)) {
            Row(
                modifier = Modifier.padding(end = 24.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                favicon?.let {
                    Image(
                        bitmap = it,
                        contentDescription = null,
                        modifier = Modifier.size(20.dp).clip(RoundedCornerShape(4.dp)),
                    )
                    Spacer(modifier = Modifier.width(8.dp))
                }
                Column {
                    Text(
                        text = resource.name,
                        color = TextPrimary,
                        fontSize = 15.sp,
                        fontWeight = FontWeight.SemiBold,
                    )
                    val statusText = when {
                        result == null -> I18n.t("checking")
                        result?.reachable == true -> "${I18n.t("available")}, ${result?.latencyMs} ${I18n.t("ms")}"
                        else -> I18n.t("unavailable")
                    }
                    val statusColor = when {
                        result == null -> TextSecondary
                        result?.reachable == true -> StatusConnected
                        else -> StatusError
                    }
                    Text(text = statusText, color = statusColor, fontSize = 13.sp)
                }
            }
            IconButton(
                onClick = onDelete,
                modifier = Modifier.align(Alignment.TopEnd).size(28.dp),
            ) {
                Text("×", color = TextSecondary, fontSize = 18.sp)
            }
        }
    }
}
