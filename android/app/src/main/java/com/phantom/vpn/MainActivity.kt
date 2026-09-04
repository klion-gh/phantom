package com.phantom.vpn

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Description
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

private enum class Screen { MAIN, ADD_CONFIG, SETTINGS, LOG }

class MainActivity : ComponentActivity() {

    private var pendingConfig: SavedConfig? = null

    private val vpnPrepareLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        val config = pendingConfig
        pendingConfig = null
        if (result.resultCode == RESULT_OK && config != null) {
            startVpn(config)
        } else {
            VpnStateHolder.update(ConnectionStatus.ERROR, "VPN permission denied")
        }
    }

    // Android 13+ requires runtime consent to post any notification at all - without
    // this, the persistent connect/disconnect notification silently never appears.
    private val notificationPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { showPersistentNotification() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        FileLog.i("MainActivity.onCreate")

        // Without this, the system draws its default opaque status/nav bar
        // background instead of letting AnimatedBackground show through them -
        // visible as solid black strips top and bottom on gesture-nav phones,
        // since there's no bezel there to make it read as "chrome" instead of
        // "broken". WindowInsets.systemBars below (in setContent) keeps actual
        // content clear of the bars; only the background is meant to run
        // underneath them.
        enableEdgeToEdge()

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
            != PackageManager.PERMISSION_GRANTED
        ) {
            notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            showPersistentNotification()
        }

        setContent {
            PhantomTheme {
                Box(modifier = Modifier.fillMaxSize()) {
                    // Deliberately outside the systemBars padding below - the
                    // backdrop is meant to run edge-to-edge, under the status/nav
                    // bars, not stop short of them.
                    AnimatedBackground(modifier = Modifier.fillMaxSize())
                    PhantomApp(
                        onConnect = { config -> requestConnect(config) },
                        onDisconnect = { stopVpn() },
                        modifier = Modifier.windowInsetsPadding(WindowInsets.systemBars),
                    )
                }
            }
        }
    }

    private fun requestConnect(config: SavedConfig) {
        FileLog.i("requestConnect")
        val prepareIntent = VpnService.prepare(this)
        if (prepareIntent != null) {
            pendingConfig = config
            vpnPrepareLauncher.launch(prepareIntent)
        } else {
            startVpn(config)
        }
    }

    private fun startVpn(config: SavedConfig) {
        ConfigStore.saveLastActiveId(this, config.id)
        val intent = Intent(this, PhantomVpnService::class.java).apply {
            action = PhantomVpnService.ACTION_CONNECT
            putExtra(PhantomVpnService.EXTRA_CONFIG_ID, config.id)
            putExtra(PhantomVpnService.EXTRA_CONFIG_YAML, config.yaml)
        }
        startService(intent)
    }

    private fun stopVpn() {
        val intent = Intent(this, PhantomVpnService::class.java).apply {
            action = PhantomVpnService.ACTION_DISCONNECT
        }
        startService(intent)
    }

    // Posts the persistent connect/disconnect notification (a no-op if a connection is
    // already up - PhantomVpnService only touches state for actions it doesn't know yet).
    private fun showPersistentNotification() {
        startService(Intent(this, PhantomVpnService::class.java).apply {
            action = PhantomVpnService.ACTION_SHOW_STATUS
        })
    }
}

@Composable
private fun PhantomApp(
    onConnect: (SavedConfig) -> Unit,
    onDisconnect: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val context = LocalContext.current
    val coroutineScope = rememberCoroutineScope()
    var configs by remember { mutableStateOf(ConfigStore.loadAll(context)) }
    var resources by remember { mutableStateOf(ResourceStore.loadAll(context)) }
    var screen by remember { mutableStateOf(Screen.MAIN) }
    var editingId by remember { mutableStateOf<String?>(null) }
    var editingYaml by remember { mutableStateOf("") }
    val state by VpnStateHolder.state.collectAsState()

    // Whether the Activity itself is resumed (visible, interactive) right now - both
    // pages' ping loops must stop the instant this goes false, not just once Android
    // gets around to actually stopping the process. An activity-lifecycle concern, so
    // it's tracked once here rather than per-page.
    var appInForeground by remember { mutableStateOf(true) }
    val lifecycleOwner = LocalLifecycleOwner.current
    DisposableEffect(lifecycleOwner) {
        val observer = LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_RESUME -> appInForeground = true
                Lifecycle.Event.ON_PAUSE -> appInForeground = false
                else -> Unit
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    // Checked once on launch - unlike the Windows app, actually installing it is
    // never automatic even after the user asks for it (see downloadAndInstallUpdate),
    // so there's no equivalent of the exe's silent relaunch to guard against here.
    var updateInfo by remember { mutableStateOf<UpdateInfo?>(null) }
    var isUpdating by remember { mutableStateOf(false) }
    // 0-100 while the APK streams to disk, null the rest of the time (including
    // while the download is already-on-disk/install-permission branches of
    // downloadAndInstallUpdate, which never call onProgress) - see MainScreen's
    // progress bar under the logo.
    var updateProgress by remember { mutableStateOf<Int?>(null) }
    LaunchedEffect(Unit) {
        updateInfo = checkForUpdate(BuildConfig.VERSION_NAME)
    }

    // Independent per-config SOCKS5 proxy toggle state - see ProxyManager. Entirely
    // separate from state.activeConfigId/the full-tunnel VPN above. Maps id -> the
    // actual bound port. Collected from ProxyManager (not kept as this composable's
    // own copy) because the proxies outlive the Activity - see runningPorts's doc.
    val proxyRunningPorts by ProxyManager.runningPorts.collectAsState()

    fun applyUpdate() {
        val info = updateInfo ?: return
        if (isUpdating) return
        isUpdating = true
        updateProgress = 0
        coroutineScope.launch {
            val ok = downloadAndInstallUpdate(context, info) { pct -> updateProgress = pct }
            isUpdating = false
            updateProgress = null
            if (!ok) {
                Toast.makeText(context, I18n.t("download_failed"), Toast.LENGTH_LONG).show()
            }
        }
    }

    fun refreshConfigs() {
        configs = ConfigStore.loadAll(context)
    }

    fun refreshResources() {
        resources = ResourceStore.loadAll(context)
    }

    // Pokes the service to re-evaluate its own foreground state right after any
    // ProxyManager change - see PhantomVpnService.ACTION_PROXY_STATE_CHANGED and the
    // class doc on ProxyManager for why a proxy alone (no VPN connected) still needs
    // this: without it, a backgrounded proxy becomes unreliable/high-latency once
    // Android's background network throttling kicks in, then eventually stops working.
    fun notifyProxyStateChanged() {
        context.startService(Intent(context, PhantomVpnService::class.java).apply {
            action = PhantomVpnService.ACTION_PROXY_STATE_CHANGED
        })
    }

    // requestedPortText comes straight from the tile's own port field (empty = "any
    // free port"); invalid/out-of-range input is rejected client-side with a Toast
    // before ever calling into the Go core, same as the Windows app's port field.
    fun toggleProxy(config: SavedConfig, requestedPortText: String) {
        if (ProxyManager.isRunning(config.id)) {
            ProxyManager.stop(config.id)
            notifyProxyStateChanged()
            return
        }

        val trimmed = requestedPortText.trim()
        var requestedPort = 0
        if (trimmed.isNotEmpty()) {
            requestedPort = trimmed.toIntOrNull() ?: -1
            if (requestedPort !in 1..65535) {
                Toast.makeText(context, I18n.t("bad_port", trimmed), Toast.LENGTH_SHORT).show()
                return
            }
        }

        coroutineScope.launch {
            ProxyManager.start(config.id, config.yaml, requestedPort, PhantomVpnService.lazyProtector)
                .onSuccess { port ->
                    ConfigStore.setProxyPort(context, config.id, port)
                    refreshConfigs()
                    notifyProxyStateChanged()
                }
                .onFailure { e ->
                    Toast.makeText(
                        context,
                        I18n.t("proxy_failed", trimmed.ifEmpty { I18n.t("proxy_any") }, e.message ?: ""),
                        Toast.LENGTH_LONG,
                    ).show()
                }
        }
    }

    // Ids with an IP-resolution retry loop already running, so a config saved twice
    // (or saved and then backfilled on the next launch) doesn't accumulate loops.
    // remembered rather than a plain local: a recomposition would otherwise hand out
    // a fresh empty set and the guard would stop guarding.
    val geoJobs = remember { java.util.Collections.synchronizedSet(mutableSetOf<String>()) }

    // The operator's own country/country_code from the yaml, if present. No
    // network, so it cannot fail and is applied immediately - and it wins over
    // anything the lookup below would return. Reports whether the config spelled
    // it out.
    fun applyCountryFromYaml(id: String, yaml: String): Boolean {
        val country = parseYamlField(yaml, "country")
        val code = parseYamlField(yaml, "country_code")
        if (country.isNullOrBlank() && code.isNullOrBlank()) return false
        ConfigStore.setCountry(context, id, country, code)
        return true
    }

    // Resolves a tile's IP (a Ping to the operator's own server) and re-resolves
    // its country only if that comes back different from whatever IP is already
    // pinned for this config - not on every save. previousIP is captured once, up
    // front: a config being saved for the first time has none yet, which is what
    // makes a first resolution behave the same as a real IP change. Both the Ping
    // and (once a change is confirmed) the country lookup retry until they
    // succeed: a single attempt at save time meant "no connectivity right now"
    // turned into "no label on this tile, ever". Backs off to a minute so a long
    // outage costs nothing.
    //
    // The country lookup goes to a third-party geolocation service and so tells it
    // the address of the user's server - see internal/geoip. That is why it only
    // ever runs after a genuine IP change, never on a timer.
    fun resolveTileMetadataInBackground(id: String, yaml: String, hasExplicitCountry: Boolean) {
        if (!geoJobs.add(id)) return
        coroutineScope.launch {
            try {
                val previousIP = configs.find { it.id == id }?.ip
                var invalidated = false // stale IP/country already cleared for this run
                var backoffMs = 2_000L
                while (isActive) {
                    val ip = fetchPing(yaml)?.first
                    if (!ip.isNullOrBlank()) {
                        if (ip == previousIP) return@launch // same server as before - nothing to do
                        if (!invalidated) {
                            ConfigStore.setServerIP(context, id, ip)
                            ConfigStore.setCountry(context, id, null, null)
                            refreshConfigs()
                            invalidated = true
                        }
                        if (hasExplicitCountry) return@launch
                        val geo = lookupCountry(ip)
                        if (geo != null) {
                            ConfigStore.setCountry(context, id, geo.first, geo.second)
                            refreshConfigs()
                            return@launch
                        }
                    }
                    delay(backoffMs)
                    backoffMs = (backoffMs * 2).coerceAtMost(60_000L)
                }
            } finally {
                geoJobs.remove(id)
            }
        }
    }

    fun resolveGeoInBackground(id: String, yaml: String) {
        val explicit = applyCountryFromYaml(id, yaml)
        refreshConfigs()
        resolveTileMetadataInBackground(id, yaml, explicit)
    }

    // Backfill on launch, in two halves that fail independently.
    //
    // The country is re-applied for every config unconditionally: it is a field in
    // the yaml, costs nothing, and this is what repairs configs saved by an older
    // build that dropped the label whenever the Ping at save time failed.
    //
    // The IP costs a real handshake, so only configs still missing one are chased -
    // and that chase retries until it succeeds rather than giving up after a single
    // attempt on a launch that happened to have no connectivity.
    LaunchedEffect(Unit) {
        val explicit = configs.associate { it.id to applyCountryFromYaml(it.id, it.yaml) }
        refreshConfigs()
        configs
            .filter { it.ip == null || it.countryCode.isNullOrBlank() }
            .forEach { resolveTileMetadataInBackground(it.id, it.yaml, explicit[it.id] == true) }
    }

    Box(modifier = modifier.fillMaxSize()) {
        when (screen) {
            Screen.LOG -> LogScreen(onClose = { screen = Screen.SETTINGS })
            Screen.SETTINGS -> SettingsScreen(
                onBack = { screen = Screen.MAIN },
                onViewLog = { screen = Screen.LOG },
            )
            Screen.ADD_CONFIG -> ConfigScreen(
                yaml = editingYaml,
                isEditing = editingId != null,
                onYamlChange = { editingYaml = it },
                onSave = {
                    val id = editingId
                    val targetId = if (id != null) {
                        ConfigStore.update(context, id, editingYaml)
                        id
                    } else {
                        ConfigStore.add(context, editingYaml).id
                    }
                    refreshConfigs()
                    screen = Screen.MAIN
                    resolveGeoInBackground(targetId, editingYaml)
                },
                onDelete = {
                    val id = editingId
                    if (id != null) {
                        if (state.activeConfigId == id) onDisconnect()
                        ProxyManager.stop(id)
                        notifyProxyStateChanged()
                        ConfigStore.delete(context, id)
                        refreshConfigs()
                    }
                    screen = Screen.MAIN
                },
                onBack = { screen = Screen.MAIN },
            )
            Screen.MAIN -> MainScreen(
                status = state.status,
                message = state.message,
                activeConfigId = state.activeConfigId,
                configs = configs,
                resources = resources,
                appInForeground = appInForeground,
                hasUpdate = updateInfo != null,
                updateProgress = updateProgress,
                isUpdating = isUpdating,
                onUpdateClick = { applyUpdate() },
                proxyRunningPorts = proxyRunningPorts,
                onToggleProxy = { config, portText -> toggleProxy(config, portText) },
                onToggle = { config ->
                    when {
                        state.activeConfigId == config.id && state.status == ConnectionStatus.CONNECTED -> onDisconnect()
                        state.activeConfigId == config.id && state.status == ConnectionStatus.CONNECTING -> Unit
                        else -> onConnect(config)
                    }
                },
                onEditConfig = { config ->
                    editingId = config.id
                    editingYaml = config.yaml
                    screen = Screen.ADD_CONFIG
                },
                onAddConfig = {
                    editingId = null
                    editingYaml = ""
                    screen = Screen.ADD_CONFIG
                },
                onAddResource = { name, url ->
                    ResourceStore.add(context, name, url)
                    refreshResources()
                },
                onDeleteResource = { id ->
                    ResourceStore.delete(context, id)
                    refreshResources()
                },
                onOpenSettings = { screen = Screen.SETTINGS },
            )
        }
    }
}

/**
 * Two swipeable pages sharing one fixed header: configs on the left/page 0 (the
 * default), resource-reachability tiles on the right/page 1 (swipe left to reach it).
 * Only the page currently on screen ever pings anything, and only while [appInForeground]
 * is true - see ConfigInfoCard/ResourceCard's pingEnabled parameter.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun MainScreen(
    status: ConnectionStatus,
    message: String,
    activeConfigId: String?,
    configs: List<SavedConfig>,
    resources: List<PingResource>,
    appInForeground: Boolean,
    hasUpdate: Boolean,
    isUpdating: Boolean,
    updateProgress: Int?,
    onUpdateClick: () -> Unit,
    proxyRunningPorts: Map<String, Int>,
    onToggleProxy: (SavedConfig, String) -> Unit,
    onToggle: (SavedConfig) -> Unit,
    onEditConfig: (SavedConfig) -> Unit,
    onAddConfig: () -> Unit,
    onAddResource: (String, String) -> Unit,
    onDeleteResource: (String) -> Unit,
    onOpenSettings: () -> Unit,
) {
    val pagerState = rememberPagerState(pageCount = { 2 })
    val coroutineScope = rememberCoroutineScope()
    var showAddResourceDialog by remember { mutableStateOf(false) }

    Column(modifier = Modifier.fillMaxSize().padding(horizontal = 16.dp, vertical = 24.dp)) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.fillMaxWidth(),
        ) {
            // The logo sits on its own radial glow rather than a plate/tile
            // background - the glow *is* the backing (see style.css's
            // .emblem-wrap on Windows for the same treatment).
            Box(modifier = Modifier.size(44.dp), contentAlignment = Alignment.Center) {
                Box(
                    modifier = Modifier
                        .size(64.dp)
                        .background(
                            Brush.radialGradient(listOf(Primary.copy(alpha = 0.34f), Primary.copy(alpha = 0f))),
                            CircleShape,
                        ),
                )
                Image(
                    painter = painterResource(R.drawable.ic_logo_emblem),
                    contentDescription = null,
                    modifier = Modifier.size(42.dp),
                )
            }
            Spacer(modifier = Modifier.width(10.dp))
            Text(
                "Phantom",
                color = TextPrimary,
                fontSize = 20.sp,
                fontWeight = FontWeight.SemiBold,
            )
            // Pushes everything after it (settings, and the update button when
            // present) to the row's right edge, instead of them trailing right
            // after the title.
            Spacer(modifier = Modifier.weight(1f))
            if (hasUpdate) {
                IconButton(onClick = onUpdateClick, enabled = !isUpdating) {
                    Text(
                        "⬇",
                        fontSize = 20.sp,
                        color = if (isUpdating) TextSecondary else Success,
                    )
                }
            }
            IconButton(onClick = onOpenSettings) {
                Image(
                    painter = painterResource(R.drawable.ic_settings_gear),
                    contentDescription = null,
                    colorFilter = ColorFilter.tint(TextSecondary),
                    modifier = Modifier.size(22.dp),
                )
            }
        }

        // Update download progress, shown under the header/logo while
        // downloadAndInstallUpdate is streaming the APK to disk - see
        // applyUpdate's onProgress callback. Absent (not just empty) the rest
        // of the time, including the already-downloaded/needs-install-
        // permission branches, which never report progress.
        if (updateProgress != null) {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 8.dp)
                    .height(3.dp)
                    .clip(RoundedCornerShape(2.dp))
                    .background(SurfaceOutline),
            ) {
                Box(
                    modifier = Modifier
                        .fillMaxHeight()
                        .fillMaxWidth((updateProgress / 100f).coerceIn(0f, 1f))
                        .background(Brush.linearGradient(BrandGradient)),
                )
            }
        }

        Spacer(modifier = Modifier.height(20.dp))

        HorizontalPager(
            state = pagerState,
            modifier = Modifier.weight(1f),
            // Default pageSpacing is 0 - with no gap, the outgoing and incoming
            // pages' tiles sit flush against each other mid-swipe (each page's
            // own content runs edge-to-edge, so nothing but the shared outer
            // padding separated them). This gives the transition the same
            // breathing room the pages already have at rest.
            pageSpacing = 24.dp,
        ) { page ->
            when (page) {
                0 -> ConfigsPage(
                    status = status,
                    message = message,
                    activeConfigId = activeConfigId,
                    configs = configs,
                    pingEnabled = appInForeground && pagerState.currentPage == 0,
                    proxyRunningPorts = proxyRunningPorts,
                    onToggleProxy = onToggleProxy,
                    onToggle = onToggle,
                    onEditConfig = onEditConfig,
                    onAddConfig = onAddConfig,
                )
                else -> ResourcesPage(
                    resources = resources,
                    pingEnabled = appInForeground && pagerState.currentPage == 1,
                    onAdd = { showAddResourceDialog = true },
                    onDelete = onDeleteResource,
                )
            }
        }

        BottomNavBar(
            currentPage = pagerState.currentPage,
            onSelect = { page -> coroutineScope.launch { pagerState.animateScrollToPage(page) } },
        )
    }

    if (showAddResourceDialog) {
        AddResourceDialog(
            onDismiss = { showAddResourceDialog = false },
            onSave = { name, url ->
                showAddResourceDialog = false
                onAddResource(name, url)
            },
        )
    }
}

/**
 * iOS-style bottom tab bar: icon only, no label, the current page tinted with the
 * accent color and everything else muted - mirrors the pager's own two pages
 * (configs/mask, resources/signal bars) so tapping is just another way to switch
 * pages alongside swiping, not a separate navigation model.
 */
// Same tile language as the config/palette cards (18dp rounded, Surface fill,
// SurfaceOutline border) instead of floating bare over the background - reads
// as one more piece of the app's tile-based design system rather than a
// leftover plain icon row.
@Composable
private fun BottomNavBar(
    currentPage: Int,
    onSelect: (Int) -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(top = 8.dp)
            .clip(RoundedCornerShape(18.dp))
            .background(Surface)
            .border(1.dp, SurfaceOutline, RoundedCornerShape(18.dp)),
        horizontalArrangement = Arrangement.SpaceEvenly,
    ) {
        NavBarItem(iconRes = R.drawable.ic_nav_lock, selected = currentPage == 0, onClick = { onSelect(0) })
        NavBarItem(iconRes = R.drawable.ic_nav_globe, selected = currentPage == 1, onClick = { onSelect(1) })
    }
}

// iconRes is a solid black glyph on transparent - only its alpha channel is
// used. Unselected just tints it TextSecondary like any other icon; selected
// recolours it with the active palette's gradient (the same BrandGradient
// used for the connected-tile border and palette-card bar) via a
// draw-then-composite trick: paint the glyph normally, then paint the
// gradient over it with BlendMode.SrcAtop, which only lands where the glyph
// itself was opaque. graphicsLayer(alpha = 0.99f) is required for the
// blend mode to actually composite against this content instead of
// whatever's beneath it.
@Composable
private fun NavBarItem(
    iconRes: Int,
    selected: Boolean,
    onClick: () -> Unit,
) {
    IconButton(onClick = onClick) {
        if (selected) {
            val gradient = Brush.linearGradient(BrandGradient)
            Image(
                painter = painterResource(iconRes),
                contentDescription = null,
                modifier = Modifier
                    .size(26.dp)
                    .graphicsLayer(alpha = 0.99f)
                    .drawWithContent {
                        drawContent()
                        drawRect(brush = gradient, blendMode = BlendMode.SrcAtop)
                    },
            )
        } else {
            Image(
                painter = painterResource(iconRes),
                contentDescription = null,
                colorFilter = ColorFilter.tint(TextSecondary),
                modifier = Modifier.size(26.dp),
            )
        }
    }
}

@Composable
private fun ConfigsPage(
    status: ConnectionStatus,
    message: String,
    activeConfigId: String?,
    configs: List<SavedConfig>,
    pingEnabled: Boolean,
    proxyRunningPorts: Map<String, Int>,
    onToggleProxy: (SavedConfig, String) -> Unit,
    onToggle: (SavedConfig) -> Unit,
    onEditConfig: (SavedConfig) -> Unit,
    onAddConfig: () -> Unit,
) {
    Column(modifier = Modifier.fillMaxSize()) {
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Text(
                I18n.t("configs"),
                color = TextSecondary,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            IconButton(onClick = onAddConfig) {
                Text("+", fontSize = 22.sp, color = TextSecondary)
            }
        }

        // Shown only when the Keystore refused to initialise and configs - which
        // hold the PSK and the server address - ended up in plain storage. It used
        // to happen silently; see ConfigStore.storageIsPlaintext.
        if (ConfigStore.storageIsPlaintext) {
            Spacer(modifier = Modifier.height(10.dp))
            Text(
                text = I18n.t("insecure_storage"),
                color = Danger,
                fontSize = 12.sp,
                modifier = Modifier.fillMaxWidth(),
            )
        }

        Spacer(modifier = Modifier.height(10.dp))

        if (configs.isNotEmpty()) {
            LazyColumn(
                modifier = Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(14.dp),
            ) {
                items(configs, key = { it.id }) { config ->
                    val cardStatus = if (activeConfigId == config.id) status else ConnectionStatus.IDLE
                    ConfigInfoCard(
                        config = config,
                        status = cardStatus,
                        pingEnabled = pingEnabled,
                        proxyRunning = proxyRunningPorts.containsKey(config.id),
                        proxyPort = proxyRunningPorts[config.id],
                        showProxy = Appearance.showProxySettings,
                        onToggle = { onToggle(config) },
                        onToggleProxy = { portText -> onToggleProxy(config, portText) },
                        onLongPress = { onEditConfig(config) },
                    )
                }
            }

            if (status == ConnectionStatus.ERROR && message.isNotBlank()) {
                Spacer(modifier = Modifier.height(12.dp))
                val bannerShape = RoundedCornerShape(12.dp)
                Text(
                    text = message,
                    color = Danger,
                    fontSize = 13.5.sp,
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(bannerShape)
                        .background(Danger.copy(alpha = 0.12f))
                        .border(1.dp, Danger.copy(alpha = 0.3f), bannerShape)
                        .padding(horizontal = 14.dp, vertical = 10.dp),
                )
            }
        } else {
            EmptyState(
                modifier = Modifier.weight(1f).fillMaxWidth(),
                title = I18n.t("no_configs"),
                hint = I18n.t("no_configs_hint"),
            )
        }
    }
}

// A ringed circle with an icon, a title and a hint - matches the Windows
// client's .empty-state exactly (see style.css).
@Composable
private fun EmptyState(modifier: Modifier = Modifier, title: String, hint: String) {
    Column(
        modifier = modifier.padding(horizontal = 48.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Box(
            modifier = Modifier
                .size(96.dp)
                .clip(CircleShape)
                .background(Surface)
                .border(1.dp, SurfaceOutline, CircleShape),
            contentAlignment = Alignment.Center,
        ) {
            Icon(Icons.Filled.Description, contentDescription = null, tint = TextMuted, modifier = Modifier.size(42.dp))
        }
        Spacer(modifier = Modifier.height(24.dp))
        Text(text = title, color = TextPrimary, fontSize = 20.sp, fontWeight = FontWeight.SemiBold)
        Spacer(modifier = Modifier.height(8.dp))
        Text(
            text = hint, color = TextSecondary, fontSize = 14.5.sp,
            textAlign = TextAlign.Center, lineHeight = 21.75.sp,
        )
    }
}

@Composable
private fun ResourcesPage(
    resources: List<PingResource>,
    pingEnabled: Boolean,
    onAdd: () -> Unit,
    onDelete: (String) -> Unit,
) {
    Column(modifier = Modifier.fillMaxSize()) {
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
            Text(
                I18n.t("resources"),
                color = TextSecondary,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            IconButton(onClick = onAdd) {
                Text("+", fontSize = 22.sp, color = TextSecondary)
            }
        }

        Spacer(modifier = Modifier.height(10.dp))

        if (resources.isNotEmpty()) {
            LazyColumn(
                modifier = Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(14.dp),
            ) {
                items(resources, key = { it.id }) { resource ->
                    ResourceCard(
                        resource = resource,
                        pingEnabled = pingEnabled,
                        onDelete = { onDelete(resource.id) },
                    )
                }
            }
        } else {
            EmptyState(
                modifier = Modifier.weight(1f).fillMaxWidth(),
                title = I18n.t("no_resources"),
                hint = I18n.t("no_resources_hint"),
            )
        }
    }
}

@Composable
private fun AddResourceDialog(
    onDismiss: () -> Unit,
    onSave: (String, String) -> Unit,
) {
    var name by remember { mutableStateOf("") }
    var url by remember { mutableStateOf("") }

    val fieldColors = OutlinedTextFieldDefaults.colors(
        focusedTextColor = TextPrimary,
        unfocusedTextColor = TextPrimary,
        focusedBorderColor = Primary,
        unfocusedBorderColor = TextSecondary.copy(alpha = 0.4f),
        cursorColor = Primary,
    )

    AlertDialog(
        onDismissRequest = onDismiss,
        shape = RoundedCornerShape(24.dp),
        title = { Text(I18n.t("add_resource"), fontSize = 19.sp, fontWeight = FontWeight.SemiBold) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    placeholder = { Text(I18n.t("resource_name_ph"), color = TextSecondary) },
                    singleLine = true,
                    colors = fieldColors,
                    modifier = Modifier.fillMaxWidth(),
                )
                OutlinedTextField(
                    value = url,
                    onValueChange = { url = it },
                    placeholder = { Text("example.com", color = TextSecondary) },
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                    colors = fieldColors,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        },
        confirmButton = {
            TextButton(onClick = {
                val trimmedName = name.trim()
                val trimmedUrl = url.trim()
                if (trimmedName.isBlank() || trimmedUrl.isBlank()) return@TextButton
                val fullUrl = if (trimmedUrl.startsWith("http://") || trimmedUrl.startsWith("https://")) {
                    trimmedUrl
                } else {
                    "https://$trimmedUrl"
                }
                onSave(trimmedName, fullUrl)
            }) { Text(I18n.t("add"), color = Primary) }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text(I18n.t("cancel")) }
        },
        containerColor = SurfaceHigh,
        titleContentColor = TextPrimary,
        textContentColor = TextSecondary,
    )
}

@Composable
private fun ConfigScreen(
    yaml: String,
    isEditing: Boolean,
    onYamlChange: (String) -> Unit,
    onSave: () -> Unit,
    onDelete: () -> Unit,
    onBack: () -> Unit,
) {
    var showDeleteConfirm by remember { mutableStateOf(false) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(20.dp)
            .verticalScroll(rememberScrollState()),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onBack) {
                Image(
                    painter = painterResource(R.drawable.ic_back_arrow),
                    contentDescription = null,
                    colorFilter = ColorFilter.tint(TextPrimary),
                    modifier = Modifier.size(20.dp),
                )
            }
            Text(
                if (isEditing) I18n.t("edit_config_title") else I18n.t("add_config_title"),
                color = TextPrimary,
                fontSize = 20.sp,
                fontWeight = FontWeight.SemiBold,
            )
        }

        Text(
            I18n.t("paste_yaml"),
            color = TextSecondary,
            fontSize = 13.sp,
        )

        OutlinedTextField(
            value = yaml,
            onValueChange = onYamlChange,
            placeholder = { Text("server: \"1.2.3.4:443\"\ndomain: \"your-domain.com\"\n...", color = TextSecondary) },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Text),
            textStyle = androidx.compose.ui.text.TextStyle(fontFamily = FontFamily.Monospace, fontSize = 13.sp),
            colors = OutlinedTextFieldDefaults.colors(
                focusedTextColor = TextPrimary,
                unfocusedTextColor = TextPrimary,
                focusedBorderColor = Primary,
                unfocusedBorderColor = TextSecondary.copy(alpha = 0.4f),
                cursorColor = Primary,
            ),
            modifier = Modifier
                .fillMaxWidth()
                .height(280.dp),
        )

        Button(
            onClick = onSave,
            shape = RoundedCornerShape(16.dp),
            colors = ButtonDefaults.buttonColors(containerColor = Primary, contentColor = Color.White),
            modifier = Modifier.fillMaxWidth().height(54.dp),
        ) {
            Text(I18n.t("save"), fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
        }

        if (isEditing) {
            TextButton(
                onClick = { showDeleteConfirm = true },
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text(I18n.t("delete_config"), color = Danger, fontSize = 14.sp, fontWeight = FontWeight.Medium)
            }
        }
    }

    if (showDeleteConfirm) {
        AlertDialog(
            onDismissRequest = { showDeleteConfirm = false },
            shape = RoundedCornerShape(24.dp),
            title = { Text(I18n.t("delete_config_q"), fontSize = 19.sp, fontWeight = FontWeight.SemiBold) },
            text = { Text(I18n.t("delete_config_text"), fontSize = 15.sp, lineHeight = 22.5.sp) },
            confirmButton = {
                TextButton(onClick = {
                    showDeleteConfirm = false
                    onDelete()
                }) { Text(I18n.t("delete"), color = Danger) }
            },
            dismissButton = {
                TextButton(onClick = { showDeleteConfirm = false }) { Text(I18n.t("cancel")) }
            },
            containerColor = SurfaceHigh,
            titleContentColor = TextPrimary,
            textContentColor = TextSecondary,
        )
    }
}

@Composable
private fun SettingsScreen(
    onBack: () -> Unit,
    onViewLog: () -> Unit,
) {
    val context = LocalContext.current
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
            Text(I18n.t("settings"), color = TextPrimary, fontSize = 20.sp, fontWeight = FontWeight.SemiBold)
        }

        // A single scrollable list, padded 20/8/20/32 (start/top/end/bottom) -
        // everything below the header lives in here.
        Column(
            modifier = Modifier
                .weight(1f)
                .verticalScroll(rememberScrollState())
                .padding(start = 20.dp, top = 8.dp, end = 20.dp, bottom = 32.dp),
        ) {
            // Show/hide the per-config proxy controls (button + port field) -
            // read by ConfigInfoCard below. Above the language selector since
            // it's the setting most likely to be flipped once and forgotten.
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    I18n.t("show_proxy_settings"),
                    color = TextPrimary,
                    fontSize = 15.sp,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier.weight(1f),
                )
                GradientSwitch(
                    checked = Appearance.showProxySettings,
                    onCheckedChange = { setShowProxySettings(context, it) },
                )
            }
            Spacer(Modifier.height(28.dp))

            // Language selector - reading I18n.lang here recomposes the whole
            // app (every screen goes through I18n.t) when it changes. Tiles
            // match the palette cards' visual language rather than the plain
            // text buttons used previously.
            SectionLabel(I18n.t("language"))
            Spacer(Modifier.height(14.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                LangTile("Русский", I18n.lang == Lang.RU, modifier = Modifier.weight(1f)) { setAppLanguage(context, Lang.RU) }
                LangTile("English", I18n.lang == Lang.EN, modifier = Modifier.weight(1f)) { setAppLanguage(context, Lang.EN) }
            }

            // Palette. Reading Appearance.palette repaints everything for the
            // same reason the language toggle does - every colour in Theme.kt
            // is a computed property over this state. Cards rather than
            // swatches: each is drawn in the colours of the palette it
            // represents (not the active one), an honest "what would this
            // look like" preview.
            Spacer(Modifier.height(28.dp))
            SectionLabel(I18n.t("palette"))
            Spacer(Modifier.height(14.dp))
            PaletteGrid(context)

            // Animated backdrop. Each row carries a live thumbnail actually
            // running that variant, not a static description - see
            // BackgroundThumbnail (AnimatedBackground.kt). 32px here, not the
            // page's ambient 28px, to set this section apart as a pair with
            // Палитра above it.
            Spacer(Modifier.height(32.dp))
            SectionLabel(I18n.t("background"))
            Spacer(Modifier.height(14.dp))
            BackgroundGrid(context)

            Spacer(Modifier.height(28.dp))
            LangTile(I18n.t("view_log"), selected = false, onClick = onViewLog)

            // Worth having somewhere visible: the app updates itself from
            // GitHub releases, so "which version am I actually running" is
            // the first thing anyone needs when an update does or doesn't
            // arrive. Comes from BuildConfig, so it is whatever the APK was
            // built as and cannot drift from it.
            Spacer(Modifier.height(16.dp))
            Text(
                text = "${I18n.t("version")} ${BuildConfig.VERSION_NAME}",
                color = TextSecondary,
                fontSize = 12.sp,
                modifier = Modifier.fillMaxWidth(),
                textAlign = TextAlign.Center,
            )
        }
    }
}

// Flutter's SliverGridDelegateWithMaxCrossAxisExtent(maxCrossAxisExtent: 200)
// picks columns = ceil(width / 200), stretching that many columns evenly to
// fill the row - not a lazy grid (only 6 items, and it lives inside the
// screen's own scrollable Column, where a nested lazy grid would fight it for
// scroll gestures), just chunked Rows sized off BoxWithConstraints.
@Composable
private fun PaletteGrid(context: android.content.Context) {
    BoxWithConstraints {
        val columns = kotlin.math.ceil(maxWidth.value / 200f).toInt().coerceAtLeast(1)
        Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Palette.entries.chunked(columns).forEach { rowPalettes ->
                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    rowPalettes.forEach { palette ->
                        PaletteCard(
                            palette = palette,
                            selected = Appearance.palette == palette,
                            onClick = { Appearance.setPalette(context, palette) },
                            modifier = Modifier.weight(1f),
                        )
                    }
                    // Pads an incomplete last row so its cards stay the same
                    // width as the full rows above instead of stretching wider.
                    repeat(columns - rowPalettes.size) { Spacer(modifier = Modifier.weight(1f)) }
                }
            }
        }
    }
}

// Drawn entirely in [palette]'s own colours, not the active theme's - every
// card is an honest preview of what selecting it looks like. Selection is a
// border colour + width change (1px outline -> 2px primary), animated so it
// reads as a response without changing the card's size.
@Composable
private fun PaletteCard(palette: Palette, selected: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier) {
    val borderWidth by animateDpAsState(if (selected) 2.dp else 1.dp, animationSpec = tween(200), label = "paletteCardBorderWidth")
    val borderColor by animateColorAsState(if (selected) palette.primary else palette.surfaceOutline, animationSpec = tween(200), label = "paletteCardBorderColor")
    val shape = RoundedCornerShape(18.dp)
    Column(
        modifier = modifier
            .aspectRatio(1.55f)
            .clip(shape)
            .background(palette.surface)
            .border(borderWidth, borderColor, shape)
            .clickable(onClick = onClick)
            .padding(14.dp),
    ) {
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            Box(Modifier.size(16.dp).clip(CircleShape).background(palette.primary))
            Box(Modifier.size(16.dp).clip(CircleShape).background(palette.accent))
            Box(Modifier.size(16.dp).clip(CircleShape).background(palette.surfaceHigh))
        }
        Spacer(Modifier.weight(1f))
        Text(I18n.t(palette.nameKey), color = palette.textPrimary, fontSize = 15.sp, fontWeight = FontWeight.SemiBold)
        Spacer(Modifier.height(4.dp))
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .height(6.dp)
                .clip(RoundedCornerShape(100.dp))
                .background(Brush.linearGradient(listOf(palette.primary, palette.accent))),
        )
    }
}

// Two per row (unlike the palette grid, not responsive to width - eight
// variants read better as a fixed 2-column block than a grid that reflows
// column count). The name sits directly on the live thumbnail rather than
// beside it, so each tile is the image - no description text, selection is
// the border alone, same as everywhere else in Settings now.
@Composable
private fun BackgroundGrid(context: android.content.Context) {
    Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        BackgroundStyle.entries.toList().chunked(2).forEach { rowStyles ->
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                rowStyles.forEach { style ->
                    BackgroundTile(
                        style = style,
                        selected = Appearance.background == style,
                        onClick = { Appearance.setBackground(context, style) },
                        modifier = Modifier.weight(1f),
                    )
                }
                repeat(2 - rowStyles.size) { Spacer(modifier = Modifier.weight(1f)) }
            }
        }
    }
}

@Composable
private fun BackgroundTile(style: BackgroundStyle, selected: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier) {
    val borderWidth by animateDpAsState(if (selected) 2.dp else 1.dp, animationSpec = tween(200), label = "bgTileBorderWidth")
    val borderColor by animateColorAsState(if (selected) Primary else SurfaceOutline, animationSpec = tween(200), label = "bgTileBorderColor")
    val shape = RoundedCornerShape(18.dp)
    Box(
        contentAlignment = Alignment.Center,
        modifier = modifier
            .aspectRatio(1.55f)
            .clip(shape)
            .border(borderWidth, borderColor, shape)
            .clickable(onClick = onClick),
    ) {
        BackgroundThumbnail(style = style, modifier = Modifier.matchParentSize())
        // A flat scrim, not a gradient - the name is centered, not anchored
        // to one edge, so it needs contrast behind it regardless of where a
        // given variant's brightest particles happen to land.
        Box(modifier = Modifier.matchParentSize().background(Color.Black.copy(alpha = 0.32f)))
        Text(
            I18n.t(style.labelKey),
            color = Color.White,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
}

// Section header: 13sp/600, +0.8sp tracking, muted - matches the Windows
// client's .settings-section-title exactly (see style.css).
@Composable
private fun SectionLabel(text: String) {
    Text(text, color = TextMuted, fontSize = 13.sp, fontWeight = FontWeight.SemiBold, letterSpacing = 0.8.sp)
}

// Same tile language as PaletteCard (18dp rounded, 1px/2px animated border,
// checkmark badge when selected) so the language switcher reads as part of
// the same design system rather than a leftover plain button pair.
@Composable
private fun LangTile(label: String, selected: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    val borderWidth by animateDpAsState(if (selected) 2.dp else 1.dp, animationSpec = tween(200), label = "langTileBorderWidth")
    val borderColor by animateColorAsState(if (selected) Primary else SurfaceOutline, animationSpec = tween(200), label = "langTileBorderColor")
    val shape = RoundedCornerShape(18.dp)
    Box(
        contentAlignment = Alignment.Center,
        modifier = modifier
            .height(52.dp)
            .clip(shape)
            .background(Surface)
            .border(borderWidth, borderColor, shape)
            .clickable(onClick = onClick)
            .padding(horizontal = 20.dp),
    ) {
        Text(
            label,
            color = if (selected) TextPrimary else TextSecondary,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
}

// setAppLanguage persists the choice and re-posts the persistent notification so
// its already-shown text/actions switch language immediately too, not just the
// Compose UI.
private fun setAppLanguage(context: android.content.Context, lang: Lang) {
    I18n.set(context, lang)
    context.startService(Intent(context, PhantomVpnService::class.java).apply {
        action = PhantomVpnService.ACTION_SHOW_STATUS
    })
}

// Same reasoning as setAppLanguage above: the persistent notification's text
// and action buttons are built from Appearance.showProxySettings, but the
// service only re-evaluates that when asked to - without this, the running
// notification would keep showing/hiding the proxy line until something else
// (opening the app, connecting, etc.) happened to re-post it.
private fun setShowProxySettings(context: android.content.Context, show: Boolean) {
    Appearance.setShowProxySettings(context, show)
    context.startService(Intent(context, PhantomVpnService::class.java).apply {
        action = PhantomVpnService.ACTION_SHOW_STATUS
    })
}

@Composable
private fun LogScreen(onClose: () -> Unit) {
    val context = LocalContext.current
    val logText = remember { FileLog.readAll() }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onClose) {
                Image(
                    painter = painterResource(R.drawable.ic_back_arrow),
                    contentDescription = null,
                    colorFilter = ColorFilter.tint(TextPrimary),
                    modifier = Modifier.size(20.dp),
                )
            }
            Text(I18n.t("log_title", FileLog.path()), color = TextPrimary, fontSize = 15.sp)
        }

        Text(
            text = logText,
            color = TextSecondary,
            fontFamily = FontFamily.Monospace,
            fontSize = 11.sp,
            modifier = Modifier
                .weight(1f)
                .fillMaxWidth()
                .verticalScroll(rememberScrollState()),
        )

        Button(
            onClick = {
                val shareIntent = Intent(Intent.ACTION_SEND).apply {
                    type = "text/plain"
                    putExtra(Intent.EXTRA_TEXT, logText)
                }
                context.startActivity(Intent.createChooser(shareIntent, "Share Phantom log"))
            },
            shape = RoundedCornerShape(16.dp),
            colors = ButtonDefaults.buttonColors(containerColor = Primary, contentColor = Color.White),
            modifier = Modifier.fillMaxWidth().height(54.dp),
        ) {
            Text(I18n.t("share"), fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
        }
    }
}
