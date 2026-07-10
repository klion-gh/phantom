package com.phantom.vpn

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
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

    LaunchedEffect(resource.url, pingEnabled) {
        if (!pingEnabled) return@LaunchedEffect
        while (isActive) {
            result = checkResource(resource.url)
            delay(8000)
        }
    }

    Card(
        colors = CardDefaults.cardColors(containerColor = BgSurface),
        shape = RoundedCornerShape(16.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Box(modifier = Modifier.fillMaxWidth().padding(14.dp)) {
            Column(modifier = Modifier.padding(end = 24.dp)) {
                Text(
                    text = resource.name,
                    color = TextPrimary,
                    fontSize = 15.sp,
                    fontWeight = FontWeight.SemiBold,
                )
                val statusText = when {
                    result == null -> "Проверка..."
                    result?.reachable == true -> "Доступен, ${result?.latencyMs} мс"
                    else -> "Недоступен"
                }
                val statusColor = when {
                    result == null -> TextSecondary
                    result?.reachable == true -> StatusConnected
                    else -> StatusError
                }
                Text(text = statusText, color = statusColor, fontSize = 13.sp)
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
