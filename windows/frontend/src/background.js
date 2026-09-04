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
  { id: 'waves', name: 'background_waves_label', desc: 'background_waves_desc' },
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

function makeStars() {
  const rng = seeded(42);
  const stars = [];
  for (let i = 0; i < 90; i++) {
    stars.push({
      x: rng(), y: rng(),
      baseRadius: rng() * 1.4 + 0.4,
      phase: rng() * TAU,
      accent: i % 5 === 0,
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

const stars = makeStars();
const meshNodes = makeMeshNodes();
const meteors = makeMeteors();
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

function drawStars(ctx, w, h, t) {
  for (const s of stars) {
    const twinkle = 0.45 + 0.55 * (0.5 + 0.5 * Math.sin(t * TAU * 2 + s.phase));
    const alpha = 0.55 * twinkle;
    const radius = s.baseRadius * twinkle;
    ctx.fillStyle = s.accent
      ? rgba(getComputedColor('--accent'), alpha)
      : `rgba(255, 255, 255, ${alpha})`;
    ctx.beginPath();
    ctx.arc(s.x * w, s.y * h, radius, 0, TAU);
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

function drawWaves(ctx, w, h, t, primary, accent) {
  for (let i = 0; i < 4; i++) {
    const baseY = h * (0.55 + i * 0.11);
    const amp = h * (0.05 - i * 0.008);
    const speed = i + 1;
    const color = i % 2 === 0 ? primary : accent;
    ctx.beginPath();
    for (let x = 0; x <= w; x += w / 40) {
      const u = x / w;
      const y = baseY + Math.sin(u * TAU * (i + 2) + t * TAU * speed) * amp;
      if (x === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.lineTo(w, h);
    ctx.lineTo(0, h);
    ctx.closePath();
    const grad = ctx.createLinearGradient(0, baseY - amp, 0, h);
    grad.addColorStop(0, rgba(color, 0.1));
    grad.addColorStop(1, rgba(color, 0));
    ctx.fillStyle = grad;
    ctx.fill();
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
function getComputedColor(varName) {
  return varName === '--primary' ? cachedPrimary : cachedAccent;
}

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
    case 'stars': return drawStars(ctx, w, h, t);
    case 'mesh': return drawMesh(ctx, w, h, t, primary, accent);
    case 'meteors': return drawMeteors(ctx, w, h, t, primary, accent);
    case 'waves': return drawWaves(ctx, w, h, t, primary, accent);
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
