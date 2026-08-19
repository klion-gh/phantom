package com.phantom.vpn

import android.provider.Settings
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.platform.LocalContext
import kotlin.math.cos
import kotlin.math.min
import kotlin.math.sin
import kotlin.math.sqrt

// Ambient animated backdrop behind every screen - eight variants, mirrors the
// Windows client's background.js formula for formula (see that file's header
// comment for the seamless-loop rule this all has to obey: one cycle is
// exactly 60 seconds, and every moving piece completes a whole number of
// periods in it, so the frame at t=1 is pixel-identical to the frame at t=0).
// Everything drawn here caps out well under full opacity so it never competes
// with real content on top of it.

private const val TAU = (Math.PI * 2).toFloat()

// A small, fast, deterministic PRNG (mulberry32-family) - Kotlin has no seeded
// Random.Default equivalent that behaves identically across recompositions,
// and unseeded particle positions would reshuffle on every draw instead of
// animating smoothly from a fixed layout. Int arithmetic already wraps at 32
// bits the same way JS's Math.imul does, so this is a direct port.
private fun seeded(initialSeed: Int): () -> Float {
    var seed = initialSeed
    return {
        seed += 0x6d2b79f5
        var t = seed
        t = (t xor (t ushr 15)) * (t or 1)
        t = (t + ((t xor (t ushr 7)) * (t or 61))) xor t
        val bits = t xor (t ushr 14)
        (bits.toLong() and 0xFFFFFFFFL).toFloat() / 4294967296f
    }
}

private class Star(val x: Float, val y: Float, val baseRadius: Float, val phase: Float, val accent: Boolean)
private class MeshNode(val baseX: Float, val baseY: Float, val ampX: Float, val ampY: Float, val fx: Int, val fy: Int, val phase: Float)
private class Meteor(val speed: Int, val length: Float, val lane: Float, val offset: Float)
private class Ember(val speed: Int, val sway: Float, val swayFreq: Int, val radius: Float, val lane: Float, val offset: Float, val accent: Boolean)

private fun makeStars(): List<Star> {
    val rng = seeded(42)
    return List(90) { i -> Star(rng(), rng(), rng() * 1.4f + 0.4f, rng() * TAU, i % 5 == 0) }
}
private fun makeMeshNodes(): List<MeshNode> {
    val rng = seeded(7)
    return List(26) {
        MeshNode(
            baseX = rng(), baseY = rng(),
            ampX = rng() * 0.05f + 0.04f, ampY = rng() * 0.05f + 0.04f,
            fx = if (rng() < 0.5f) 1 else 2, fy = if (rng() < 0.5f) 1 else 2,
            phase = rng() * TAU,
        )
    }
}
private fun makeMeteors(): List<Meteor> {
    val rng = seeded(19)
    return List(14) { Meteor(1 + (rng() * 3).toInt(), rng() * 0.12f + 0.08f, rng(), rng()) }
}
private fun makeEmbers(): List<Ember> {
    val rng = seeded(101)
    return List(40) { i ->
        Ember(
            speed = 1 + (rng() * 2).toInt(), sway = rng() * 0.04f + 0.02f, swayFreq = 1 + (rng() * 3).toInt(),
            radius = rng() * 1.8f + 0.8f, lane = rng(), offset = rng(), accent = i % 3 == 0,
        )
    }
}

private fun DrawScope.drawOrbs(w: Float, h: Float, t: Float, primary: Color, accent: Color) {
    val s = min(w, h)
    data class Orb(val r: Float, val fx: Float, val fy: Float, val phase: Float, val color: Color)
    val orbs = listOf(
        Orb(0.55f, 1f, 1f, 0.00f, primary),
        Orb(0.45f, 1f, 2f, 0.35f, accent),
        Orb(0.35f, 2f, 1f, 0.68f, primary),
    )
    for (o in orbs) {
        val theta = (t + o.phase) * TAU
        val cx = w * (0.5f + 0.38f * sin(theta * o.fx))
        val cy = h * (0.45f + 0.34f * cos(theta * o.fy))
        val radius = s * o.r
        drawCircle(
            brush = Brush.radialGradient(
                colors = listOf(o.color.copy(alpha = 0.2f), o.color.copy(alpha = 0f)),
                center = Offset(cx, cy), radius = radius,
            ),
            radius = radius, center = Offset(cx, cy),
        )
    }
}

private fun DrawScope.drawAurora(w: Float, h: Float, t: Float, primary: Color, accent: Color) {
    for (i in 0 until 3) {
        val phase = t * TAU + i * 1.7f
        val baseY = h * (0.28f + i * 0.22f)
        val color = if (i % 2 == 0) primary else accent
        val path = androidx.compose.ui.graphics.Path()
        var x = 0f
        var first = true
        while (x <= w) {
            val u = x / w
            val y = baseY +
                sin(u * 3 * Math.PI.toFloat() + phase) * h * 0.06f +
                cos(u * TAU - phase * 2) * h * 0.03f
            if (first) { path.moveTo(x, y); first = false } else path.lineTo(x, y)
            x += w / 24
        }
        path.lineTo(w, h)
        path.lineTo(0f, h)
        path.close()
        drawPath(
            path = path,
            brush = Brush.verticalGradient(
                colors = listOf(color.copy(alpha = 0.13f), color.copy(alpha = 0f)),
                startY = baseY - h * 0.1f, endY = h,
            ),
        )
    }
}

private fun DrawScope.drawStars(stars: List<Star>, w: Float, h: Float, t: Float, accent: Color) {
    for (s in stars) {
        val twinkle = 0.45f + 0.55f * (0.5f + 0.5f * sin(t * TAU * 2 + s.phase))
        val alpha = 0.55f * twinkle
        val radius = s.baseRadius * twinkle
        val color = if (s.accent) accent else Color.White
        drawCircle(color = color.copy(alpha = alpha), radius = radius, center = Offset(s.x * w, s.y * h))
    }
}

private fun DrawScope.drawMesh(nodes: List<MeshNode>, w: Float, h: Float, t: Float, primary: Color, accent: Color) {
    val s = min(w, h)
    val limit = s * 0.22f
    val pts = nodes.map { n ->
        Offset(
            (n.baseX + n.ampX * sin(t * TAU * n.fx + n.phase)) * w,
            (n.baseY + n.ampY * cos(t * TAU * n.fy + n.phase)) * h,
        )
    }
    for (i in pts.indices) {
        for (j in i + 1 until pts.size) {
            val dx = pts[i].x - pts[j].x
            val dy = pts[i].y - pts[j].y
            val d = sqrt(dx * dx + dy * dy)
            if (d >= limit) continue
            drawLine(
                color = primary.copy(alpha = 0.16f * (1 - d / limit)),
                start = pts[i], end = pts[j], strokeWidth = 1f,
            )
        }
    }
    for (p in pts) drawCircle(color = accent.copy(alpha = 0.5f), radius = 1.6f, center = p)
}

private fun DrawScope.drawMeteors(meteors: List<Meteor>, w: Float, h: Float, t: Float, primary: Color, accent: Color) {
    val s = min(w, h)
    for (m in meteors) {
        val progress = (t * m.speed + m.offset).mod(1f)
        val x = (m.lane * 1.4f - 0.2f) * w + progress * w * 0.5f
        val y = -0.2f * h + progress * 1.4f * h
        // Trail direction follows the streak's own motion, not a fixed
        // constant, so it always points the right way regardless of aspect.
        val vx = w * 0.5f; val vy = h * 1.4f
        val vlen = sqrt(vx * vx + vy * vy).let { if (it == 0f) 1f else it }
        val dx = vx / vlen; val dy = vy / vlen
        val length = s * m.length
        val tx = x - dx * length; val ty = y - dy * length
        val color = if (m.lane < 0.5f) primary else accent
        drawLine(
            brush = Brush.linearGradient(
                colors = listOf(color.copy(alpha = 0f), color.copy(alpha = 0.45f)),
                start = Offset(tx, ty), end = Offset(x, y),
            ),
            start = Offset(tx, ty), end = Offset(x, y),
            strokeWidth = 1.6f, cap = StrokeCap.Round,
        )
    }
}

private fun DrawScope.drawWaves(w: Float, h: Float, t: Float, primary: Color, accent: Color) {
    for (i in 0 until 4) {
        val baseY = h * (0.55f + i * 0.11f)
        val amp = h * (0.05f - i * 0.008f)
        val speed = i + 1
        val color = if (i % 2 == 0) primary else accent
        val path = androidx.compose.ui.graphics.Path()
        var x = 0f
        var first = true
        while (x <= w) {
            val u = x / w
            val y = baseY + sin(u * TAU * (i + 2) + t * TAU * speed) * amp
            if (first) { path.moveTo(x, y); first = false } else path.lineTo(x, y)
            x += w / 40
        }
        path.lineTo(w, h)
        path.lineTo(0f, h)
        path.close()
        drawPath(
            path = path,
            brush = Brush.verticalGradient(
                colors = listOf(color.copy(alpha = 0.1f), color.copy(alpha = 0f)),
                startY = baseY - amp, endY = h,
            ),
        )
    }
}

private fun DrawScope.drawEmbers(embers: List<Ember>, w: Float, h: Float, t: Float, primary: Color, accent: Color) {
    for (e in embers) {
        val progress = (t * e.speed + e.offset).mod(1f)
        val y = (1.1f - progress * 1.2f) * h
        val x = (e.lane + e.sway * sin(progress * TAU * e.swayFreq)) * w
        val alpha = (0.5f * sin(progress * Math.PI.toFloat())).coerceAtLeast(0f)
        drawCircle(color = (if (e.accent) accent else primary).copy(alpha = alpha), radius = e.radius, center = Offset(x, y))
    }
}

/** True when the system's "remove animations" accessibility setting is on -
 * Android has no prefers-reduced-motion media query, this is its equivalent. */
@Composable
private fun reducedMotionEnabled(): Boolean {
    val context = LocalContext.current
    return remember {
        Settings.Global.getFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f
    }
}

// The actual drawing, decoupled from Appearance.background so it can also
// drive the background list's small live thumbnails (BackgroundThumbnail),
// each forced to a specific variant regardless of which one is actually
// active - see the class doc on AnimatedBackground/BackgroundThumbnail below.
@Composable
private fun BackgroundCanvas(style: BackgroundStyle, modifier: Modifier = Modifier) {
    val primary = Primary
    val accent = Accent
    val backdrop = Backdrop
    val reducedMotion = reducedMotionEnabled()
    val effectiveStyle = if (reducedMotion) BackgroundStyle.PLAIN else style

    val stars = remember { makeStars() }
    val meshNodes = remember { makeMeshNodes() }
    val meteors = remember { makeMeteors() }
    val embers = remember { makeEmbers() }

    val transition = rememberInfiniteTransition(label = "backdrop")
    val t by transition.animateFloat(
        initialValue = 0f, targetValue = 1f,
        animationSpec = infiniteRepeatable(animation = tween(60_000, easing = LinearEasing), repeatMode = RepeatMode.Restart),
        label = "t",
    )

    Canvas(modifier = modifier.background(Brush.linearGradient(backdrop))) {
        val w = size.width
        val h = size.height
        when (effectiveStyle) {
            BackgroundStyle.ORBS -> drawOrbs(w, h, t, primary, accent)
            BackgroundStyle.AURORA -> drawAurora(w, h, t, primary, accent)
            BackgroundStyle.STARS -> drawStars(stars, w, h, t, accent)
            BackgroundStyle.MESH -> drawMesh(meshNodes, w, h, t, primary, accent)
            BackgroundStyle.METEORS -> drawMeteors(meteors, w, h, t, primary, accent)
            BackgroundStyle.WAVES -> drawWaves(w, h, t, primary, accent)
            BackgroundStyle.EMBERS -> drawEmbers(embers, w, h, t, primary, accent)
            BackgroundStyle.PLAIN -> Unit
        }
    }
}

/** The app's real backdrop - whatever [Appearance.background] currently is. */
@Composable
fun AnimatedBackground(modifier: Modifier = Modifier) {
    BackgroundCanvas(style = Appearance.background, modifier = modifier)
}

/** A small, independent live preview of one specific variant - used by the
 * background list (SettingsScreen), not tied to whichever background is
 * actually active. */
@Composable
fun BackgroundThumbnail(style: BackgroundStyle, modifier: Modifier = Modifier) {
    BackgroundCanvas(style = style, modifier = modifier)
}
