// Structured diagnostic logging for the frontend half of the app, routed into
// the same phantom.log the Go side writes (via the UIDiag binding) so one file
// holds the whole picture. Same `cat=X ev=Y k=v ...` shape as Android's
// Diag.kt and windows/diag.go, so all three are greppable the same way:
//
//     findstr "cat=BG" phantom.log
//
// This exists because everything the UI actually does lives in the WebView,
// which has no console the user can open - without routing it out, half the
// app would be the only part with no trace in the log at all.

const ENABLED = true;

export const Cat = {
  APP: 'APP',
  BG: 'BG',
  UI: 'UI',
  VPN: 'VPN',
  ROUTE: 'ROUTE',
};

function fieldsToString(fields) {
  if (!fields) return '';
  return Object.entries(fields)
    .map(([k, v]) => `${k}=${v === null || v === undefined ? 'null' : v}`)
    .join(' ');
}

/** One structured line. Never throws - a diagnostic that can break the app is
 *  worse than no diagnostic, and this runs on paths as hot as section
 *  switching. */
export function diag(category, event, fields) {
  if (!ENABLED) return;
  try {
    window.go.main.App.UIDiag(category, event, fieldsToString(fields));
  } catch (_) {
    // binding not ready yet (very early startup) - drop it rather than
    // interfering with whatever is actually starting up.
  }
}

const lastEmit = new Map();

/** For call sites that can fire every frame or every poll tick: drops
 *  everything but roughly one line per `everyMs` per `key`, so a 4s status
 *  poll or a rAF-driven redraw doesn't bury the rest of the log. */
export function diagSampled(key, category, event, fieldsFn, everyMs = 2000) {
  if (!ENABLED) return;
  const now = Date.now();
  const last = lastEmit.get(key);
  if (last !== undefined && now - last < everyMs) return;
  lastEmit.set(key, now);
  diag(category, event, fieldsFn());
}

const lastValue = new Map();

/** For state that's polled but rarely changes (connection status, config
 *  health): writes a line only when the fields differ from the last one
 *  written under `key`. A poll every few seconds otherwise fills the log with
 *  identical lines that bury the one that actually changed. */
export function diagOnChange(key, category, event, fields) {
  if (!ENABLED) return;
  const serialized = fieldsToString(fields);
  if (lastValue.get(key) === serialized) return;
  lastValue.set(key, serialized);
  diag(category, event, fields);
}

/** Dumped once at startup. The CSS capability probes are the point: which
 *  features a given WebView2 actually supports is exactly what a "it renders
 *  wrong here" report needs, and it's unanswerable after the fact. */
export function logEnvironment() {
  if (!ENABLED) return;
  const supports = (prop, value) => {
    try {
      return CSS.supports(prop, value);
    } catch (_) {
      return 'probe-failed';
    }
  };
  diag(Cat.APP, 'environment', {
    ua: (navigator.userAgent || '').replace(/\s+/g, '_'),
    dpr: window.devicePixelRatio,
    viewport: `${window.innerWidth}x${window.innerHeight}`,
    backdropFilter: supports('backdrop-filter', 'blur(10px)'),
    webkitBackdropFilter: supports('-webkit-backdrop-filter', 'blur(10px)'),
    maskComposite: supports('mask-composite', 'exclude'),
    reducedMotion: window.matchMedia('(prefers-reduced-motion: reduce)').matches,
  });
}

