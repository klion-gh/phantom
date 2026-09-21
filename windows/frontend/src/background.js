// Ambient animated backdrop behind every screen (see .backdrop/#bg-canvas in
// style.css) - eight variants, switched instantly by data-background on
// <html>, persisted the same way as the palette (see applyAppearance in
// main.js). Everything here is deliberately quiet: every drawn element caps
// out well under full opacity so it never competes with real content.
//
// The one hard rule: a single cycle is exactly 60 seconds and every moving
// piece completes a whole number of periods in it, so the frame at t=1 is
// pixel-identical to the frame at t=0 - no seam, no jump, no visible "reset".
// That's why every frequency below is an integer (1, 2, 3...), never 0.7 or
// 1.3 - different speeds come from different integers or a phase offset, not
// a fractional multiplier.

const TAU = Math.PI * 2;
const CYCLE_SECONDS = 60;

// name/desc are i18n keys (see i18n.js's "background_*" entries), not the
// literal display text - main.js's renderBackgroundList runs them through t().
export const BACKGROUNDS = [
  { id: 'orbs', name: 'background_orbs_label', desc: 'background_orbs_desc' },
  { id: 'aurora', name: 'background_aurora_label', desc: 'background_aurora_desc' },
  { id: 'stars', name: 'background_stars_label', desc: 'background_stars_desc' },
  { id: 'mesh', name: 'background_mesh_label', desc: 'background_mesh_desc' },
  { id: 'meteors', name: 'background_meteors_label', desc: 'background_meteors_desc' },
  { id: 'matrix', name: 'background_matrix_label', desc: 'background_matrix_desc' },
  { id: 'embers', name: 'background_embers_label', desc: 'background_embers_desc' },
  { id: 'plain', name: 'background_plain_label', desc: 'background_plain_desc' },
];

// A small, fast, deterministic PRNG (mulberry32-family) - Math.random() can't
// be seeded, and unseeded particle positions would reshuffle on every single
// frame instead of animating smoothly from a fixed layout.
function seeded(seed) {
  return function () {
    seed |= 0; seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function hexToRgb(hex) {
  const h = hex.trim().replace('#', '');
  return {
    r: parseInt(h.slice(0, 2), 16),
    g: parseInt(h.slice(2, 4), 16),
    b: parseInt(h.slice(4, 6), 16),
  };
}
function rgba(hex, alpha) {
  const { r, g, b } = hexToRgb(hex);
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

// --- Fixed particle layouts, generated once from their own seed so they
// don't reshuffle across palette/background switches or resizes (everything
// is stored in [0,1] canvas-relative units, scaled to pixels at draw time).

// Long-exposure star-trail photo: every star circles the same fixed pole
// point at a constant angular speed (an integer number of full turns per 60s
// cycle, same seamless-loop rule as everything else here), tracing a fading
// arc behind it rather than sitting still. birthT/lifeLen give each star its
// own window within the cycle to fade in, hold, and fade out - not literal
// randomness (which would break the loop), but with 64 stars on staggered
// windows the repeat isn't perceptible.
function makeStars() {
  const rng = seeded(42);
  const stars = [];
  for (let i = 0; i < 64; i++) {
    stars.push({
      angle0: rng() * TAU,
      radiusFrac: 0.15 + rng() * 0.85,
      speed: 1 + Math.floor(rng() * 3), // integer turns per cycle
      direction: rng() < 0.5 ? 1 : -1,
      trailArc: 0.3 + rng() * 0.35, // radians of visible trail behind the head
      birthT: rng(),
      lifeLen: 0.25 + rng() * 0.5,
      baseRadius: rng() * 1.3 + 0.5,
      accent: i % 6 === 0,
    });
  }
  return stars;
}

function makeMeshNodes() {
  const rng = seeded(7);
  const nodes = [];
  for (let i = 0; i < 26; i++) {
    nodes.push({
      baseX: rng(), baseY: rng(),
      ampX: rng() * 0.05 + 0.04,
      ampY: rng() * 0.05 + 0.04,
      fx: rng() < 0.5 ? 1 : 2,
      fy: rng() < 0.5 ? 1 : 2,
      phase: rng() * TAU,
    });
  }
  return nodes;
}

function makeMeteors() {
  const rng = seeded(19);
  const meteors = [];
  for (let i = 0; i < 14; i++) {
    meteors.push({
      speed: 1 + Math.floor(rng() * 3), // 1..3
      length: rng() * 0.12 + 0.08, // fraction of S, 0.08..0.20
      lane: rng(),
      offset: rng(),
    });
  }
  return meteors;
}

function makeEmbers() {
  const rng = seeded(101);
  const embers = [];
  for (let i = 0; i < 40; i++) {
    embers.push({
      speed: 1 + Math.floor(rng() * 2), // 1..2
      sway: rng() * 0.04 + 0.02, // 0.02..0.06
      swayFreq: 1 + Math.floor(rng() * 3), // 1..3
      radius: rng() * 1.8 + 0.8, // 0.8..2.6
      lane: rng(),
      offset: rng(),
      accent: i % 3 === 0,
    });
  }
  return embers;
}

// The classic falling-code rain - same design as the Android client's
// "Матрица" (AnimatedBackground.kt's drawMatrix): dense columns of "0"/"1"
// scrolling down and wrapping, brightest at the head and fading up the
// trail, in the active palette's own accent colour rather than a hardcoded
// green so it stays "this app's Matrix" across every palette.
function makeMatrixColumns() {
  const rng = seeded(2027);
  const columns = [];
  for (let i = 0; i < 52; i++) {
    const glyphs = [];
    for (let j = 0; j < 18; j++) glyphs.push(rng() < 0.5);
    columns.push({
      lane: rng(),
      speed: 1 + Math.floor(rng() * 3),
      offset: rng(),
      glyphs,
    });
  }
  return columns;
}

const stars = makeStars();
const meshNodes = makeMeshNodes();
const meteors = makeMeteors();
const matrixColumns = makeMatrixColumns();
const embers = makeEmbers();

function drawOrbs(ctx, w, h, t, primary, accent) {
  const S = Math.min(w, h);
  const orbs = [
    { r: 0.55, fx: 1, fy: 1, phase: 0.00, color: primary },
    { r: 0.45, fx: 1, fy: 2, phase: 0.35, color: accent },
    { r: 0.35, fx: 2, fy: 1, phase: 0.68, color: primary },
  ];
  for (const o of orbs) {
    const theta = (t + o.phase) * TAU;
    const cx = w * (0.5 + 0.38 * Math.sin(theta * o.fx));
    const cy = h * (0.45 + 0.34 * Math.cos(theta * o.fy));
    const radius = S * o.r;
    const grad = ctx.createRadialGradient(cx, cy, 0, cx, cy, radius);
    grad.addColorStop(0, rgba(o.color, 0.2));
    grad.addColorStop(1, rgba(o.color, 0));
    ctx.fillStyle = grad;
    ctx.beginPath();
    ctx.arc(cx, cy, radius, 0, TAU);
    ctx.fill();
  }
}

function drawAurora(ctx, w, h, t, primary, accent) {
  ctx.save();
  ctx.filter = 'blur(30px)';
  for (let i = 0; i < 3; i++) {
    const phase = t * TAU + i * 1.7;
    const baseY = h * (0.28 + i * 0.22);
    const color = i % 2 === 0 ? primary : accent;
    ctx.beginPath();
    for (let x = 0; x <= w; x += w / 24) {
      const u = x / w;
      const y = baseY
        + Math.sin(u * 3 * Math.PI + phase) * h * 0.06
        + Math.cos(u * TAU - phase * 2) * h * 0.03;
      if (x === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.lineTo(w, h);
    ctx.lineTo(0, h);
    ctx.closePath();
    const grad = ctx.createLinearGradient(0, baseY - h * 0.1, 0, h);
    grad.addColorStop(0, rgba(color, 0.13));
    grad.addColorStop(1, rgba(color, 0));
    ctx.fillStyle = grad;
    ctx.fill();
  }
  ctx.restore();
}

function drawStars(ctx, w, h, t, accent) {
  const S = Math.max(w, h);
  // Off the top edge, like most real polar star-trail photos - only the
  // lower arcs of each circle sweep through the visible frame instead of
  // full rings centred on screen.
  const poleX = w * 0.5;
  const poleY = h * -0.15;
  const segments = 18;
  for (const s of stars) {
    let lifeT = t - s.birthT;
    if (lifeT < 0) lifeT += 1;
    if (lifeT > s.lifeLen) continue;
    const lifeFrac = lifeT / s.lifeLen;
    const fadeIn = Math.min(1, lifeFrac / 0.2);
    const fadeOut = Math.min(1, (1 - lifeFrac) / 0.2);
    const lifeAlpha = Math.min(fadeIn, fadeOut);
    if (lifeAlpha <= 0.01) continue;

    const radius = s.radiusFrac * S;
    const angle = s.angle0 + s.direction * s.speed * t * TAU;
    const color = s.accent ? accent : null;

    ctx.lineWidth = s.baseRadius * 0.9;
    for (let i = 0; i < segments; i++) {
      const a0 = angle - s.direction * (i / segments) * s.trailArc;
      const a1 = angle - s.direction * ((i + 1) / segments) * s.trailArc;
      const segAlpha = lifeAlpha * 0.5 * (1 - i / segments);
      if (segAlpha <= 0.01) continue;
      ctx.strokeStyle = color ? rgba(color, segAlpha) : `rgba(255, 255, 255, ${segAlpha})`;
      ctx.beginPath();
      ctx.arc(poleX, poleY, radius, Math.min(a0, a1), Math.max(a0, a1));
      ctx.stroke();
    }

    const headAlpha = lifeAlpha * 0.9;
    ctx.fillStyle = color ? rgba(color, headAlpha) : `rgba(255, 255, 255, ${headAlpha})`;
    ctx.beginPath();
    ctx.arc(poleX + Math.cos(angle) * radius, poleY + Math.sin(angle) * radius, s.baseRadius, 0, TAU);
    ctx.fill();
  }
}

function drawMesh(ctx, w, h, t, primary, accent) {
  const S = Math.min(w, h);
  const limit = S * 0.22;
  const pts = meshNodes.map((n) => ({
    x: (n.baseX + n.ampX * Math.sin(t * TAU * n.fx + n.phase)) * w,
    y: (n.baseY + n.ampY * Math.cos(t * TAU * n.fy + n.phase)) * h,
  }));
  ctx.lineWidth = 1;
  for (let i = 0; i < pts.length; i++) {
    for (let j = i + 1; j < pts.length; j++) {
      const dx = pts[i].x - pts[j].x;
      const dy = pts[i].y - pts[j].y;
      const d = Math.sqrt(dx * dx + dy * dy);
      if (d >= limit) continue;
      ctx.strokeStyle = rgba(primary, 0.16 * (1 - d / limit));
      ctx.beginPath();
      ctx.moveTo(pts[i].x, pts[i].y);
      ctx.lineTo(pts[j].x, pts[j].y);
      ctx.stroke();
    }
  }
  ctx.fillStyle = rgba(accent, 0.5);
  for (const p of pts) {
    ctx.beginPath();
    ctx.arc(p.x, p.y, 1.6, 0, TAU);
    ctx.fill();
  }
}

function drawMeteors(ctx, w, h, t, primary, accent) {
  const S = Math.min(w, h);
  ctx.lineCap = 'round';
  ctx.lineWidth = 1.6;
  for (const m of meteors) {
    const progress = (t * m.speed + m.offset) % 1;
    const x = (m.lane * 1.4 - 0.2) * w + progress * w * 0.5;
    const y = -0.2 * h + progress * 1.4 * h;
    // Trail direction follows the streak's own motion (derived from the
    // position formula above), not a fixed constant, so the tail always
    // points the right way regardless of aspect ratio.
    const vx = w * 0.5, vy = h * 1.4;
    const vlen = Math.sqrt(vx * vx + vy * vy) || 1;
    const dx = vx / vlen, dy = vy / vlen;
    const length = S * m.length;
    const tx = x - dx * length, ty = y - dy * length;
    const grad = ctx.createLinearGradient(tx, ty, x, y);
    const color = m.lane < 0.5 ? primary : accent;
    grad.addColorStop(0, rgba(color, 0));
    grad.addColorStop(1, rgba(color, 0.45));
    ctx.strokeStyle = grad;
    ctx.beginPath();
    ctx.moveTo(tx, ty);
    ctx.lineTo(x, y);
    ctx.stroke();
  }
}

function drawMatrix(ctx, w, h, t, accent) {
  const trailLen = matrixColumns.length ? matrixColumns[0].glyphs.length : 18;
  const glyphH = h * 0.03;
  const trailPx = trailLen * glyphH;
  ctx.font = `${Math.max(10, glyphH * 0.62)}px "Consolas", "Cascadia Code", monospace`;
  ctx.textBaseline = 'top';
  for (const col of matrixColumns) {
    const progress = (t * col.speed + col.offset) % 1;
    // Head travels from trailPx above the screen to trailPx *past* the bottom
    // edge, not just to the edge itself - so the tail (a full trailPx behind
    // the head) has completely scrolled off before the column wraps, instead
    // of vanishing mid-scroll the moment the head alone touches bottom.
    const headY = progress * (h + 2 * trailPx) - trailPx;
    const x = col.lane * w;
    for (let i = 0; i < trailLen; i++) {
      const y = headY - i * glyphH;
      if (y < -glyphH || y > h) continue;
      const fade = Math.max(0, Math.min(1, 1 - i / trailLen));
      const alpha = fade * fade * 0.8;
      if (alpha < 0.02) continue;
      ctx.fillStyle = i === 0 ? `rgba(255, 255, 255, ${alpha})` : rgba(accent, alpha);
      ctx.fillText(col.glyphs[i] ? '1' : '0', x, y);
    }
  }
}

function drawEmbers(ctx, w, h, t, primary, accent) {
  for (const e of embers) {
    const progress = (t * e.speed + e.offset) % 1;
    const y = (1.1 - progress * 1.2) * h;
    const x = (e.lane + e.sway * Math.sin(progress * TAU * e.swayFreq)) * w;
    const alpha = Math.max(0, 0.5 * Math.sin(progress * Math.PI));
    ctx.fillStyle = rgba(e.accent ? accent : primary, alpha);
    ctx.beginPath();
    ctx.arc(x, y, e.radius, 0, TAU);
    ctx.fill();
  }
}

let cachedPrimary = '#8B7CF6';
let cachedAccent = '#5B8DEF';

export function refreshPaletteColors() {
  const style = getComputedStyle(document.documentElement);
  cachedPrimary = style.getPropertyValue('--primary').trim() || cachedPrimary;
  cachedAccent = style.getPropertyValue('--accent').trim() || cachedAccent;
}

function draw(ctx, w, h, t, kind) {
  ctx.clearRect(0, 0, w, h);
  if (kind === 'plain') return;
  const primary = cachedPrimary, accent = cachedAccent;
  switch (kind) {
    case 'orbs': return drawOrbs(ctx, w, h, t, primary, accent);
    case 'aurora': return drawAurora(ctx, w, h, t, primary, accent);
    case 'stars': return drawStars(ctx, w, h, t, accent);
    case 'mesh': return drawMesh(ctx, w, h, t, primary, accent);
    case 'meteors': return drawMeteors(ctx, w, h, t, primary, accent);
    case 'matrix': return drawMatrix(ctx, w, h, t, accent);
    case 'embers': return drawEmbers(ctx, w, h, t, primary, accent);
    default: return;
  }
}

const reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

// Drives one <canvas> from a size getter and a kind getter, so the same loop
// serves both the single full-screen backdrop (size = viewport, kind = the
// active setting) and the background list's small per-row live thumbnails
// (size = the row's own box, kind = whatever that row represents) - see
// initBackground/initMiniBackground below. Every instance shares the same
// palette colour cache and the same t = (now/1000 % 60) / 60 clock, so a
// thumbnail and the real backdrop are never out of sync with each other.
function startLoop(canvas, getSize, getKind) {
  const ctx = canvas.getContext('2d');

  function resize() {
    const { w, h } = getSize();
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.max(1, Math.round(w * dpr));
    canvas.height = Math.max(1, Math.round(h * dpr));
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  resize();
  window.addEventListener('resize', resize);

  function frame(nowMs) {
    const { w, h } = getSize();
    const kind = reduceMotion ? 'plain' : getKind();
    const t = (nowMs / 1000 % CYCLE_SECONDS) / CYCLE_SECONDS;
    draw(ctx, w, h, t, kind);
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);

  return resize;
}

export function initBackground(canvas) {
  refreshPaletteColors();
  const paletteObserver = new MutationObserver(refreshPaletteColors);
  paletteObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-palette'] });

  startLoop(
    canvas,
    () => ({ w: window.innerWidth, h: window.innerHeight }),
    () => document.documentElement.dataset.background || 'orbs',
  );
}

// A small, independent live preview of one specific variant - used by the
// background list (renderBackgroundList in main.js), not tied to whatever
// the app's actual active background is. Sized off the canvas element's own
// CSS box (via ResizeObserver), not the viewport.
export function initMiniBackground(canvas, kind) {
  const resize = startLoop(
    canvas,
    () => {
      const rect = canvas.getBoundingClientRect();
      return { w: rect.width || 1, h: rect.height || 1 };
    },
    () => kind,
  );
  if (window.ResizeObserver) {
    new ResizeObserver(resize).observe(canvas);
  }
}
