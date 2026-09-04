package com.phantom.vpn

import android.content.Context
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp

/**
 * Six interchangeable dark palettes - see PROTOCOL.md's theming section. The
 * palette changes colour only: sizing, radii, spacing and type are identical
 * across all six so the app stays one product regardless of which is picked.
 * There is no light variant - this system is built for a dark, colourful
 * background and breaks if inverted, so unlike the old theme switch this is
 * a straight palette choice, not palette+mode.
 *
 * MIDNIGHT is the original and stays the default, so nobody's app changes
 * appearance because this feature was added.
 */
enum class Palette(
    // I18n key, not the literal name - see I18n.kt's "palette_*" entries.
    val nameKey: String,
    val bg: Color,
    val surface: Color,
    val surfaceHigh: Color,
    val surfaceOutline: Color,
    val primary: Color,
    val primaryDeep: Color,
    val accent: Color,
    val textPrimary: Color,
    val textSecondary: Color,
    val textMuted: Color,
    // The 3-stop 135deg wash behind AnimatedBackground's canvas - see
    // style.css's --backdrop for the same values on Windows.
    val backdrop: List<Color>,
) {
    MIDNIGHT(
        nameKey = "palette_midnight",
        bg = Color(0xFF0E0B18), surface = Color(0xFF171327), surfaceHigh = Color(0xFF211C36), surfaceOutline = Color(0xFF2E2748),
        primary = Color(0xFF8B7CF6), primaryDeep = Color(0xFF6D5AE0), accent = Color(0xFF5B8DEF),
        textPrimary = Color(0xFFF2EFFA), textSecondary = Color(0xFF9C93B8), textMuted = Color(0xFF6B6385),
        backdrop = listOf(Color(0xFF1A162E), Color(0xFF0E0B18), Color(0xFF131325)),
    ),
    EMERALD(
        nameKey = "palette_emerald",
        bg = Color(0xFF07140F), surface = Color(0xFF0F2019), surfaceHigh = Color(0xFF162C23), surfaceOutline = Color(0xFF224034),
        primary = Color(0xFF34D399), primaryDeep = Color(0xFF10B981), accent = Color(0xFF4ECDC4),
        textPrimary = Color(0xFFECFDF5), textSecondary = Color(0xFF8CAFA1), textMuted = Color(0xFF5E7D71),
        backdrop = listOf(Color(0xFF0C271D), Color(0xFF07140F), Color(0xFF0B1F1A)),
    ),
    SUNSET(
        nameKey = "palette_sunset",
        bg = Color(0xFF17090C), surface = Color(0xFF261216), surfaceHigh = Color(0xFF33191E), surfaceOutline = Color(0xFF48252C),
        primary = Color(0xFFFF7A59), primaryDeep = Color(0xFFE85D3D), accent = Color(0xFFFFB86C),
        textPrimary = Color(0xFFFFF1EC), textSecondary = Color(0xFFC0968D), textMuted = Color(0xFF8C6A63),
        backdrop = listOf(Color(0xFF2E1414), Color(0xFF17090C), Color(0xFF251412)),
    ),
    OCEAN(
        nameKey = "palette_ocean",
        bg = Color(0xFF061320), surface = Color(0xFF0C2033), surfaceHigh = Color(0xFF122C45), surfaceOutline = Color(0xFF1D3F5E),
        primary = Color(0xFF38BDF8), primaryDeep = Color(0xFF0EA5E9), accent = Color(0xFF6EE7B7),
        textPrimary = Color(0xFFECFAFF), textSecondary = Color(0xFF8AA9BF), textMuted = Color(0xFF5C7A88),
        backdrop = listOf(Color(0xFF0B2436), Color(0xFF061320), Color(0xFF0C2029)),
    ),
    GRAPHITE(
        nameKey = "palette_graphite",
        bg = Color(0xFF0D0D0F), surface = Color(0xFF17171A), surfaceHigh = Color(0xFF212126), surfaceOutline = Color(0xFF2E2E35),
        primary = Color(0xFFE4E4E7), primaryDeep = Color(0xFFA1A1AA), accent = Color(0xFF7DD3FC),
        textPrimary = Color(0xFFF4F4F5), textSecondary = Color(0xFF9A9AA5), textMuted = Color(0xFF67676F),
        backdrop = listOf(Color(0xFF222225), Color(0xFF0D0D0F), Color(0xFF14191D)),
    ),
    SAKURA(
        nameKey = "palette_sakura",
        bg = Color(0xFF15090F), surface = Color(0xFF23121B), surfaceHigh = Color(0xFF301926), surfaceOutline = Color(0xFF452537),
        primary = Color(0xFFFF8FB1), primaryDeep = Color(0xFFE05C8B), accent = Color(0xFFC084FC),
        textPrimary = Color(0xFFFFEFF5), textSecondary = Color(0xFFC195A8), textMuted = Color(0xFF8E6A79),
        backdrop = listOf(Color(0xFF2C161F), Color(0xFF15090F), Color(0xFF1F101D)),
    ),
}

/**
 * Eight animated-backdrop variants - see AnimatedBackground.kt. "ORBS" is the
 * original/default.
 */
enum class BackgroundStyle(val labelKey: String, val descriptionKey: String) {
    ORBS("background_orbs_label", "background_orbs_desc"),
    AURORA("background_aurora_label", "background_aurora_desc"),
    STARS("background_stars_label", "background_stars_desc"),
    MESH("background_mesh_label", "background_mesh_desc"),
    METEORS("background_meteors_label", "background_meteors_desc"),
    WAVES("background_waves_label", "background_waves_desc"),
    EMBERS("background_embers_label", "background_embers_desc"),
    PLAIN("background_plain_label", "background_plain_desc"),
}

/**
 * User-chosen look: which palette, and which animated backdrop.
 *
 * Both are Compose state, so every composable that reads a colour below
 * recomposes the moment either changes - the same mechanism [I18n] uses for the
 * language toggle. Persisted in the same plain SharedPreferences file, since
 * neither is sensitive.
 */
object Appearance {
    private const val PREFS = "phantom_settings"
    private const val PALETTE_KEY = "palette"
    private const val BACKGROUND_KEY = "background_style"
    private const val SHOW_PROXY_SETTINGS_KEY = "show_proxy_settings"

    var palette by mutableStateOf(Palette.MIDNIGHT)
        private set

    var background by mutableStateOf(BackgroundStyle.ORBS)
        private set

    var showProxySettings by mutableStateOf(true)
        private set

    fun load(context: Context) {
        val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        palette = runCatching { Palette.valueOf(p.getString(PALETTE_KEY, null) ?: "") }.getOrDefault(Palette.MIDNIGHT)
        background = runCatching { BackgroundStyle.valueOf(p.getString(BACKGROUND_KEY, null) ?: "") }.getOrDefault(BackgroundStyle.ORBS)
        showProxySettings = p.getBoolean(SHOW_PROXY_SETTINGS_KEY, true)
    }

    fun setPalette(context: Context, value: Palette) {
        palette = value
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString(PALETTE_KEY, value.name)
            .apply()
    }

    fun setBackground(context: Context, value: BackgroundStyle) {
        background = value
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString(BACKGROUND_KEY, value.name)
            .apply()
    }

    fun setShowProxySettings(context: Context, value: Boolean) {
        showProxySettings = value
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putBoolean(SHOW_PROXY_SETTINGS_KEY, value)
            .apply()
    }
}

// Exposed as computed properties rather than constants so that reading any of
// them inside a composable subscribes it to Appearance.palette - switching the
// palette repaints the app with no other plumbing.
val Bg: Color get() = Appearance.palette.bg
val Surface: Color get() = Appearance.palette.surface
val SurfaceHigh: Color get() = Appearance.palette.surfaceHigh
val SurfaceOutline: Color get() = Appearance.palette.surfaceOutline
val Primary: Color get() = Appearance.palette.primary
val PrimaryDeep: Color get() = Appearance.palette.primaryDeep
val Accent: Color get() = Appearance.palette.accent
val TextPrimary: Color get() = Appearance.palette.textPrimary
val TextSecondary: Color get() = Appearance.palette.textSecondary
val TextMuted: Color get() = Appearance.palette.textMuted
val Backdrop: List<Color> get() = Appearance.palette.backdrop

// Fixed regardless of palette - an error/success that changes colour with the
// theme stops reading as an error/success.
val Danger = Color(0xFFFF5C7A)
val Success = Color(0xFF4ADE80)

/** [Primary] -> [Accent], 135deg - the "this is on" outline/glow. */
val BrandGradient: List<Color> get() = listOf(Primary, Accent)

@Composable
fun PhantomTheme(content: @Composable () -> Unit) {
    val scheme = darkColorScheme(
        primary = Primary,
        onPrimary = Color.White,
        secondary = PrimaryDeep,
        background = Bg,
        onBackground = TextPrimary,
        surface = Surface,
        onSurface = TextPrimary,
        surfaceVariant = SurfaceHigh,
        onSurfaceVariant = TextSecondary,
        error = Danger,
    )
    MaterialTheme(colorScheme = scheme, content = content)
}

/**
 * The app's one on/off switch, used everywhere instead of Material3's own
 * [androidx.compose.material3.Switch]: Material3's SwitchColors only take
 * flat [Color]s, so it can't give the track a gradient fill, and the track
 * shimmering with the active palette's [BrandGradient] while on is exactly
 * what distinguishes "connected"/"on" from a plain neutral toggle elsewhere
 * in the app. The thumb itself stays plain white regardless of state - only
 * the track carries the gradient. Track sizing (44x26, 20dp thumb, 3dp
 * inset) matches the Windows client's .toggle-switch pixel-for-pixel.
 */
@Composable
fun GradientSwitch(
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
) {
    val thumbOffset by animateDpAsState(if (checked) 21.dp else 3.dp, tween(200), label = "gradientSwitchThumb")
    Box(
        modifier = modifier
            .size(width = 44.dp, height = 26.dp)
            .clip(RoundedCornerShape(50))
            .then(
                if (checked) Modifier.background(Brush.linearGradient(BrandGradient))
                else Modifier.background(SurfaceHigh)
            )
            .then(
                if (enabled) Modifier.clickable { onCheckedChange(!checked) } else Modifier
            ),
    ) {
        Box(
            modifier = Modifier
                .offset(x = thumbOffset, y = 3.dp)
                .size(20.dp)
                .clip(CircleShape)
                .background(Color.White),
        )
    }
}
