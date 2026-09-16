package com.phantom.vpn

import android.graphics.BitmapFactory
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.graphics.ColorFilter
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONArray
import java.net.HttpURLConnection
import java.net.URL

/** One service from the built-in catalogue (internal/routing's PopularResources). */
data class PopularResource(val name: String, val icon: String, val domains: List<String>)

/**
 * Reads the catalogue once. It lives on the Go side so the Windows client
 * shows exactly the same list - see internal/routing/catalog.go.
 */
private fun loadCatalogue(): List<PopularResource> = runCatching {
    val arr = JSONArray(Mobile.popularResourcesJSON())
    (0 until arr.length()).map { i ->
        val obj = arr.getJSONObject(i)
        val domainsArr = obj.getJSONArray("domains")
        PopularResource(
            name = obj.getString("name"),
            icon = obj.getString("icon"),
            domains = (0 until domainsArr.length()).map { domainsArr.getString(it) },
        )
    }
}.getOrDefault(emptyList())

/**
 * The "Популярные ресурсы" picker: a grid of services, tap to add or remove
 * one from the smart-VPN list.
 *
 * Selection is per *service*, not per domain - tapping Instagram adds
 * everything Instagram needs (its CDN included), because a half-added service
 * that loads its page but not its images is the failure mode this screen
 * exists to prevent.
 */
@Composable
fun PopularResourcesScreen(
    onBack: () -> Unit,
    onToggle: (PopularResource) -> Unit,
) {
    val catalogue = remember { loadCatalogue() }

    Column(modifier = Modifier.fillMaxSize()) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.padding(start = 20.dp, top = 20.dp, end = 20.dp),
        ) {
            IconButton(onClick = onBack) {
                Image(
                    painter = painterResource(R.drawable.ic_back_arrow),
                    contentDescription = null,
                    colorFilter = ColorFilter.tint(TextPrimary),
                    modifier = Modifier.size(20.dp),
                )
            }
            Text(
                I18n.t("popular_resources"),
                color = TextPrimary,
                fontSize = 20.sp,
                fontWeight = FontWeight.SemiBold,
            )
        }

        LazyVerticalGrid(
            columns = GridCells.Adaptive(minSize = 104.dp),
            contentPadding = PaddingValues(start = 20.dp, top = 14.dp, end = 20.dp, bottom = 32.dp),
            horizontalArrangement = Arrangement.spacedBy(10.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            items(catalogue, key = { it.name }) { resource ->
                ResourceTile(
                    resource = resource,
                    selected = RoutingStore.hasAll(resource.domains),
                    onClick = { onToggle(resource) },
                )
            }
        }
    }
}

@Composable
private fun ResourceTile(resource: PopularResource, selected: Boolean, onClick: () -> Unit) {
    val borderWidth by animateDpAsState(if (selected) 2.dp else 1.dp, tween(200), label = "popularBorderWidth")
    val shape = RoundedCornerShape(18.dp)
    // The same gradient outline a connected config tile uses (see
    // ConfigInfoCard) - "this one is on" reads identically everywhere in the
    // app rather than being a different visual language per screen.
    val borderBrush = if (selected) {
        Brush.linearGradient(BrandGradient)
    } else {
        Brush.linearGradient(listOf(SurfaceOutline, SurfaceOutline))
    }

    var logo by remember(resource.icon) { mutableStateOf<ImageBitmap?>(null) }
    LaunchedEffect(resource.icon) {
        logo = fetchLogo(resource.icon)
    }

    Column(
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
        modifier = Modifier
            .aspectRatio(1f)
            .clip(shape)
            .background(Surface)
            .border(borderWidth, borderBrush, shape)
            .clickable(onClick = onClick)
            .padding(10.dp),
    ) {
        val bitmap = logo
        if (bitmap != null) {
            Image(
                bitmap = bitmap,
                contentDescription = null,
                modifier = Modifier.size(36.dp).clip(RoundedCornerShape(8.dp)),
            )
        } else {
            // Placeholder while the logo loads (or if it never does) - the
            // service's initial, so the tile is identifiable either way rather
            // than being a blank square.
            Box(
                modifier = Modifier
                    .size(36.dp)
                    .clip(RoundedCornerShape(8.dp))
                    .background(SurfaceHigh),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    resource.name.take(1).uppercase(),
                    color = TextSecondary,
                    fontSize = 16.sp,
                    fontWeight = FontWeight.Bold,
                )
            }
        }
        Spacer(Modifier.height(8.dp))
        Text(
            resource.name,
            color = if (selected) TextPrimary else TextSecondary,
            fontSize = 12.sp,
            fontWeight = FontWeight.Medium,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
            textAlign = TextAlign.Center,
            lineHeight = 14.sp,
        )
    }
}

/**
 * Same favicon source the resource-reachability tiles already use (see
 * ResourceInfo.fetchFaviconBitmap) - no bundled brand assets, and one cache
 * shared across the app for the domains that appear in both places.
 */
private val logoCache = mutableMapOf<String, ImageBitmap?>()

private suspend fun fetchLogo(domain: String): ImageBitmap? = withContext(Dispatchers.IO) {
    synchronized(logoCache) { if (logoCache.containsKey(domain)) return@withContext logoCache[domain] }
    val bitmap = runCatching {
        val conn = URL("https://www.google.com/s2/favicons?domain=$domain&sz=64").openConnection() as HttpURLConnection
        conn.connectTimeout = 4000
        conn.readTimeout = 4000
        conn.inputStream.use { BitmapFactory.decodeStream(it) }
    }.getOrNull()?.asImageBitmap()
    synchronized(logoCache) { logoCache[domain] = bitmap }
    bitmap
}
