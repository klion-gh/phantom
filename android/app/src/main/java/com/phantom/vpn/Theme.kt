package com.phantom.vpn

import android.content.Context
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.Color

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
    val displayName: String,
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
        displayName = "Полночь",
        bg = Color(0xFF0E0B18), surface = Color(0xFF171327), surfaceHigh = Color(0xFF211C36), surfaceOutline = Color(0xFF2E2748),
        primary = Color(0xFF8B7CF6), primaryDeep = Color(0xFF6D5AE0), accent = Color(0xFF5B8DEF),
        textPrimary = Color(0xFFF2EFFA), textSecondary = Color(0xFF9C93B8), textMuted = Color(0xFF6B6385),
        backdrop = listOf(Color(0xFF1A162E), Color(0xFF0E0B18), Color(0xFF131325)),
    ),
    EMERALD(
        displayName = "Изумруд",
        bg = Color(0xFF07140F), surface = Color(0xFF0F2019), surfaceHigh = Color(0xFF162C23), surfaceOutline = Color(0xFF224034),
        primary = Color(0xFF34D399), primaryDeep = Color(0xFF10B981), accent = Color(0xFF4ECDC4),
        textPrimary = Color(0xFFECFDF5), textSecondary = Color(0xFF8CAFA1), textMuted = Color(0xFF5E7D71),
        backdrop = listOf(Color(0xFF0C271D), Color(0xFF07140F), Color(0xFF0B1F1A)),
    ),
    SUNSET(
        displayName = "Закат",
        bg = Color(0xFF17090C), surface = Color(0xFF261216), surfaceHigh = Color(0xFF33191E), surfaceOutline = Color(0xFF48252C),
        primary = Color(0xFFFF7A59), primaryDeep = Color(0xFFE85D3D), accent = Color(0xFFFFB86C),
        textPrimary = Color(0xFFFFF1EC), textSecondary = Color(0xFFC0968D), textMuted = Color(0xFF8C6A63),
        backdrop = listOf(Color(0xFF2E1414), Color(0xFF17090C), Color(0xFF251412)),
    ),
    OCEAN(
        displayName = "Океан",
        bg = Color(0xFF061320), surface = Color(0xFF0C2033), surfaceHigh = Color(0xFF122C45), surfaceOutline = Color(0xFF1D3F5E),
        primary = Color(0xFF38BDF8), primaryDeep = Color(0xFF0EA5E9), accent = Color(0xFF6EE7B7),
        textPrimary = Color(0xFFECFAFF), textSecondary = Color(0xFF8AA9BF), textMuted = Color(0xFF5C7A88),
        backdrop = listOf(Color(0xFF0B2436), Color(0xFF061320), Color(0xFF0C2029)),
    ),
    GRAPHITE(
        displayName = "Графит",
        bg = Color(0xFF0D0D0F), surface = Color(0xFF17171A), surfaceHigh = Color(0xFF212126), surfaceOutline = Color(0xFF2E2E35),
        primary = Color(0xFFE4E4E7), primaryDeep = Color(0xFFA1A1AA), accent = Color(0xFF7DD3FC),
        textPrimary = Color(0xFFF4F4F5), textSecondary = Color(0xFF9A9AA5), textMuted = Color(0xFF67676F),
        backdrop = listOf(Color(0xFF222225), Color(0xFF0D0D0F), Color(0xFF14191D)),
    ),
    SAKURA(
        displayName = "Сакура",
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
enum class BackgroundStyle(val label: String, val description: String) {
    ORBS("Сферы", "Плавно плывущие пятна света"),
    AURORA("Сияние", "Медленные цветные ленты"),
    STARS("Звёзды", "Мерцающие точки на фоне"),
    MESH("Сеть", "Точки, соединённые тонкими линиями"),
    METEORS("Метеоры", "Редкие росчерки по диагонали"),
    WAVES("Волны", "Слоистые волнистые линии"),
    EMBERS("Искры", "Огоньки, поднимающиеся снизу вверх"),
    PLAIN("Без анимации", "Только фоновый градиент"),
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

    var palette by mutableStateOf(Palette.MIDNIGHT)
        private set

    var background by mutableStateOf(BackgroundStyle.ORBS)
        private set

    fun load(context: Context) {
        val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        palette = runCatching { Palette.valueOf(p.getString(PALETTE_KEY, null) ?: "") }.getOrDefault(Palette.MIDNIGHT)
        background = runCatching { BackgroundStyle.valueOf(p.getString(BACKGROUND_KEY, null) ?: "") }.getOrDefault(BackgroundStyle.ORBS)
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
