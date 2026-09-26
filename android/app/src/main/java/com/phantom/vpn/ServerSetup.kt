package com.phantom.vpn

import android.content.Context
import android.os.Handler
import android.os.Looper
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.ContentPaste
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Mobile
import mobile.ProvisionListener
import org.json.JSONObject

// "Подключить свой сервер": set up Phantom on a VPS from its IP, login and
// password (mobile.ProvisionServer / internal/provision), with live progress.

/** Host key fingerprints of servers set up before, keyed "host:port". */
object KnownServerKeys {
    private const val PREFS = "phantom_ssh_hosts"
    private fun prefs(c: Context) = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
    fun get(c: Context, key: String): String = prefs(c).getString(key, "") ?: ""
    fun put(c: Context, key: String, fp: String) = prefs(c).edit().putString(key, fp).apply()
    fun forget(c: Context, key: String) = prefs(c).edit().remove(key).apply()
}

/** The "+" in the configs header: paste a config, or set up your own server. */
@Composable
fun AddConfigButton(onPaste: () -> Unit, onServer: () -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { open = true }) {
            Text("+", fontSize = 22.sp, color = TextSecondary)
        }
        // This Material3 version's DropdownMenu takes its shape and colour from
        // the theme, so the app's own are supplied through one just for it.
        MaterialTheme(
            colorScheme = MaterialTheme.colorScheme.copy(surface = SurfaceHigh, surfaceContainer = SurfaceHigh),
            shapes = MaterialTheme.shapes.copy(extraSmall = RoundedCornerShape(20.dp)),
        ) {
            DropdownMenu(
                expanded = open,
                onDismissRequest = { open = false },
                modifier = Modifier.background(SurfaceHigh).padding(horizontal = 6.dp),
            ) {
                AddMenuItem(Icons.Filled.ContentPaste, I18n.t("add_menu_paste"), I18n.t("add_menu_paste_sub")) {
                    open = false; onPaste()
                }
                AddMenuItem(Icons.Filled.Dns, I18n.t("add_menu_server"), I18n.t("add_menu_server_sub")) {
                    open = false; onServer()
                }
            }
        }
    }
}

@Composable
private fun AddMenuItem(icon: ImageVector, title: String, sub: String, onClick: () -> Unit) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(14.dp),
        modifier = Modifier
            .widthIn(min = 280.dp)
            .clip(RoundedCornerShape(14.dp))
            .clickable(onClick = onClick)
            .padding(horizontal = 14.dp, vertical = 12.dp),
    ) {
        Box(
            modifier = Modifier.size(42.dp).clip(RoundedCornerShape(12.dp)).background(Surface),
            contentAlignment = Alignment.Center,
        ) { Icon(icon, contentDescription = null, tint = Primary, modifier = Modifier.size(22.dp)) }
        Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(title, color = TextPrimary, fontSize = 15.sp, fontWeight = FontWeight.SemiBold)
            Text(sub, color = TextSecondary, fontSize = 12.5.sp)
        }
    }
}

private enum class StepState { PENDING, ACTIVE, DONE, FAILED }

private val STEPS = listOf("connect", "checks", "install", "verify")

/**
 * The setup dialog. [onConfig] adds the finished config and reports whether
 * it was new (false: that exact config is already in the list).
 */
@Composable
fun ServerSetupDialog(onDismiss: () -> Unit, onConfig: (String) -> Boolean) {
    val context = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    val main = remember { Handler(Looper.getMainLooper()) }

    var host by rememberSaveable { mutableStateOf("") }
    var user by rememberSaveable { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var domain by rememberSaveable { mutableStateOf("") }
    var port by rememberSaveable { mutableStateOf("") }
    var sshPort by rememberSaveable { mutableStateOf("") }
    var formError by remember { mutableStateOf<String?>(null) }

    var showProgress by remember { mutableStateOf(false) }
    var running by remember { mutableStateOf(false) }
    val steps = remember { mutableStateMapOf<String, StepState>() }
    val log = remember { mutableStateListOf<String>() }
    var result by remember { mutableStateOf<Pair<Boolean, String>?>(null) }  // ok, text
    var lastCode by remember { mutableStateOf("") }
    var lastRequest by remember { mutableStateOf<JSONObject?>(null) }

    fun hostKeyId(req: JSONObject) = "${req.getString("host")}:${req.optInt("sshPort").takeIf { it > 0 } ?: 22}"

    fun start(req: JSONObject) {
        lastRequest = req
        showProgress = true
        running = true
        result = null
        lastCode = ""
        steps.clear()
        log.clear()
        req.put("knownHostKey", KnownServerKeys.get(context, hostKeyId(req)))
        val listener = object : ProvisionListener {
            override fun onStep(step: String, state: String) {
                main.post {
                    steps[step] = when (state) {
                        "active" -> StepState.ACTIVE
                        "done" -> StepState.DONE
                        else -> StepState.FAILED
                    }
                }
            }
            override fun onLog(line: String) {
                main.post { log.add(line) }
            }
        }
        scope.launch {
            val reply = withContext(Dispatchers.IO) {
                runCatching { JSONObject(Mobile.provisionServer(req.toString(), listener)) }
                    .getOrElse { JSONObject().put("ok", false).put("code", "failed").put("detail", it.message ?: "") }
            }
            running = false
            if (reply.optBoolean("ok")) {
                reply.optString("hostKey").takeIf { it.isNotEmpty() }?.let { KnownServerKeys.put(context, hostKeyId(req), it) }
                val isNew = onConfig(reply.getString("yaml"))
                val ms = reply.optLong("latencyMs")
                val key = when {
                    !isNew -> "server_ok_dup"
                    reply.optBoolean("existing") -> "server_ok_existing"
                    else -> "server_ok_new"
                }
                result = true to I18n.t(key).replace("{ms}", ms.toString())
            } else {
                lastCode = reply.optString("code")
                result = false to provisionErrorText(lastCode, reply.optString("detail"), req.optInt("port").takeIf { it > 0 } ?: 8443)
            }
        }
    }

    fun dismiss() {
        if (running) Mobile.cancelProvision()
        onDismiss()
    }

    val shape = RoundedCornerShape(24.dp)
    Dialog(onDismissRequest = { dismiss() }, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(
            modifier = Modifier
                .fillMaxWidth(0.94f)
                .clip(shape)
                .background(SurfaceHigh)
                .border(1.dp, SurfaceOutline.copy(alpha = 0.6f), shape)
                .padding(20.dp),
            verticalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = { dismiss() }) {
                    androidx.compose.foundation.Image(
                        painter = painterResource(R.drawable.ic_back_arrow),
                        contentDescription = null,
                        colorFilter = ColorFilter.tint(TextPrimary),
                        modifier = Modifier.size(20.dp),
                    )
                }
                Text(I18n.t("server_title"), color = TextPrimary, fontSize = 20.sp, fontWeight = FontWeight.SemiBold)
            }

            Column(
                modifier = Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(14.dp),
            ) {
                if (!showProgress) {
                    Text(I18n.t("server_intro"), color = TextSecondary, fontSize = 13.5.sp, lineHeight = 20.sp)
                    SetupField(I18n.t("server_host"), host, { host = it }, "203.0.113.10", KeyboardType.Uri)
                    Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        SetupField(I18n.t("server_user"), user, { user = it }, "root", KeyboardType.Ascii, Modifier.weight(1f))
                        SetupField(I18n.t("server_password"), password, { password = it }, "", KeyboardType.Password, Modifier.weight(1f), secret = true)
                    }
                    SetupField(I18n.t("server_domain"), domain, { domain = it }, "vpn.example.com", KeyboardType.Uri)
                    Text(I18n.t("server_domain_hint"), color = TextMuted, fontSize = 12.sp, lineHeight = 17.sp)
                    Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        SetupField(I18n.t("server_port"), port, { port = it.filter(Char::isDigit) }, "8443", KeyboardType.Number, Modifier.weight(1f))
                        SetupField(I18n.t("server_ssh_port"), sshPort, { sshPort = it.filter(Char::isDigit) }, "22", KeyboardType.Number, Modifier.weight(1f))
                    }
                    formError?.let { ErrorBox(it) }
                    PrimaryButton(I18n.t("server_start")) {
                        val missing = buildList {
                            if (host.isBlank()) add(I18n.t("server_host"))
                            if (password.isEmpty()) add(I18n.t("server_password"))
                            if (domain.isBlank()) add(I18n.t("server_domain"))
                        }
                        if (missing.isNotEmpty()) {
                            formError = I18n.t("server_need").replace("{fields}", missing.joinToString(", "))
                            return@PrimaryButton
                        }
                        formError = null
                        start(JSONObject().apply {
                            put("host", host.trim())
                            put("user", user.trim())
                            put("password", password)
                            put("domain", domain.trim())
                            put("port", port.toIntOrNull() ?: 0)
                            put("sshPort", sshPort.toIntOrNull() ?: 0)
                        })
                    }
                } else {
                    STEPS.forEach { StepRow(I18n.t("server_step_$it"), steps[it] ?: StepState.PENDING) }
                    result?.let { (ok, text) ->
                        Box(
                            modifier = Modifier
                                .fillMaxWidth()
                                .clip(RoundedCornerShape(14.dp))
                                .background((if (ok) Success else Danger).copy(alpha = 0.12f))
                                .padding(horizontal = 14.dp, vertical = 12.dp),
                        ) { Text(text, color = if (ok) TextPrimary else Danger, fontSize = 14.sp, lineHeight = 20.sp) }
                    }
                    LogBox(log)
                    Row(
                        horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End),
                        verticalAlignment = Alignment.CenterVertically,
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        val r = result
                        when {
                            running -> TextButton(onClick = { Mobile.cancelProvision() }) { Text(I18n.t("cancel"), color = TextSecondary) }
                            r != null && r.first -> PrimaryButton(I18n.t("server_done"), Modifier.width(140.dp)) { onDismiss() }
                            else -> {
                                TextButton(onClick = { showProgress = false }) { Text(I18n.t("server_edit"), color = TextSecondary) }
                                if (lastCode == "host_key_changed") {
                                    TextButton(onClick = {
                                        lastRequest?.let { KnownServerKeys.forget(context, hostKeyId(it)); start(it) }
                                    }) { Text(I18n.t("server_trust_key"), color = Primary) }
                                } else {
                                    PrimaryButton(I18n.t("server_retry"), Modifier.width(140.dp)) { lastRequest?.let { start(it) } }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

fun provisionErrorText(code: String, detail: String, port: Int): String {
    val field = when (detail) {
        "host" -> I18n.t("server_host"); "user" -> I18n.t("server_user"); "password" -> I18n.t("server_password")
        "domain" -> I18n.t("server_domain"); "port" -> I18n.t("server_port"); "sshPort" -> I18n.t("server_ssh_port")
        else -> detail
    }
    val key = "prov_err_$code"
    val text = I18n.t(key)
    return (if (text == key) I18n.t("prov_err_failed") else text)
        .replace("{detail}", detail).replace("{port}", port.toString()).replace("{field}", field)
}

@Composable
private fun SetupField(
    label: String,
    value: String,
    onChange: (String) -> Unit,
    placeholder: String,
    keyboard: KeyboardType,
    modifier: Modifier = Modifier.fillMaxWidth(),
    secret: Boolean = false,
) {
    Column(modifier = modifier, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(label, color = TextSecondary, fontSize = 13.sp)
        OutlinedTextField(
            value = value,
            onValueChange = onChange,
            placeholder = { Text(placeholder, color = TextMuted) },
            singleLine = true,
            visualTransformation = if (secret) PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
            keyboardOptions = KeyboardOptions(keyboardType = keyboard, autoCorrect = false),
            shape = RoundedCornerShape(16.dp),
            colors = OutlinedTextFieldDefaults.colors(
                focusedTextColor = TextPrimary,
                unfocusedTextColor = TextPrimary,
                focusedBorderColor = Primary,
                unfocusedBorderColor = SurfaceOutline,
                cursorColor = Primary,
            ),
            modifier = Modifier.fillMaxWidth(),
        )
    }
}

@Composable
private fun ErrorBox(text: String) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(14.dp))
            .background(Danger.copy(alpha = 0.1f))
            .padding(horizontal = 14.dp, vertical = 12.dp),
    ) { Text(text, color = Danger, fontSize = 13.5.sp, lineHeight = 19.sp) }
}

@Composable
private fun PrimaryButton(text: String, modifier: Modifier = Modifier.fillMaxWidth(), onClick: () -> Unit) {
    Box(
        modifier = modifier
            .height(50.dp)
            .clip(RoundedCornerShape(16.dp))
            .background(Brush.linearGradient(BrandGradient))
            .clickable(onClick = onClick),
        contentAlignment = Alignment.Center,
    ) { Text(text, color = Color.White, fontSize = 16.sp, fontWeight = FontWeight.SemiBold) }
}

@Composable
private fun StepRow(label: String, state: StepState) {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Box(modifier = Modifier.size(22.dp), contentAlignment = Alignment.Center) {
            when (state) {
                StepState.ACTIVE -> CircularProgressIndicator(color = Primary, trackColor = SurfaceOutline, strokeWidth = 2.dp, modifier = Modifier.size(20.dp))
                StepState.DONE -> Box(
                    Modifier.size(22.dp).clip(CircleShape).background(Success),
                    contentAlignment = Alignment.Center,
                ) { Icon(Icons.Filled.Check, contentDescription = null, tint = Color.White, modifier = Modifier.size(15.dp)) }
                StepState.FAILED -> Box(
                    Modifier.size(22.dp).clip(CircleShape).background(Danger),
                    contentAlignment = Alignment.Center,
                ) { Text("!", color = Color.White, fontSize = 13.sp, fontWeight = FontWeight.Bold) }
                StepState.PENDING -> Box(Modifier.size(22.dp).border(2.dp, SurfaceOutline, CircleShape))
            }
        }
        Text(
            label,
            color = when (state) {
                StepState.PENDING -> TextMuted
                StepState.FAILED -> Danger
                else -> TextPrimary
            },
            fontSize = 15.sp,
        )
    }
}

@Composable
private fun LogBox(lines: List<String>) {
    val scroll = rememberScrollState()
    LaunchedEffect(lines.size) { scroll.animateScrollTo(scroll.maxValue) }
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .height(180.dp)
            .clip(RoundedCornerShape(16.dp))
            .background(Surface)
            .border(1.dp, SurfaceOutline, RoundedCornerShape(16.dp))
            .verticalScroll(scroll)
            .padding(12.dp),
    ) {
        Text(lines.joinToString("\n"), color = TextSecondary, fontSize = 11.sp, lineHeight = 16.sp, fontFamily = FontFamily.Monospace)
    }
}
