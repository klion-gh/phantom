package com.phantom.vpn

import android.content.Context
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.Color

enum class ThemeMode { DARK, LIGHT }

/**
 * The accent gradient used for the "this is on" outline - a connected config tile
 * and a running proxy toggle. Four presets rather than a free colour picker: the
 * gradients are hand-picked three-stop ramps that stay legible against both
 * backgrounds, which an arbitrary colour would not.
 *
 * [solid]/[bright]/[deep] are a single flat colour tracking the same selection,
 * for anywhere the full multi-stop gradient doesn't fit (buttons, focus rings,
 * the connect switch) - every spot that used to be hardcoded purple regardless
 * of the chosen accent now follows it instead. [solid] is always the gradient's
 * own first stop, so the default (PINK) is pixel-identical to the old fixed
 * lavender it replaces.
 *
 * PINK is the original and stays the default, so nobody's app changes appearance
 * because this feature was added.
 */
enum class Accent(val stops: List<Color>, val solid: Color, val bright: Color, val deep: Color) {
    PINK(
        listOf(Color(0xFFA78BFA), Color(0xFFF472B6), Color(0xFF7DD3FC)),
        solid = Color(0xFFA78BFA), bright = Color(0xFFC9B8FF), deep = Color(0xFF4A3B8C),
    ),
    GREEN(
        listOf(Color(0xFF34D399), Color(0xFF4ADE80), Color(0xFFBEF264)),
        solid = Color(0xFF34D399), bright = Color(0xFF6EE7B7), deep = Color(0xFF065F46),
    ),
    BLUE(
        listOf(Color(0xFF60A5FA), Color(0xFF38BDF8), Color(0xFF22D3EE)),
        solid = Color(0xFF38BDF8), bright = Color(0xFF7DD3FC), deep = Color(0xFF075985),
    ),
    RED(
        listOf(Color(0xFFEF4444), Color(0xFFF87171), Color(0xFFFB923C)),
        solid = Color(0xFFF87171), bright = Color(0xFFFCA5A5), deep = Color(0xFF7F1D1D),
    ),
}

/**
 * User-chosen look: light or dark, and which accent gradient.
 *
 * Both are Compose state, so every composable that reads a colour below
 * recomposes the moment either changes - the same mechanism [I18n] uses for the
 * language toggle. Persisted in the same plain SharedPreferences file, since
 * neither is sensitive.
 */
object Appearance {
    private const val PREFS = "phantom_settings"
    private const val THEME_KEY = "theme"
    private const val ACCENT_KEY = "accent"

    var theme by mutableStateOf(ThemeMode.DARK)
        private set

    var accent by mutableStateOf(Accent.PINK)
        private set

    val isDark: Boolean get() = theme == ThemeMode.DARK

    fun load(context: Context) {
        val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        theme = if (p.getString(THEME_KEY, null) == "light") ThemeMode.LIGHT else ThemeMode.DARK
        accent = runCatching { Accent.valueOf(p.getString(ACCENT_KEY, null) ?: "") }.getOrDefault(Accent.PINK)
    }

    fun setTheme(context: Context, mode: ThemeMode) {
        theme = mode
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString(THEME_KEY, if (mode == ThemeMode.LIGHT) "light" else "dark")
            .apply()
    }

    fun setAccent(context: Context, value: Accent) {
        accent = value
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString(ACCENT_KEY, value.name)
            .apply()
    }
}

// The palette is exposed as computed properties rather than constants so that
// reading any of them inside a composable subscribes it to Appearance.theme -
// switching the theme repaints the app with no other plumbing. Call sites are
// unchanged from when these were plain vals.
private fun pick(dark: Long, light: Long) = Color(if (Appearance.isDark) dark else light)

val BgDeep: Color get() = pick(0xFF0A0A0A, 0xFFF4F4F4)
val BgSurface: Color get() = pick(0xFF161616, 0xFFFFFFFF)
val BgSurfaceAlt: Color get() = pick(0xFF1E1E1E, 0xFFEBEBEB)
val StatusConnected: Color get() = pick(0xFF4ADE80, 0xFF15803D)
val StatusError: Color get() = pick(0xFFF87171, 0xFFDC2626)
val TextPrimary: Color get() = pick(0xFFF5F5F5, 0xFF18181B)
val TextSecondary: Color get() = pick(0xFF9C9C9C, 0xFF5F5F5F)

/** The accent gradient's stops - see [Accent]. */
val AccentGradient: List<Color> get() = Appearance.accent.stops

/** Single flat colour tracking the selected accent - see [Accent]. */
val AccentSolid: Color get() = Appearance.accent.solid
val AccentSolidBright: Color get() = Appearance.accent.bright
val AccentSolidDeep: Color get() = Appearance.accent.deep

@Composable
fun PhantomTheme(content: @Composable () -> Unit) {
    val scheme = if (Appearance.isDark) {
        darkColorScheme(
            primary = AccentSolid,
            onPrimary = BgDeep,
            secondary = AccentSolidDeep,
            background = BgDeep,
            onBackground = TextPrimary,
            surface = BgSurface,
            onSurface = TextPrimary,
            surfaceVariant = BgSurfaceAlt,
            onSurfaceVariant = TextSecondary,
            error = StatusError,
        )
    } else {
        lightColorScheme(
            primary = AccentSolid,
            // White on the accent colour, not the near-black background -
            // dark-on-accent is unreadable at button contrast.
            onPrimary = Color.White,
            secondary = AccentSolidDeep,
            background = BgDeep,
            onBackground = TextPrimary,
            surface = BgSurface,
            onSurface = TextPrimary,
            surfaceVariant = BgSurfaceAlt,
            onSurfaceVariant = TextSecondary,
            error = StatusError,
        )
    }
    MaterialTheme(colorScheme = scheme, content = content)
}
