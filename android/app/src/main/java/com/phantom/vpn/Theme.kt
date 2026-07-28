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
 * PINK is the original and stays the default, so nobody's app changes appearance
 * because this feature was added.
 */
enum class Accent(val stops: List<Color>) {
    PINK(listOf(Color(0xFFA78BFA), Color(0xFFF472B6), Color(0xFF7DD3FC))),
    GREEN(listOf(Color(0xFF34D399), Color(0xFF4ADE80), Color(0xFFBEF264))),
    BLUE(listOf(Color(0xFF60A5FA), Color(0xFF38BDF8), Color(0xFF22D3EE))),
    RED(listOf(Color(0xFFEF4444), Color(0xFFF87171), Color(0xFFFB923C))),
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

val BgDeep: Color get() = pick(0xFF07070C, 0xFFF4F3FA)
val BgSurface: Color get() = pick(0xFF141225, 0xFFFFFFFF)
val BgSurfaceAlt: Color get() = pick(0xFF1C1934, 0xFFEBE9F5)
val AccentLavender: Color get() = pick(0xFFA78BFA, 0xFF7C3AED)
val AccentLavenderBright: Color get() = pick(0xFFC9B8FF, 0xFF9061F9)
val AccentPurpleDeep: Color get() = pick(0xFF4A3B8C, 0xFFDDD6FE)
val StatusConnected: Color get() = pick(0xFF4ADE80, 0xFF15803D)
val StatusError: Color get() = pick(0xFFF87171, 0xFFDC2626)
val TextPrimary: Color get() = pick(0xFFF5F3FF, 0xFF17162B)
val TextSecondary: Color get() = pick(0xFF9C97B8, 0xFF5F5B79)

/** The accent gradient's stops - see [Accent]. */
val AccentGradient: List<Color> get() = Appearance.accent.stops

@Composable
fun PhantomTheme(content: @Composable () -> Unit) {
    val scheme = if (Appearance.isDark) {
        darkColorScheme(
            primary = AccentLavender,
            onPrimary = BgDeep,
            secondary = AccentPurpleDeep,
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
            primary = AccentLavender,
            // White on the light theme's purple, not the near-black background -
            // dark-on-purple is unreadable at button contrast.
            onPrimary = Color.White,
            secondary = AccentPurpleDeep,
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
