package com.phantom.vpn

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.ContextCompat

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
    var configs by remember { mutableStateOf(ConfigStore.loadAll(context)) }
    var screen by remember { mutableStateOf(Screen.MAIN) }
    var editingId by remember { mutableStateOf<String?>(null) }
    var editingYaml by remember { mutableStateOf("") }
    val state by VpnStateHolder.state.collectAsState()

    fun refreshConfigs() {
        configs = ConfigStore.loadAll(context)
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
                if (id != null) ConfigStore.update(context, id, editingYaml) else ConfigStore.add(context, editingYaml)
                refreshConfigs()
                screen = Screen.MAIN
            },
            onDelete = {
                val id = editingId
                if (id != null) {
                    if (state.activeConfigId == id) onDisconnect()
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
            onOpenSettings = { screen = Screen.SETTINGS },
        )
    }
}

@Composable
private fun MainScreen(
    status: ConnectionStatus,
    message: String,
    activeConfigId: String?,
    configs: List<SavedConfig>,
    onToggle: (SavedConfig) -> Unit,
    onEditConfig: (SavedConfig) -> Unit,
    onAddConfig: () -> Unit,
    onOpenSettings: () -> Unit,
) {
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
            Spacer(modifier = Modifier.weight(1f))
            IconButton(onClick = onAddConfig) {
                Text("+", fontSize = 24.sp, color = TextSecondary)
            }
            IconButton(onClick = onOpenSettings) {
                Text("⚙", fontSize = 22.sp, color = TextSecondary)
            }
        }

        Spacer(modifier = Modifier.height(28.dp))

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
                        onToggle = { onToggle(config) },
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
