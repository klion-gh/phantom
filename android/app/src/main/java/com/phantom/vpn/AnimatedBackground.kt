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
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.blur
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.translate
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInRoot
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
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

private class Star(
    val angle0: Float,
    val radiusFrac: Float,
    val speed: Int,
    val direction: Int,
    val trailArc: Float,
    val birthT: Float,
    val lifeLen: Float,
    val baseRadius: Float,
    val accent: Boolean,
)
private class MeshNode(val baseX: Float, val baseY: Float, val ampX: Float, val ampY: Float, val fx: Int, val fy: Int, val phase: Float)
private class Meteor(val speed: Int, val length: Float, val lane: Float, val offset: Float)
private class Ember(val speed: Int, val sway: Float, val swayFreq: Int, val radius: Float, val lane: Float, val offset: Float, val accent: Boolean)
private class MatrixColumn(val lane: Float, val speed: Int, val offset: Float, val glyphs: List<Boolean>)
private class GalaxyStar(val armPhase: Float, val radiusFrac: Float, val speed: Int, val size: Float, val accent: Boolean)

// Long-exposure star-trail photo: every star circles the same fixed pole
// point at a constant angular speed (an integer number of full turns per 60s
// cycle, same seamless-loop rule as everything else here - see drawStars),
// tracing a fading arc behind it rather than sitting still. birthT/lifeLen
// give each star its own window within the cycle to fade in, hold, and fade
// out - not literal randomness (which would break the loop), but with 64
// stars on staggered windows the repeat isn't perceptible. Ported from the
// Windows client's identical background.js redesign.
private fun makeStars(): List<Star> {
    val rng = seeded(42)
    return List(64) { i ->
        Star(
            angle0 = rng() * TAU,
            radiusFrac = 0.15f + rng() * 0.85f,
            speed = 1 + (rng() * 3).toInt(),
            direction = if (rng() < 0.5f) 1 else -1,
            trailArc = 0.3f + rng() * 0.35f,
            birthT = rng(),
            lifeLen = 0.25f + rng() * 0.5f,
            baseRadius = rng() * 1.3f + 0.5f,
            accent = i % 6 == 0,
        )
    }
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
private fun makeMatrixColumns(): List<MatrixColumn> {
    val rng = seeded(2027)
    return List(52) {
        MatrixColumn(
            lane = rng(),
            speed = 1 + (rng() * 3).toInt(),
            offset = rng(),
            glyphs = List(18) { rng() < 0.5f },
        )
    }
}
private fun makeGalaxyStars(): List<GalaxyStar> {
    val rng = seeded(555)
    return List(150) { i ->
        GalaxyStar(
            armPhase = rng() * TAU,
            radiusFrac = rng(),
            speed = 1 + (rng() * 2).toInt(),
            size = rng() * 1.6f + 0.5f,
            accent = i % 4 == 0,
        )
    }
}

// Computed once for the process, not per-composition: the layouts are fixed
// and identical for every Canvas that draws them (the real backdrop and the
// settings screen's thumbnails alike), so there is nothing per-instance to
// remember.
private val sharedStars = makeStars()
private val sharedMeshNodes = makeMeshNodes()
private val sharedMeteors = makeMeteors()
private val sharedEmbers = makeEmbers()
private val sharedMatrixColumns = makeMatrixColumns()
private val sharedGalaxyStars = makeGalaxyStars()

// The style dispatch, shared by the real backdrop and the settings screen's
// per-variant thumbnails.
private fun DrawScope.drawBackdropContent(
    style: BackgroundStyle,
    w: Float,
    h: Float,
    t: Float,
    primary: Color,
    accent: Color,
    zeroGlyph: TextLayoutResult?,
    oneGlyph: TextLayoutResult?,
) {
    when (style) {
        BackgroundStyle.ORBS -> drawOrbs(w, h, t, primary, accent)
        BackgroundStyle.AURORA -> drawAurora(w, h, t, primary, accent)
        BackgroundStyle.STARS -> drawStars(sharedStars, w, h, t, accent)
        BackgroundStyle.MESH -> drawMesh(sharedMeshNodes, w, h, t, primary, accent)
        BackgroundStyle.METEORS -> drawMeteors(sharedMeteors, w, h, t, primary, accent)
        BackgroundStyle.WAVES -> drawWaves(w, h, t, primary, accent)
        BackgroundStyle.EMBERS -> drawEmbers(sharedEmbers, w, h, t, primary, accent)
        BackgroundStyle.MATRIX ->
            if (zeroGlyph != null && oneGlyph != null) {
                drawMatrix(sharedMatrixColumns, w, h, t, zeroGlyph, oneGlyph, accent)
            }
        BackgroundStyle.GALAXY -> drawGalaxy(sharedGalaxyStars, w, h, t, primary, accent)
        BackgroundStyle.PLAIN -> Unit
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
    val s = kotlin.math.max(w, h)
    // Off the top edge, like most real polar star-trail photos - only the
    // lower arcs of each circle sweep through the visible frame instead of
    // full rings centred on screen.
    val poleX = w * 0.5f
    val poleY = h * -0.15f
    val segments = 18
    for (star in stars) {
        var lifeT = t - star.birthT
        if (lifeT < 0f) lifeT += 1f
        if (lifeT > star.lifeLen) continue
        val lifeFrac = lifeT / star.lifeLen
        val fadeIn = (lifeFrac / 0.2f).coerceAtMost(1f)
        val fadeOut = ((1f - lifeFrac) / 0.2f).coerceAtMost(1f)
        val lifeAlpha = min(fadeIn, fadeOut)
        if (lifeAlpha <= 0.01f) continue

        val radius = star.radiusFrac * s
        val angle = star.angle0 + star.direction * star.speed * t * TAU
        val color = if (star.accent) accent else Color.White

        for (i in 0 until segments) {
            val a0 = angle - star.direction * (i.toFloat() / segments) * star.trailArc
            val a1 = angle - star.direction * ((i + 1).toFloat() / segments) * star.trailArc
            val segAlpha = lifeAlpha * 0.5f * (1f - i.toFloat() / segments)
            if (segAlpha <= 0.01f) continue
            drawArc(
                color = color.copy(alpha = segAlpha),
                startAngle = Math.toDegrees(min(a0, a1).toDouble()).toFloat(),
                sweepAngle = Math.toDegrees((kotlin.math.abs(a1 - a0)).toDouble()).toFloat(),
                useCenter = false,
                topLeft = Offset(poleX - radius, poleY - radius),
                size = Size(radius * 2, radius * 2),
                style = Stroke(width = star.baseRadius * 0.9f),
            )
        }

        val headAlpha = lifeAlpha * 0.9f
        drawCircle(
            color = color.copy(alpha = headAlpha),
            radius = star.baseRadius,
            center = Offset(poleX + cos(angle) * radius, poleY + sin(angle) * radius),
        )
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

// The classic falling-code rain: each lane is a trail of pre-measured "0"/"1"
// glyphs (measured once, outside this loop - see BackgroundCanvas - re-measuring
// per glyph per frame is what would actually be expensive, not the draw call
// itself) scrolling downward and wrapping, brightest at the head and fading up
// the trail. Uses the palette's own primary/accent rather than a hardcoded
// green, same as every other variant here, so it stays "this app's Matrix
// background" across all six palettes instead of a green rectangle that fights
// whichever palette is active.
private fun DrawScope.drawMatrix(
    columns: List<MatrixColumn>,
    w: Float,
    h: Float,
    t: Float,
    zeroGlyph: androidx.compose.ui.text.TextLayoutResult,
    oneGlyph: androidx.compose.ui.text.TextLayoutResult,
    accent: Color,
) {
    val trailLen = columns.firstOrNull()?.glyphs?.size ?: 18
    val glyphH = h * 0.03f
    val trailPx = trailLen * glyphH
    for (col in columns) {
        val progress = (t * col.speed + col.offset).mod(1f)
        // Head travels from trailPx above the screen to trailPx *past* the
        // bottom edge - not just to the bottom edge itself - so the tail (a
        // full trailPx behind the head) has completely scrolled off before
        // the column wraps, instead of vanishing mid-scroll the moment the
        // head alone touches the bottom.
        val headY = progress * (h + 2f * trailPx) - trailPx
        val x = col.lane * w
        for (i in 0 until trailLen) {
            val y = headY - i * glyphH
            if (y < -glyphH || y > h) continue
            val fade = (1f - i.toFloat() / trailLen).coerceIn(0f, 1f)
            val alpha = fade * fade * 0.8f
            if (alpha < 0.02f) continue
            val glyph = if (col.glyphs[i]) oneGlyph else zeroGlyph
            val color = if (i == 0) Color.White else accent
            drawText(glyph, color = color, alpha = alpha, topLeft = Offset(x, y))
        }
    }
}

// A rotating spiral galaxy: a soft core glow plus several hundred stars laid
// out along logarithmic-ish spiral arms, orbiting at an integer multiple of
// the 60s cycle each (the same "whole number of periods" rule every other
// variant follows) so the frame at the seam matches exactly.
private fun DrawScope.drawGalaxy(stars: List<GalaxyStar>, w: Float, h: Float, t: Float, primary: Color, accent: Color) {
    val cx = w * 0.5f
    val cy = h * 0.44f
    val maxR = min(w, h) * 0.62f
    val armCount = 3
    drawCircle(
        brush = Brush.radialGradient(
            colors = listOf(primary.copy(alpha = 0.22f), primary.copy(alpha = 0f)),
            center = Offset(cx, cy), radius = maxR * 0.4f,
        ),
        radius = maxR * 0.4f, center = Offset(cx, cy),
    )
    for (s in stars) {
        val arm = (s.armPhase / TAU * armCount).toInt() % armCount
        val armOffset = arm * (TAU / armCount)
        val angle = s.armPhase + s.radiusFrac * TAU * 1.6f + t * TAU * s.speed + armOffset
        val r = s.radiusFrac * maxR
        val x = cx + cos(angle) * r
        val y = cy + sin(angle) * r * 0.62f
        val twinkle = 0.5f + 0.5f * sin(t * TAU * 3 + s.armPhase * 5)
        val alpha = ((0.15f + 0.55f * (1f - s.radiusFrac)) * twinkle).coerceIn(0f, 0.85f)
        drawCircle(
            color = (if (s.accent) accent else Color.White).copy(alpha = alpha),
            radius = s.size,
            center = Offset(x, y),
        )
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

/** The app's real backdrop - whatever [Appearance.background] currently is. */
@Composable
fun AnimatedBackground(modifier: Modifier = Modifier) {
    val primary = Primary
    val accent = Accent
    val backdrop = Backdrop
    val reducedMotion = reducedMotionEnabled()
    val style = if (reducedMotion) BackgroundStyle.PLAIN else Appearance.background
    val (zeroGlyph, oneGlyph) = rememberMatrixGlyphs()

    val transition = rememberInfiniteTransition(label = "backdrop")
    val t by transition.animateFloat(
        initialValue = 0f, targetValue = 1f,
        animationSpec = infiniteRepeatable(animation = tween(60_000, easing = LinearEasing), repeatMode = RepeatMode.Restart),
        label = "t",
    )

    // Not sampled - this only fires when the setting actually changes, and
    // "which style was active when it looked wrong" is the first thing any
    // rendering report needs. reducedMotion silently forces PLAIN, which has
    // confused this before, so it's logged explicitly rather than inferred.
    LaunchedEffect(style, reducedMotion) {
        Diag.log(
            Diag.Cat.BG, "style",
            "style" to style,
            "requested" to Appearance.background,
            "reducedMotion" to reducedMotion,
        )
    }

    Canvas(modifier = modifier.background(Brush.linearGradient(backdrop))) {
        Diag.sampled("backdrop", Diag.Cat.BG, "draw", everyMs = 5000) {
            arrayOf("w" to size.width, "h" to size.height, "t" to t, "style" to style)
        }
        drawBackdropContent(style, size.width, size.height, t, primary, accent, zeroGlyph, oneGlyph)
    }
}

/** The "0"/"1" the Matrix variant draws, measured once rather than per glyph
 * per frame - drawText(TextLayoutResult, ...) lets colour/alpha vary per call
 * without re-measuring, so this pair covers every glyph it ever draws. */
@Composable
private fun rememberMatrixGlyphs(): Pair<TextLayoutResult, TextLayoutResult> {
    val textMeasurer = rememberTextMeasurer()
    return remember(textMeasurer) {
        val style = TextStyle(fontFamily = FontFamily.Monospace, fontSize = 13.sp)
        textMeasurer.measure("0", style) to textMeasurer.measure("1", style)
    }
}

/** A small, independent live preview of one specific variant - used by the
 * background list (SettingsScreen), not tied to whichever background is
 * actually active. */
@Composable
fun BackgroundThumbnail(style: BackgroundStyle, modifier: Modifier = Modifier) {
    val primary = Primary
    val accent = Accent
    val backdrop = Backdrop
    val reducedMotion = reducedMotionEnabled()
    val effectiveStyle = if (reducedMotion) BackgroundStyle.PLAIN else style
    val (zeroGlyph, oneGlyph) = rememberMatrixGlyphs()

    val transition = rememberInfiniteTransition(label = "backdropThumb")
    val t by transition.animateFloat(
        initialValue = 0f, targetValue = 1f,
        animationSpec = infiniteRepeatable(animation = tween(60_000, easing = LinearEasing), repeatMode = RepeatMode.Restart),
        label = "t",
    )

    Canvas(modifier = modifier.background(Brush.linearGradient(backdrop))) {
        drawBackdropContent(effectiveStyle, size.width, size.height, t, primary, accent, zeroGlyph, oneGlyph)
    }
}
