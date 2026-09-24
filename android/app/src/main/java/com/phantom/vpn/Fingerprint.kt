package com.phantom.vpn

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * The TLS fingerprint - how a connection to the server introduces itself -
 * picked per config from the edit dialog instead of by hand-editing YAML.
 *
 * Why it's worth a UI of its own: as of June 2026 TSPU freezes connections to
 * datacenter IPs that present a Chrome/Safari ClientHello (and burst), while
 * Firefox and Edge pass (see internal/transport/fingerprint.go). Configs
 * generated before this carry `fingerprint: "chrome133"`, so fixing one means
 * changing that line - and which profile passes can change again, so it has
 * to be switchable without an app update.
 *
 * The choice is written into the config's own YAML (one `fingerprint:` line)
 * rather than stored beside it, so the tunnel, pings and auto-select probes -
 * which all read the YAML - pick it up with no extra plumbing, and the user
 * sees exactly what changed.
 */
enum class FingerprintChoice(val value: String, val label: String) {
    AUTO("auto", "Авто"),
    FIREFOX("firefox", "Firefox"),
    EDGE("edge", "Edge"),
    CHROME("chrome133", "Chrome"),
}

/** Which choice the YAML currently names, or null for one not offered here
 * (360, qq, safari, an older chrome...) - those still work, they're just
 * left as the operator wrote them. A config with no line at all is "auto",
 * the core's default. */
fun currentFingerprint(yaml: String): FingerprintChoice? {
    val raw = parseYamlField(yaml, "fingerprint")?.lowercase()?.trim()
    return when {
        raw.isNullOrEmpty() || raw == "auto" -> FingerprintChoice.AUTO
        raw.startsWith("firefox") -> FingerprintChoice.FIREFOX
        raw == "edge" -> FingerprintChoice.EDGE
        raw == "chrome133" || raw == "chrome" -> FingerprintChoice.CHROME
        else -> null
    }
}

/** The YAML with its fingerprint line set to [choice] - replaced in place if
 * there is one, appended otherwise. */
fun withFingerprint(yaml: String, choice: FingerprintChoice): String {
    val line = "fingerprint: \"${choice.value}\""
    val existing = Regex("""(?m)^[ \t]*fingerprint[ \t]*:.*$""")
    if (existing.containsMatchIn(yaml)) return existing.replaceFirst(yaml, line)
    val base = yaml.trimEnd('\n', '\r', ' ', '\t')
    return if (base.isEmpty()) line else "$base\n$line"
}

@Composable
fun FingerprintPicker(yaml: String, onYamlChange: (String) -> Unit) {
    val current = currentFingerprint(yaml)
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(I18n.t("fingerprint_title"), color = TextPrimary, fontSize = 15.sp, fontWeight = FontWeight.Medium)
        Text(I18n.t("fingerprint_hint"), color = TextSecondary, fontSize = 12.5.sp, lineHeight = 17.sp)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
            FingerprintChoice.entries.forEach { choice ->
                FingerprintTile(
                    label = if (choice == FingerprintChoice.AUTO) I18n.t("fingerprint_auto") else choice.label,
                    selected = choice == current,
                    modifier = Modifier.weight(1f),
                    onClick = { onYamlChange(withFingerprint(yaml, choice)) },
                )
            }
        }
    }
}

// The language tile (LangTile) in a compact size, so four fit across the
// dialog - same fill, radius and animated selected border.
@Composable
private fun FingerprintTile(label: String, selected: Boolean, modifier: Modifier, onClick: () -> Unit) {
    val borderWidth by animateDpAsState(if (selected) 2.dp else 1.dp, tween(200), label = "fpBorderWidth")
    val borderColor by animateColorAsState(if (selected) Primary else SurfaceOutline, tween(200), label = "fpBorderColor")
    Tile(
        contentAlignment = Alignment.Center,
        modifier = modifier.height(42.dp).clickable(onClick = onClick),
        color = Surface,
        shape = RoundedCornerShape(14.dp),
        borderColor = borderColor,
        borderWidth = borderWidth,
    ) {
        Text(
            label,
            color = if (selected) TextPrimary else TextSecondary,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
            maxLines = 1,
        )
    }
}
