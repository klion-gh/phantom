package com.phantom.vpn

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
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
import androidx.compose.material.icons.filled.SignalCellularAlt
import androidx.compose.material.icons.filled.TheaterComedy
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
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
                Surface(modifier = Modifier.fillMaxSize(), color = BgDeep) {
                    PhantomApp(
                        onConnect = { config -> requestConnect(config) },
                        onDisconnect = { stopVpn() },
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
    LaunchedEffect(Unit) {
        updateInfo = checkForUpdate(BuildConfig.VERSION_NAME)
    }

    // Independent per-config SOCKS5 proxy toggle state - see ProxyManager. Entirely
    // separate from state.activeConfigId/the full-tunnel VPN above. Maps id -> the
    // actual bound port, so the UI's port field can show/restore it after a re-render.
    var proxyRunningPorts by remember { mutableStateOf(mapOf<String, Int>()) }

    fun applyUpdate() {
        val info = updateInfo ?: return
        if (isUpdating) return
        isUpdating = true
        coroutineScope.launch {
            val ok = downloadAndInstallUpdate(context, info)
            isUpdating = false
            if (!ok) {
                Toast.makeText(context, "Не удалось скачать обновление", Toast.LENGTH_LONG).show()
            }
        }
    }

    fun refreshConfigs() {
        configs = ConfigStore.loadAll(context)
    }

    fun refreshResources() {
        resources = ResourceStore.loadAll(context)
    }

    // requestedPortText comes straight from the tile's own port field (empty = "any
    // free port"); invalid/out-of-range input is rejected client-side with a Toast
    // before ever calling into the Go core, same as the Windows app's port field.
    fun toggleProxy(config: SavedConfig, requestedPortText: String) {
        if (ProxyManager.isRunning(config.id)) {
            ProxyManager.stop(config.id)
            proxyRunningPorts = proxyRunningPorts - config.id
            return
        }

        val trimmed = requestedPortText.trim()
        var requestedPort = 0
        if (trimmed.isNotEmpty()) {
            requestedPort = trimmed.toIntOrNull() ?: -1
            if (requestedPort !in 1..65535) {
                Toast.makeText(context, "Некорректный порт: $trimmed", Toast.LENGTH_SHORT).show()
                return
            }
        }

        coroutineScope.launch {
            ProxyManager.start(config.id, config.yaml, requestedPort)
                .onSuccess { port ->
                    proxyRunningPorts = proxyRunningPorts + (config.id to port)
                    ConfigStore.setProxyPort(context, config.id, port)
                    refreshConfigs()
                }
                .onFailure { e ->
                    Toast.makeText(
                        context,
                        "Не удалось включить прокси на порту ${trimmed.ifEmpty { "(любой)" }}: ${e.message}",
                        Toast.LENGTH_LONG,
                    ).show()
                }
        }
    }

    // Resolves and persists a config's server IP/country/flag exactly once (via
    // ConfigStore.setGeo), then refreshes so the tile picks it up - see the "why once,
    // not on a timer" note on SavedConfig.
    fun resolveGeoInBackground(id: String, yaml: String) {
        coroutineScope.launch {
            val (ip, _) = fetchPing(yaml) ?: return@launch
            val geo = fetchGeo(ip)
            ConfigStore.setGeo(context, id, ip, geo?.first, geo?.second)
            refreshConfigs()
        }
    }

    // Configs saved before this per-config geo cache existed have no country/flag yet -
    // backfill them once on launch rather than leaving those tiles blank forever.
    LaunchedEffect(Unit) {
        configs.filter { it.countryCode == null }.forEach { resolveGeoInBackground(it.id, it.yaml) }
    }

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
                    proxyRunningPorts = proxyRunningPorts - id
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
            Image(
                painter = painterResource(R.drawable.ic_logo_emblem),
                contentDescription = null,
                modifier = Modifier.size(36.dp),
            )
            Spacer(modifier = Modifier.width(10.dp))
            Text(
                "Phantom",
                color = TextPrimary,
                fontSize = 20.sp,
                fontWeight = FontWeight.SemiBold,
            )
            IconButton(onClick = onOpenSettings) {
                Text("⚙", fontSize = 22.sp, color = TextSecondary)
            }
            if (hasUpdate) {
                IconButton(onClick = onUpdateClick, enabled = !isUpdating) {
                    Text(
                        "⬇",
                        fontSize = 20.sp,
                        color = if (isUpdating) TextSecondary else StatusConnected,
                    )
                }
            }
            Spacer(modifier = Modifier.weight(1f))
        }

        Spacer(modifier = Modifier.height(20.dp))

        HorizontalPager(
            state = pagerState,
            modifier = Modifier.weight(1f),
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
@Composable
private fun BottomNavBar(
    currentPage: Int,
    onSelect: (Int) -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(top = 8.dp),
        horizontalArrangement = Arrangement.SpaceEvenly,
    ) {
        NavBarItem(icon = Icons.Filled.TheaterComedy, selected = currentPage == 0, onClick = { onSelect(0) })
        NavBarItem(icon = Icons.Filled.SignalCellularAlt, selected = currentPage == 1, onClick = { onSelect(1) })
    }
}

@Composable
private fun NavBarItem(
    icon: ImageVector,
    selected: Boolean,
    onClick: () -> Unit,
) {
    IconButton(onClick = onClick) {
        Icon(
            imageVector = icon,
            contentDescription = null,
            tint = if (selected) AccentLavender else TextSecondary,
            modifier = Modifier.size(26.dp),
        )
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
                "Конфигурации",
                color = TextSecondary,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            IconButton(onClick = onAddConfig) {
                Text("+", fontSize = 22.sp, color = TextSecondary)
            }
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
                        onToggle = { onToggle(config) },
                        onToggleProxy = { portText -> onToggleProxy(config, portText) },
                        onLongPress = { onEditConfig(config) },
                    )
                }
            }

            if (status == ConnectionStatus.ERROR && message.isNotBlank()) {
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = message,
                    color = StatusError,
                    fontSize = 13.sp,
                )
            }
        } else {
            Column(
                modifier = Modifier.weight(1f).fillMaxWidth(),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center,
            ) {
                Text(
                    text = "Нет добавленной конфигурации",
                    color = TextPrimary,
                    fontSize = 16.sp,
                    fontWeight = FontWeight.Medium,
                )
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = "Нажмите + чтобы добавить client.yaml",
                    color = TextSecondary,
                    fontSize = 13.sp,
                )
            }
        }
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
                "Доступность ресурсов",
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
            Column(
                modifier = Modifier.weight(1f).fillMaxWidth(),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center,
            ) {
                Text(
                    text = "Нет добавленных ресурсов",
                    color = TextPrimary,
                    fontSize = 16.sp,
                    fontWeight = FontWeight.Medium,
                )
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = "Нажмите + чтобы добавить сайт для проверки",
                    color = TextSecondary,
                    fontSize = 13.sp,
                )
            }
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
        focusedBorderColor = AccentLavender,
        unfocusedBorderColor = TextSecondary.copy(alpha = 0.4f),
        cursorColor = AccentLavender,
    )

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Добавить ресурс") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    placeholder = { Text("Название, например Netflix", color = TextSecondary) },
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
            }) { Text("Добавить", color = AccentLavender) }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("Отмена") }
        },
        containerColor = BgSurface,
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
                Text("←", fontSize = 22.sp, color = TextPrimary)
            }
            Text(
                if (isEditing) "Редактировать конфигурацию" else "Добавить конфигурацию",
                color = TextPrimary,
                fontSize = 20.sp,
                fontWeight = FontWeight.SemiBold,
            )
        }

        Text(
            "Вставьте содержимое client.yaml целиком:",
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
                focusedBorderColor = AccentLavender,
                unfocusedBorderColor = TextSecondary.copy(alpha = 0.4f),
                cursorColor = AccentLavender,
            ),
            modifier = Modifier
                .fillMaxWidth()
                .height(280.dp),
        )

        Button(
            onClick = onSave,
            colors = ButtonDefaults.buttonColors(containerColor = AccentLavender, contentColor = BgDeep),
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text("Сохранить")
        }

        if (isEditing) {
            OutlinedButton(
                onClick = { showDeleteConfirm = true },
                colors = ButtonDefaults.outlinedButtonColors(contentColor = StatusError),
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text("Удалить конфигурацию")
            }
        }
    }

    if (showDeleteConfirm) {
        AlertDialog(
            onDismissRequest = { showDeleteConfirm = false },
            title = { Text("Удалить конфигурацию?") },
            text = { Text("Придётся снова вставить client.yaml, чтобы подключиться этим профилем.") },
            confirmButton = {
                TextButton(onClick = {
                    showDeleteConfirm = false
                    onDelete()
                }) { Text("Удалить", color = StatusError) }
            },
            dismissButton = {
                TextButton(onClick = { showDeleteConfirm = false }) { Text("Отмена") }
            },
            containerColor = BgSurface,
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
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(20.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onBack) {
                Text("←", fontSize = 22.sp, color = TextPrimary)
            }
            Text("Настройки", color = TextPrimary, fontSize = 20.sp, fontWeight = FontWeight.SemiBold)
        }

        OutlinedButton(onClick = onViewLog, modifier = Modifier.fillMaxWidth()) {
            Text("Посмотреть лог")
        }
    }
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
                Text("←", fontSize = 22.sp, color = TextPrimary)
            }
            Text("Лог (${FileLog.path()})", color = TextPrimary, fontSize = 15.sp)
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
            colors = ButtonDefaults.buttonColors(containerColor = AccentLavender, contentColor = BgDeep),
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text("Поделиться")
        }
    }
}
