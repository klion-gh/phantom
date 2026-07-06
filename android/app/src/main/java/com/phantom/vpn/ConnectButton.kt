package com.phantom.vpn

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.dp

/**
 * Circular power button: tap connects when idle/error, tap disconnects when connected,
 * no-op while connecting. `size` defaults to the original full-screen dimensions but is
 * overridable so the same composable fits inside the smaller config info block.
 */
@Composable
fun ConnectButton(
    status: ConnectionStatus,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    size: androidx.compose.ui.unit.Dp = 180.dp,
) {
    val glowSize = size + 52.dp
    val iconSize = size * (60f / 180f)

    Box(modifier = modifier.size(glowSize), contentAlignment = androidx.compose.ui.Alignment.Center) {
        if (status == ConnectionStatus.CONNECTED) {
            Box(
                modifier = Modifier
                    .size(glowSize)
                    .background(
                        Brush.radialGradient(
                            colors = listOf(AccentLavender.copy(alpha = 0.35f), Color.Transparent),
                        ),
                        shape = CircleShape,
                    )
            )
        }

        Box(
            modifier = Modifier
                .size(size)
                .clip(CircleShape)
                .background(brushFor(status))
                .clickable(enabled = status != ConnectionStatus.CONNECTING, onClick = onClick),
            contentAlignment = androidx.compose.ui.Alignment.Center,
        ) {
            if (status == ConnectionStatus.CONNECTING) {
                CircularProgressIndicator(
                    modifier = Modifier.size(size - 16.dp),
                    color = AccentLavenderBright,
                    strokeWidth = 3.dp,
                    trackColor = Color.Transparent,
                )
            }
            PowerGlyph(color = iconColorFor(status), size = iconSize)
        }
    }
}

private fun brushFor(status: ConnectionStatus): Brush = when (status) {
    ConnectionStatus.CONNECTED -> Brush.linearGradient(listOf(AccentLavenderBright, AccentPurpleDeep))
    ConnectionStatus.CONNECTING -> Brush.linearGradient(listOf(BgSurfaceAlt, BgSurfaceAlt))
    ConnectionStatus.ERROR -> Brush.linearGradient(listOf(StatusError.copy(alpha = 0.25f), BgSurface))
    ConnectionStatus.IDLE -> Brush.linearGradient(listOf(BgSurface, BgSurface))
}

private fun iconColorFor(status: ConnectionStatus): Color = when (status) {
    ConnectionStatus.CONNECTED -> BgDeep
    ConnectionStatus.CONNECTING -> TextSecondary
    ConnectionStatus.ERROR -> StatusError
    ConnectionStatus.IDLE -> TextSecondary
}

/** Simple hand-drawn power symbol (arc + vertical line) - avoids pulling in an icon library. */
@Composable
private fun PowerGlyph(color: Color, size: androidx.compose.ui.unit.Dp) {
    androidx.compose.foundation.Canvas(modifier = Modifier.size(size)) {
        val strokeWidth = this.size.minDimension * 0.11f
        val stroke = Stroke(width = strokeWidth, cap = androidx.compose.ui.graphics.StrokeCap.Round)

        // vertical tick
        drawLine(
            color = color,
            start = Offset(this.size.width / 2f, this.size.height * 0.08f),
            end = Offset(this.size.width / 2f, this.size.height * 0.5f),
            strokeWidth = strokeWidth,
            cap = androidx.compose.ui.graphics.StrokeCap.Round,
        )
        // open ring, gap at the top where the tick sits
        val ringPadding = this.size.width * 0.16f
        drawArc(
            color = color,
            startAngle = -55f,
            sweepAngle = 290f,
            useCenter = false,
            topLeft = Offset(ringPadding, ringPadding),
            size = Size(this.size.width - ringPadding * 2, this.size.height - ringPadding * 2),
            style = stroke,
        )
    }
}
