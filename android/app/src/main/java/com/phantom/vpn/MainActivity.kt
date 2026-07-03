package com.phantom.vpn

import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey

private enum class Screen { MAIN, CONFIG, LOG }

class MainActivity : ComponentActivity() {

    private var prefs: android.content.SharedPreferences? = null
    private var pendingYaml: String? = null

    private val vpnPrepareLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        val yaml = pendingYaml
        pendingYaml = null
        if (result.resultCode == RESULT_OK && yaml != null) {
            startVpn(yaml)
        } else {
            VpnStateHolder.update(ConnectionStatus.ERROR, "VPN permission denied")
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        FileLog.i("MainActivity.onCreate")

        // EncryptedSharedPreferences touches the Android Keystore and can throw on some
        // devices/ROMs; never let that take the whole app down - fall back to plain prefs
        // and keep going so the user at least sees the UI (and the log button).
        val securePrefs = try {
            val masterKey = MasterKey.Builder(this)
                .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
                .build()
            EncryptedSharedPreferences.create(
                this, "phantom_secure_prefs", masterKey,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
            )
        } catch (t: Throwable) {
            FileLog.e("EncryptedSharedPreferences init failed, falling back to plain prefs", t)
            null
        }
        prefs = securePrefs ?: getSharedPreferences("phantom_plain_prefs", MODE_PRIVATE)

        setContent {
            PhantomTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = BgDeep) {
                    PhantomApp(
                        initialYaml = prefs?.getString("client_yaml", "") ?: "",
                        onSaveYaml = { yaml -> prefs?.edit()?.putString("client_yaml", yaml)?.apply() },
                        onConnect = { yaml -> requestConnect(yaml) },
                        onDisconnect = { stopVpn() },
                    )
                }
            }
        }
    }

    private fun requestConnect(yaml: String) {
        FileLog.i("requestConnect")
        val prepareIntent = VpnService.prepare(this)
        if (prepareIntent != null) {
            pendingYaml = yaml
            vpnPrepareLauncher.launch(prepareIntent)
        } else {
            startVpn(yaml)
        }
    }

    private fun startVpn(yaml: String) {
        val intent = Intent(this, PhantomVpnService::class.java).apply {
            action = PhantomVpnService.ACTION_CONNECT
            putExtra(PhantomVpnService.EXTRA_CONFIG_YAML, yaml)
        }
        startService(intent)
    }

    private fun stopVpn() {
        val intent = Intent(this, PhantomVpnService::class.java).apply {
            action = PhantomVpnService.ACTION_DISCONNECT
        }
        startService(intent)
    }
}

@Composable
private fun PhantomApp(
    initialYaml: String,
    onSaveYaml: (String) -> Unit,
    onConnect: (String) -> Unit,
    onDisconnect: () -> Unit,
) {
    var yaml by remember { mutableStateOf(initialYaml) }
    var screen by remember { mutableStateOf(Screen.MAIN) }
    val state by VpnStateHolder.state.collectAsState()

    when (screen) {
        Screen.LOG -> LogScreen(onClose = { screen = Screen.CONFIG })
        Screen.CONFIG -> ConfigScreen(
            yaml = yaml,
            onYamlChange = { yaml = it },
            onSave = { onSaveYaml(yaml) },
            onBack = { screen = Screen.MAIN },
            onViewLog = { screen = Screen.LOG },
        )
        Screen.MAIN -> MainScreen(
            status = state.status,
            message = state.message,
            hasConfig = yaml.isNotBlank(),
            onToggle = {
                when (state.status) {
                    ConnectionStatus.CONNECTED -> onDisconnect()
                    ConnectionStatus.CONNECTING -> Unit
                    else -> if (yaml.isNotBlank()) onConnect(yaml) else screen = Screen.CONFIG
                }
            },
            onOpenConfig = { screen = Screen.CONFIG },
        )
    }
}

@Composable
private fun MainScreen(
    status: ConnectionStatus,
    message: String,
    hasConfig: Boolean,
    onToggle: () -> Unit,
    onOpenConfig: () -> Unit,
) {
    Column(modifier = Modifier.fillMaxSize().padding(24.dp)) {
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
            IconButton(onClick = onOpenConfig) {
                Text("⚙", fontSize = 22.sp, color = TextSecondary)
            }
        }

        Column(
            modifier = Modifier.weight(1f).fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            ConnectButton(status = status, onClick = onToggle)

            Spacer(modifier = Modifier.height(28.dp))

            Text(
                text = statusLabel(status),
                color = statusColor(status),
                fontSize = 18.sp,
                fontWeight = FontWeight.Medium,
            )

            if (status == ConnectionStatus.ERROR && message.isNotBlank()) {
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = message,
                    color = TextSecondary,
                    fontSize = 13.sp,
                    modifier = Modifier.padding(horizontal = 32.dp),
                )
            } else if (!hasConfig) {
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = "Нажмите ⚙ и вставьте client.yaml",
                    color = TextSecondary,
                    fontSize = 13.sp,
                )
            }
        }
    }
}

private fun statusLabel(status: ConnectionStatus): String = when (status) {
    ConnectionStatus.IDLE -> "Отключено"
    ConnectionStatus.CONNECTING -> "Подключение..."
    ConnectionStatus.CONNECTED -> "Подключено"
    ConnectionStatus.ERROR -> "Ошибка подключения"
}

@Composable
private fun statusColor(status: ConnectionStatus): Color = when (status) {
    ConnectionStatus.CONNECTED -> StatusConnected
    ConnectionStatus.ERROR -> StatusError
    ConnectionStatus.CONNECTING -> AccentLavenderBright
    ConnectionStatus.IDLE -> TextSecondary
}

@Composable
private fun ConfigScreen(
    yaml: String,
    onYamlChange: (String) -> Unit,
    onSave: () -> Unit,
    onBack: () -> Unit,
    onViewLog: () -> Unit,
) {
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
            Text("Конфигурация", color = TextPrimary, fontSize = 20.sp, fontWeight = FontWeight.SemiBold)
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

        OutlinedButton(onClick = onViewLog, modifier = Modifier.fillMaxWidth()) {
            Text("Посмотреть лог")
        }
    }
}

@Composable
private fun LogScreen(onClose: () -> Unit) {
    val context = androidx.compose.ui.platform.LocalContext.current
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
