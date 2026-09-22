package com.phantom.vpn

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * The Маршрутизация page: which traffic belongs in the tunnel, rather than
 * which server carries it.
 *
 * Everything below the master toggle stays on screen and dims when it isn't
 * in effect (smart mode off, or overridden by "Автоматически") rather than
 * disappearing - the point of a switch is to show what it controls, and
 * removing the controls entirely makes the section jump size and leaves the
 * user guessing what turning it on would even do. See [InactiveOverlay].
 *
 * [autoEnabled] is surfaced here (not just on the Конфигурации page where it
 * lives) because it *overrides* this whole page: with whole-device routing on,
 * the site list is inert, and silently letting the user edit a list that has
 * no effect is the kind of thing that reads as a bug.
 */
@Composable
fun RoutingPage(
    configs: List<SavedConfig>,
    autoEnabled: Boolean,
    connected: Boolean,
    health: Map<String, ConfigHealth>,
    onToggleSmart: (Boolean) -> Unit,
    onAddSite: (String) -> Unit,
    onRemoveSite: (String) -> Unit,
    onToggleConfig: (String) -> Unit,
    onOpenPopular: () -> Unit,
    onApplySites: () -> Unit,
) {
    Column(modifier = Modifier.fillMaxSize()) {
        Text(
            I18n.t("routing"),
            color = TextSecondary,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(14.dp))

        // A manually-connected config is a whole-device VPN, exactly like
        // "Выбирать лучшую" - the only difference is who picked the server. It
        // claims the tunnel the same way, so Умный VPN has to go inactive for
        // the same reason: with neither mode driving, any live connection can
        // only be this manual one.
        val blockedByManualConfig = connected && !autoEnabled && !RoutingStore.smartEnabled

        LazyColumn(verticalArrangement = Arrangement.spacedBy(14.dp)) {
            item {
                SmartVpnHeaderTile(
                    enabled = RoutingStore.smartEnabled,
                    overridden = autoEnabled,
                    blockedByConfig = blockedByManualConfig,
                    onToggle = onToggleSmart,
                )
            }

            // Only "Выбирать лучшую" dims these - it genuinely overrides this
            // page. With Умный VPN merely switched off they stay live, so the
            // site list and configs can be set up before turning it on rather
            // than having to switch it on first just to be allowed to edit.
            val inactive = autoEnabled
            item {
                InactiveOverlay(inactive) {
                    Column(verticalArrangement = Arrangement.spacedBy(14.dp)) {
                        PopularResourcesButton(onClick = onOpenPopular)
                        SitesSection(
                            connected = connected,
                            onAddSite = onAddSite,
                            onRemoveSite = onRemoveSite,
                            onApplySites = onApplySites,
                        )
                        ConfigPickerSection(
                            configs = configs,
                            selected = RoutingStore.smartConfigIds,
                            health = health,
                            smartOn = RoutingStore.smartEnabled,
                            onToggleConfig = onToggleConfig,
                        )
                    }
                }
            }
        }
    }
}

/**
 * Dims [content] and, while [inactive], swallows every tap on it - a plain
 * `alpha()` only changes how it looks, taps would still reach the buttons and
 * text fields underneath. The overlay Box sits on top matching the group's
 * size and consumes the gesture itself rather than the individual controls
 * each needing their own `enabled` flag threaded through.
 *
 * Not private: MainActivity's Конфигурации page reuses this for the same
 * reason - a config's manual toggle there means "connect this as a
 * whole-device VPN", which is exactly what "Умный VPN"/"Автоматически"
 * already have the tunnel doing for a different purpose.
 */
@Composable
fun InactiveOverlay(inactive: Boolean, modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    Box(modifier = modifier.alpha(if (inactive) 0.4f else 1f)) {
        content()
        if (inactive) {
            Box(
                modifier = Modifier
                    .matchParentSize()
                    .pointerInput(Unit) { detectTapGestures {} },
            )
        }
    }
}

/**
 * The master switch, plus - whenever something else already has the tunnel -
 * the one line explaining why. Two separate things can claim it: "Выбирать
 * лучшую" ([overridden]), or a config connected by hand from the
 * Конфигурации page ([blockedByConfig]) - either way this mode can't also be
 * driving it, so it goes inactive the same way Выбирать лучшую and the config
 * list themselves do elsewhere on this page (see [InactiveOverlay]), instead
 * of the one-off accent-colored text this used to show.
 */
@Composable
private fun SmartVpnHeaderTile(
    enabled: Boolean,
    overridden: Boolean,
    blockedByConfig: Boolean,
    onToggle: (Boolean) -> Unit,
) {
    val inactive = overridden || blockedByConfig
    InactiveOverlay(inactive) {
        SectionTile {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        I18n.t("smart_vpn"),
                        color = TextPrimary,
                        fontSize = 16.sp,
                        fontWeight = FontWeight.SemiBold,
                    )
                    Spacer(Modifier.height(4.dp))
                    Text(
                        when {
                            overridden -> I18n.t("auto_overrides_smart")
                            blockedByConfig -> I18n.t("smart_vpn_blocked_by_config")
                            else -> I18n.t("smart_vpn_hint")
                        },
                        color = TextSecondary,
                        fontSize = 13.sp,
                        lineHeight = 18.sp,
                    )
                }
                Spacer(Modifier.width(12.dp))
                GradientSwitch(
                    checked = enabled,
                    onCheckedChange = onToggle,
                    enabled = !inactive,
                )
            }
        }
    }
}

// Sits above the hand-typed list rather than inside it: picking from the
// catalogue is how most people will fill this in, and typing a domain by hand
// is the fallback for whatever isn't in it.
@Composable
private fun PopularResourcesButton(onClick: () -> Unit) {
    val shape = RoundedCornerShape(18.dp)
    Tile(
        modifier = Modifier.fillMaxWidth().clickable(onClick = onClick),
        color = Surface,
        shape = shape,
        borderColor = SurfaceOutline,
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 16.dp),
        ) {
            Text(
                I18n.t("popular_resources"),
                color = TextPrimary,
                fontSize = 15.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            Text("›", color = TextSecondary, fontSize = 20.sp, fontWeight = FontWeight.Bold)
        }
    }
}

@Composable
private fun SitesSection(
    connected: Boolean,
    onAddSite: (String) -> Unit,
    onRemoveSite: (String) -> Unit,
    onApplySites: () -> Unit,
) {
    var draft by remember { mutableStateOf("") }

    SectionTile {
        Text(
            I18n.t("smart_vpn_sites"),
            color = TextPrimary,
            fontSize = 15.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(Modifier.height(10.dp))

        Row(verticalAlignment = Alignment.CenterVertically) {
            OutlinedTextField(
                value = draft,
                onValueChange = { draft = it },
                placeholder = { Text(I18n.t("smart_vpn_site_ph"), color = TextMuted, fontSize = 14.sp) },
                singleLine = true,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                shape = RoundedCornerShape(14.dp),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedTextColor = TextPrimary,
                    unfocusedTextColor = TextPrimary,
                    focusedBorderColor = Primary,
                    unfocusedBorderColor = SurfaceOutline,
                    cursorColor = Primary,
                    focusedContainerColor = SurfaceHigh,
                    unfocusedContainerColor = SurfaceHigh,
                ),
                modifier = Modifier.weight(1f),
            )
            Spacer(Modifier.width(8.dp))
            IconButton(
                onClick = {
                    onAddSite(draft)
                    draft = ""
                },
                enabled = draft.isNotBlank(),
            ) {
                Text(
                    "+",
                    fontSize = 24.sp,
                    color = if (draft.isNotBlank()) Primary else TextMuted,
                )
            }
        }

        if (RoutingStore.sites.isEmpty()) {
            Spacer(Modifier.height(12.dp))
            Text(
                I18n.t("smart_vpn_no_sites"),
                color = TextSecondary,
                fontSize = 14.sp,
                fontWeight = FontWeight.Medium,
                modifier = Modifier.fillMaxWidth(),
                textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(4.dp))
            Text(
                I18n.t("smart_vpn_no_sites_hint"),
                color = TextMuted,
                fontSize = 12.5.sp,
                modifier = Modifier.fillMaxWidth(),
                textAlign = TextAlign.Center,
            )
        } else {
            Spacer(Modifier.height(6.dp))
            // A plain Column, not a nested LazyColumn: this sits inside the
            // page's own LazyColumn, where a second lazy list would fight it
            // for scroll gestures and refuse to measure.
            RoutingStore.sites.forEach { site ->
                SiteRow(pattern = site.pattern, onRemove = { onRemoveSite(site.pattern) })
            }
        }

        // Only worth showing once there's both a live tunnel (nothing to
        // reconnect otherwise) and an actual unapplied edit - shown
        // regardless of whether the edit was an add or the list's last
        // removal, since either one can leave an already-open connection on
        // the wrong path.
        if (connected && RoutingStore.sitesDirty) {
            Spacer(Modifier.height(12.dp))
            SitesApplyRow(onApply = onApplySites)
        }
    }
}

@Composable
private fun SitesApplyRow(onApply: () -> Unit) {
    Column(modifier = Modifier.fillMaxWidth()) {
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .height(1.dp)
                .background(SurfaceOutline.copy(alpha = 0.6f)),
        )
        Spacer(Modifier.height(10.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                I18n.t("smart_vpn_apply_hint"),
                color = TextSecondary,
                fontSize = 12.sp,
                lineHeight = 16.sp,
                modifier = Modifier.weight(1f),
            )
            Spacer(Modifier.width(12.dp))
            Text(
                I18n.t("apply_changes"),
                color = Primary,
                fontSize = 13.5.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier
                    .clip(RoundedCornerShape(12.dp))
                    .clickable(onClick = onApply)
                    .padding(horizontal = 10.dp, vertical = 6.dp),
            )
        }
    }
}

@Composable
private fun SiteRow(pattern: String, onRemove: () -> Unit) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 2.dp),
    ) {
        Text(
            pattern,
            color = TextPrimary,
            fontSize = 14.sp,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f),
        )
        IconButton(onClick = onRemove, modifier = Modifier.size(32.dp)) {
            Text("×", color = TextSecondary, fontSize = 18.sp)
        }
    }
}

@Composable
private fun ConfigPickerSection(
    configs: List<SavedConfig>,
    selected: Set<String>,
    health: Map<String, ConfigHealth>,
    smartOn: Boolean,
    onToggleConfig: (String) -> Unit,
) {
    SectionTile {
        Text(
            I18n.t("smart_vpn_configs"),
            color = TextPrimary,
            fontSize = 15.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(Modifier.height(4.dp))
        Text(
            I18n.t("smart_vpn_configs_hint"),
            color = TextSecondary,
            fontSize = 13.sp,
            lineHeight = 18.sp,
        )
        Spacer(Modifier.height(12.dp))

        if (configs.isEmpty()) {
            Text(
                I18n.t("no_configs_for_routing"),
                color = TextMuted,
                fontSize = 13.sp,
                modifier = Modifier.fillMaxWidth(),
                textAlign = TextAlign.Center,
            )
            return@SectionTile
        }

        configs.forEach { config ->
            ConfigPickRow(
                config = config,
                selected = config.id in selected,
                ping = PingStore[config.id],
                // The selector only decides anything while the mode is on -
                // with it off, a stale "this one is carrying traffic" marker
                // from before it was switched off would be misleading.
                active = smartOn && health[config.id]?.active == true,
                onClick = { onToggleConfig(config.id) },
            )
            Spacer(Modifier.height(8.dp))
        }
    }
}

/** Health of one config as last measured by the smart selector - which one it
 * is carrying traffic through. What the rows *show* as latency is the same
 * [PingStore] value the Конфигурации tiles show, not this. */
data class ConfigHealth(val alive: Boolean, val latencyMs: Long, val probed: Boolean, val active: Boolean)

@Composable
private fun ConfigPickRow(
    config: SavedConfig,
    selected: Boolean,
    ping: PingInfo?,
    active: Boolean,
    onClick: () -> Unit,
) {
    val borderWidth by animateDpAsState(if (selected) 2.dp else 1.dp, tween(200), label = "pickBorderWidth")
    val borderColor by animateColorAsState(if (selected) Primary else SurfaceOutline, tween(200), label = "pickBorderColor")
    val shape = RoundedCornerShape(14.dp)

    val domain = parseYamlField(config.yaml, "domain")
        ?: parseYamlField(config.yaml, "server")
        ?: "—"

    Tile(
        modifier = Modifier.fillMaxWidth().clickable(onClick = onClick),
        color = SurfaceHigh,
        shape = shape,
        borderColor = borderColor,
        borderWidth = borderWidth,
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 12.dp),
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    domain,
                    color = TextPrimary,
                    fontSize = 14.5.sp,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                // Word for word what the Конфигурации tile shows for this
                // config, from the same measurement (see PingStore).
                Spacer(Modifier.height(3.dp))
                Text(
                    ping?.latencyMs?.let { "${I18n.t("ping")}: $it ${I18n.t("ms")}" } ?: "${I18n.t("ping")}: —",
                    color = TextSecondary,
                    fontSize = 12.sp,
                )
            }
            // The active config is the one actually carrying traffic right now -
            // worth distinguishing from "selected", which only means the selector
            // is allowed to choose it.
            if (selected && active) {
                Box(
                    modifier = Modifier
                        .size(width = 26.dp, height = 4.dp)
                        .clip(RoundedCornerShape(100.dp))
                        .background(Brush.linearGradient(BrandGradient)),
                )
            }
        }
    }
}

/** The page's one tile shape, shared by every block on it. */
@Composable
private fun SectionTile(content: @Composable ColumnScope.() -> Unit) {
    val shape = RoundedCornerShape(18.dp)
    Tile(
        modifier = Modifier.fillMaxWidth(),
        color = Surface,
        shape = shape,
        borderColor = SurfaceOutline.copy(alpha = 0.6f),
    ) {
        Column(modifier = Modifier.fillMaxWidth().padding(16.dp), content = content)
    }
}
