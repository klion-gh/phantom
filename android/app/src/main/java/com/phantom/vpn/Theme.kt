package com.phantom.vpn

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

val BgDeep = Color(0xFF07070C)
val BgSurface = Color(0xFF141225)
val BgSurfaceAlt = Color(0xFF1C1934)
val AccentLavender = Color(0xFFA78BFA)
val AccentLavenderBright = Color(0xFFC9B8FF)
val AccentPurpleDeep = Color(0xFF4A3B8C)
val StatusConnected = Color(0xFF4ADE80)
val StatusError = Color(0xFFF87171)
val TextPrimary = Color(0xFFF5F3FF)
val TextSecondary = Color(0xFF9C97B8)

private val PhantomColorScheme = darkColorScheme(
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

// Always dark, regardless of the system setting - the brand identity is dark/purple.
@Composable
fun PhantomTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = PhantomColorScheme, content = content)
}
