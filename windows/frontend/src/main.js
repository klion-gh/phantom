import './style.css';
import { Connect, Disconnect, Status, ReadLog, ListConfigs, AddConfig, UpdateConfig, DeleteConfig, SetConfigGeo, Ping, ListResources, AddResource, DeleteResource, ListExcludedApps, PickExcludedAppExe, AddExcludedApp, DeleteExcludedApp, ApplyUpdate, StartProxy, StopProxy, GetLanguage, SetLanguage } from '../wailsjs/go/main/App';
import { t, getLang, setLang, applyStaticTranslations } from './i18n.js';

const screens = {
  main: document.getElementById('screen-main'),
  config: document.getElementById('screen-config'),
  settings: document.getElementById('screen-settings'),
  log: document.getElementById('screen-log'),
  splitTunnel: document.getElementById('screen-split-tunnel'),
};

function showScreen(name) {
  for (const key in screens) {
    screens[key].classList.toggle('hidden', key !== name);
  }
}

const configList = document.getElementById('config-list');
const emptyState = document.getElementById('empty-state');
const errorText = document.getElementById('error-text');
const configTextarea = document.getElementById('config-textarea');
const configScreenTitle = document.getElementById('config-screen-title');
const btnDelete = document.getElementById('btn-delete');
const deleteConfirm = document.getElementById('delete-confirm');
const logText = document.getElementById('log-text');
const resourceList = document.getElementById('resource-list');
const updateBanner = document.getElementById('update-banner');
const btnUpdate = document.getElementById('btn-update');
const addResourceOverlay = document.getElementById('add-resource-overlay');
const resourceNameInput = document.getElementById('resource-name-input');
const resourceUrlInput = document.getElementById('resource-url-input');
const excludedAppList = document.getElementById('excluded-app-list');
const excludedAppEmpty = document.getElementById('excluded-app-empty');

let configs = [];
let editingId = null;
let pendingConnectId = null; // id currently mid-Connect() call - drives the spinner state
let currentStatus = { connected: false, alive: false, stats: '{}', activeConfigId: '' };

const pingData = new Map(); // id -> { ip, latencyMs } - just live latency; country/flag
                             // come from the config's own cached fields (see SetConfigGeo)
const pingTimers = new Map(); // id -> interval handle

let resources = [];
const resourceTimers = new Map(); // id -> interval handle

let excludedApps = [];

// Independent per-config SOCKS5 proxy toggle state - id -> {running, port}.
// Unrelated to currentStatus/the full-tunnel VPN: a config can have this on,
// off, connected, disconnected, or any combination, all independently (see
// windows/proxymanager.go). Not polled on a timer like ping/status - it only
// ever changes in response to this app's own StartProxy/StopProxy calls, so
// there's no external drift to detect.
const proxyState = new Map();

function escapeHtml(text) {
  const div = document.createElement('div');
  div.textContent = text;
  return div.innerHTML;
}

function parseYamlField(yaml, key) {
  const quoted = new RegExp(`^\\s*${key}\\s*:\\s*"([^"]*)"\\s*$`, 'm').exec(yaml);
  if (quoted) return quoted[1].trim();
  const bare = new RegExp(`^\\s*${key}\\s*:\\s*(\\S+)\\s*$`, 'm').exec(yaml);
  return bare ? bare[1].trim() : '';
}

// Resolves and persists a config's server IP once (a local DNS + disguised
// handshake via Ping - no third party), plus the optional country/country_code
// the operator put in the client.yaml, then re-renders so the tile picks it up.
// Called after every Add/UpdateConfig.
//
// There is deliberately NO IP->country geo lookup here: that used to hit a
// third-party service (ipwho.is) and a flag CDN (flagcdn.com), which leaked the
// server's IP to those services on a timer. The country label is now purely a
// static field an operator can put in the config they hand out.
async function resolveConfigGeo(id, yaml) {
  try {
    const ping = JSON.parse(await Ping(yaml));
    if (!ping.ip) return;
    const country = parseYamlField(yaml, 'country');
    const countryCode = parseYamlField(yaml, 'country_code');
    await SetConfigGeo(id, ping.ip, country, countryCode);
    await reloadConfigs();
  } catch (e) {
    console.error(e);
  }
}

async function pollPing(config) {
  const prev = pingData.get(config.id) || {};
  try {
    const json = JSON.parse(await Ping(config.yaml));
    pingData.set(config.id, json.ip ? { ip: json.ip, latencyMs: json.latency_ms } : { ...prev, latencyMs: null });
  } catch (e) {
    pingData.set(config.id, { ...prev, latencyMs: null });
  }
  updateTileMeta(config.id);
}

function startPingLoop(config) {
  pollPing(config);
  pingTimers.set(config.id, setInterval(() => pollPing(config), 6000));
}

function stopAllPingLoops() {
  for (const handle of pingTimers.values()) clearInterval(handle);
  pingTimers.clear();
  pingData.clear();
}

function updateTileMeta(id) {
  const card = configList.querySelector(`[data-id="${id}"]`);
  const config = configs.find((c) => c.id === id);
  if (!card || !config) return;

  const info = pingData.get(id) || {};
  card.querySelector('.config-ip').textContent = info.ip || config.ip || parseYamlField(config.yaml, 'server') || '—';
  card.querySelector('.ping-text').textContent = info.latencyMs != null ? `${t('ping')}: ${info.latencyMs} ${t('ms')}` : `${t('ping')}: —`;

  // Country label comes from the operator-provided country/country_code in the
  // config (see resolveConfigGeo) - no third-party geo/flag lookup anymore. The
  // flag <img> stays hidden: a real flag would need a CDN (the leak we removed)
  // or bundled images, and Windows/Chromium can't render flag emoji either, so
  // we show the country name/code as text instead.
  const flagImg = card.querySelector('.geo-flag');
  const geoText = card.querySelector('.geo-text');
  flagImg.classList.add('hidden');
  geoText.textContent = config.country || config.countryCode || '';
}

// Checks one resource tile via a plain fetch() from this page's own network
// stack - which goes through the OS's real routing table, so once the
// Phantom tunnel's 0.0.0.0/0 route is active, this request (like every
// other request on the machine) travels through it too. That's what makes
// "blocked site starts responding once connected" work with no special
// casing: mode:'no-cors' avoids CORS entirely since only success/failure
// and timing matter here, not the response body.
async function checkResource(resource) {
  const card = resourceList.querySelector(`[data-id="${resource.id}"]`);
  if (!card) return;
  const statusEl = card.querySelector('.resource-status');

  const start = performance.now();
  try {
    await fetch(resource.url, { mode: 'no-cors', signal: AbortSignal.timeout(5000) });
    const ms = Math.round(performance.now() - start);
    statusEl.textContent = `${t('available')}, ${ms} ${t('ms')}`;
    statusEl.className = 'resource-status reachable';
  } catch (e) {
    statusEl.textContent = t('unavailable');
    statusEl.className = 'resource-status unreachable';
  }
}

function startResourceLoop(resource) {
  checkResource(resource);
  resourceTimers.set(resource.id, setInterval(() => checkResource(resource), 8000));
}

function stopAllResourceLoops() {
  for (const handle of resourceTimers.values()) clearInterval(handle);
  resourceTimers.clear();
}

// Pausing on document.hidden (rather than only on renderConfigList/renderResourceList's
// own stopAll*Loops) is what makes pinging skip minimized/backgrounded time - checking
// blocked sites or dialing the disguised handshake every few seconds while nobody's
// looking at the window is just wasted traffic. document.hidden reflects the native
// window's occlusion/minimized state in WebView2, not just tab-switching, so this
// covers both a plain taskbar minimize and the tray-hide from App.beforeClose. Unlike
// stopAllPingLoops/stopAllResourceLoops (used when actually rebuilding the tile DOM),
// this never touches pingData/resources - the last-known values stay on screen instead
// of flashing to "—" every time the window is hidden and shown again.
function pauseAllPolling() {
  for (const handle of pingTimers.values()) clearInterval(handle);
  pingTimers.clear();
  for (const handle of resourceTimers.values()) clearInterval(handle);
  resourceTimers.clear();
}

function resumeAllPolling() {
  for (const config of configs) startPingLoop(config);
  for (const resource of resources) startResourceLoop(resource);
}

document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    pauseAllPolling();
  } else {
    resumeAllPolling();
  }
});

function renderResourceList() {
  stopAllResourceLoops();
  resourceList.innerHTML = '';

  for (const resource of resources) {
    const card = document.createElement('div');
    card.className = 'resource-card';
    card.dataset.id = resource.id;
    card.innerHTML = `
      <button class="resource-remove-btn" title="${t('remove')}">&times;</button>
      <div class="resource-name">${escapeHtml(resource.name)}</div>
      <div class="resource-status">${t('checking')}</div>
    `;
    card.querySelector('.resource-remove-btn').addEventListener('click', async () => {
      await DeleteResource(resource.id);
      await reloadResources();
    });
    resourceList.appendChild(card);
    startResourceLoop(resource);
  }
}

async function reloadResources() {
  try {
    resources = JSON.parse(await ListResources());
  } catch (e) {
    resources = [];
  }
  renderResourceList();
}

// Split-tunneling exclusion list - no polling loop here (unlike configs/
// resources), this is just a static list the Go side consults per-connection
// (see windows/splittunnel.go), so rendering is a plain one-shot refresh.
function renderExcludedAppList() {
  excludedAppList.innerHTML = '';
  excludedAppEmpty.classList.toggle('hidden', excludedApps.length > 0);

  for (const app of excludedApps) {
    const card = document.createElement('div');
    card.className = 'resource-card';
    card.dataset.id = app.id;
    card.innerHTML = `
      <button class="resource-remove-btn" title="${t('remove')}">&times;</button>
      <div class="resource-name">${escapeHtml(app.name)}</div>
      <div class="excluded-app-path">${escapeHtml(app.exePath)}</div>
    `;
    card.querySelector('.resource-remove-btn').addEventListener('click', async () => {
      await DeleteExcludedApp(app.id);
      await reloadExcludedApps();
    });
    excludedAppList.appendChild(card);
  }
}

async function reloadExcludedApps() {
  try {
    excludedApps = JSON.parse(await ListExcludedApps());
  } catch (e) {
    excludedApps = [];
  }
  renderExcludedAppList();
}

function renderConfigList() {
  stopAllPingLoops();
  configList.innerHTML = '';
  emptyState.classList.toggle('hidden', configs.length > 0);

  for (const config of configs) {
    const domain = parseYamlField(config.yaml, 'domain') || parseYamlField(config.yaml, 'server') || '—';

    const card = document.createElement('div');
    card.className = 'config-card';
    card.dataset.id = config.id;
    card.innerHTML = `
      <div class="config-info">
        <div class="config-domain">${escapeHtml(domain)}</div>
        <div class="config-ip"></div>
        <div class="config-meta">
          <span class="ping-text">${t('ping')}: —</span>
          <img class="geo-flag hidden" alt="" />
          <span class="geo-text"></span>
        </div>
      </div>
      <button class="config-edit-btn" title="${t('edit')}">&#9881;</button>
      <div class="proxy-block">
        <button class="proxy-toggle" title="${t('proxy_tooltip')}">PROXY</button>
        <input
          class="proxy-port-input"
          type="text"
          inputmode="numeric"
          placeholder="${t('port_ph')}"
          value="${config.proxyPort || ''}"
          title="${t('proxy_port_title')}"
        />
      </div>
      <button class="power-btn idle" title="${t('connect')}">
        <svg viewBox="0 0 24 24" class="power-icon">
          <path d="M12 2v9" stroke-linecap="round" />
          <path d="M6.5 5.5a8 8 0 1 0 11 0" stroke-linecap="round" fill="none" />
        </svg>
        <svg class="spinner" viewBox="0 0 50 50">
          <circle cx="25" cy="25" r="20" fill="none" stroke-width="4" />
        </svg>
      </button>
    `;

    card.querySelector('.config-edit-btn').addEventListener('click', () => openEditScreen(config));
    card.querySelector('.proxy-toggle').addEventListener('click', () => toggleProxy(config));
    card.querySelector('.power-btn').addEventListener('click', () => toggleConnection(config));

    configList.appendChild(card);
    startPingLoop(config);
    refreshProxyToggle(config.id);
  }

  refreshTileStatuses();
}

// Independent SOCKS5 proxy toggle - see the proxyState comment above. Doesn't
// touch currentStatus/toggleConnection's full-tunnel state at all. The port
// field under the toggle is only editable while off (see refreshProxyToggle) -
// its value is read fresh right here at toggle time, so whatever the user
// last typed is exactly what gets requested.
async function toggleProxy(config) {
  const state = proxyState.get(config.id);
  if (state?.running) {
    await StopProxy(config.id);
    proxyState.set(config.id, { running: false });
    refreshProxyToggle(config.id);
    return;
  }

  const card = configList.querySelector(`[data-id="${config.id}"]`);
  const btn = card?.querySelector('.proxy-toggle');
  const portInput = card?.querySelector('.proxy-port-input');

  const rawPort = portInput?.value.trim() || '';
  let requestedPort = 0;
  if (rawPort) {
    requestedPort = Number(rawPort);
    if (!Number.isInteger(requestedPort) || requestedPort < 1 || requestedPort > 65535) {
      errorText.textContent = t('bad_port', { port: rawPort });
      errorText.classList.remove('hidden');
      return;
    }
  }

  if (btn) btn.disabled = true;
  try {
    const resp = JSON.parse(await StartProxy(config.id, config.yaml, requestedPort));
    proxyState.set(config.id, resp);
    if (resp.running) {
      errorText.classList.add('hidden');
      config.proxyPort = resp.port; // keep in sync so a later re-render still shows it
    } else {
      errorText.textContent = t('proxy_failed', { port: rawPort || t('proxy_any'), error: resp.error });
      errorText.classList.remove('hidden');
    }
  } finally {
    if (btn) btn.disabled = false;
    refreshProxyToggle(config.id);
  }
}

function refreshProxyToggle(configId) {
  const card = configList.querySelector(`[data-id="${configId}"]`);
  const btn = card?.querySelector('.proxy-toggle');
  const portInput = card?.querySelector('.proxy-port-input');
  if (!btn || !portInput) return;
  const state = proxyState.get(configId);
  const running = !!state?.running;

  btn.classList.toggle('active', running);
  btn.title = running
    ? t('proxy_tooltip_active', { port: state.port })
    : t('proxy_tooltip');

  portInput.disabled = running;
  if (running) portInput.value = state.port;
}

function refreshTileStatuses() {
  for (const config of configs) {
    const card = configList.querySelector(`[data-id="${config.id}"]`);
    if (!card) continue;
    const btn = card.querySelector('.power-btn');

    let cls = 'idle';
    if (pendingConnectId === config.id) {
      cls = 'connecting';
    } else if (currentStatus.activeConfigId === config.id && currentStatus.connected) {
      cls = currentStatus.alive === false ? 'error' : 'connected';
    }
    btn.className = 'power-btn ' + cls;
    btn.disabled = cls === 'connecting';
    card.classList.toggle('connected', cls === 'connected');
  }
}

async function toggleConnection(config) {
  const isActive = currentStatus.activeConfigId === config.id && currentStatus.connected;

  if (isActive) {
    await Disconnect();
    currentStatus = { connected: false, alive: false, stats: '{}', activeConfigId: '' };
    refreshTileStatuses();
    return;
  }

  if (pendingConnectId) return; // already connecting a different tile

  pendingConnectId = config.id;
  refreshTileStatuses();
  try {
    const err = await Connect(config.id, config.yaml);
    if (err) {
      errorText.textContent = err;
      errorText.classList.remove('hidden');
    } else {
      errorText.classList.add('hidden');
    }
  } catch (e) {
    errorText.textContent = String(e);
    errorText.classList.remove('hidden');
  }
  pendingConnectId = null;
  await refreshStatus();
}

async function refreshStatus() {
  try {
    currentStatus = JSON.parse(await Status());
  } catch (e) {
    console.error(e);
  }
  refreshTileStatuses();
}

async function reloadConfigs() {
  try {
    configs = JSON.parse(await ListConfigs());
  } catch (e) {
    configs = [];
  }
  renderConfigList();
}

function openEditScreen(config) {
  editingId = config.id;
  configTextarea.value = config.yaml;
  configScreenTitle.textContent = t('edit_config_title');
  btnDelete.classList.remove('hidden');
  showScreen('config');
}

document.getElementById('btn-add').addEventListener('click', () => {
  editingId = null;
  configTextarea.value = '';
  configScreenTitle.textContent = t('add_config');
  btnDelete.classList.add('hidden');
  showScreen('config');
});

document.getElementById('btn-back-config').addEventListener('click', () => showScreen('main'));

document.getElementById('btn-save').addEventListener('click', async () => {
  const yaml = configTextarea.value;
  let targetId = editingId;
  if (editingId) {
    await UpdateConfig(editingId, yaml);
  } else {
    targetId = await AddConfig(yaml);
  }
  await reloadConfigs();
  showScreen('main');

  // Resolve IP/country once in the background - the tile shows "—" for
  // location until this lands, then re-renders itself via reloadConfigs.
  if (targetId) {
    resolveConfigGeo(targetId, yaml);
  }
});

btnDelete.addEventListener('click', () => deleteConfirm.classList.remove('hidden'));
document.getElementById('btn-delete-cancel').addEventListener('click', () => deleteConfirm.classList.add('hidden'));
document.getElementById('btn-delete-confirm').addEventListener('click', async () => {
  deleteConfirm.classList.add('hidden');
  if (editingId) {
    await DeleteConfig(editingId);
    proxyState.delete(editingId);
    await reloadConfigs();
  }
  showScreen('main');
});

document.getElementById('btn-gear').addEventListener('click', () => showScreen('settings'));
document.getElementById('btn-back-settings').addEventListener('click', () => showScreen('main'));

// applyLanguage re-labels everything for the given language: the static markup
// (data-i18n attributes) plus the dynamically-built lists whose strings are
// baked into innerHTML at render time. Cheap enough to just re-render them,
// since switching language is a rare, explicit action.
function applyLanguage(lang) {
  setLang(lang);
  applyStaticTranslations();
  renderConfigList();
  renderResourceList();
  renderExcludedAppList();
  refreshTileStatuses();
  configScreenTitle.textContent = editingId ? t('edit_config_title') : t('add_config');
  document.getElementById('btn-lang-ru').classList.toggle('active', getLang() === 'ru');
  document.getElementById('btn-lang-en').classList.toggle('active', getLang() === 'en');
}

document.getElementById('btn-lang-ru').addEventListener('click', async () => {
  await SetLanguage('ru'); // persisted Go-side so the tray menu matches too
  applyLanguage('ru');
});
document.getElementById('btn-lang-en').addEventListener('click', async () => {
  await SetLanguage('en');
  applyLanguage('en');
});

document.getElementById('btn-view-log').addEventListener('click', async () => {
  logText.textContent = await ReadLog();
  showScreen('log');
});
document.getElementById('btn-back-log').addEventListener('click', () => showScreen('settings'));

document.getElementById('btn-open-split-tunnel').addEventListener('click', () => showScreen('splitTunnel'));
document.getElementById('btn-back-split-tunnel').addEventListener('click', () => showScreen('settings'));
document.getElementById('btn-copy-log').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(logText.textContent);
  } catch (e) {
    console.error(e);
  }
});

document.getElementById('btn-add-resource').addEventListener('click', () => {
  resourceNameInput.value = '';
  resourceUrlInput.value = '';
  addResourceOverlay.classList.remove('hidden');
});
document.getElementById('btn-resource-cancel').addEventListener('click', () => {
  addResourceOverlay.classList.add('hidden');
});
document.getElementById('btn-resource-save').addEventListener('click', async () => {
  const name = resourceNameInput.value.trim();
  let url = resourceUrlInput.value.trim();
  if (!name || !url) return;
  if (!/^https?:\/\//i.test(url)) url = 'https://' + url;
  await AddResource(name, url);
  addResourceOverlay.classList.add('hidden');
  await reloadResources();
});

document.getElementById('btn-add-excluded-app').addEventListener('click', async () => {
  const path = await PickExcludedAppExe();
  if (!path) return;
  const fileName = path.split(/[\\/]/).pop() || path;
  const name = fileName.replace(/\.exe$/i, '');
  await AddExcludedApp(name, path);
  await reloadExcludedApps();
});

btnUpdate.addEventListener('click', async () => {
  btnUpdate.disabled = true;
  // On success the process relaunches and exits - this call never actually
  // returns then, so there's nothing further to do here in that case. On
  // failure the 'update:failed' listener below shows why and re-enables the
  // button.
  await ApplyUpdate();
});

// The Go side (updater.go) checks GitHub for a newer release shortly after
// startup - it no longer installs it on its own, just remembers it and fires
// this, which is what shows the green update button next to the settings
// gear. Actually downloading/installing (and the app relaunching) only
// happens once the user clicks that button.
if (window.runtime) {
  window.runtime.EventsOn('update:available', (tag) => {
    btnUpdate.title = t('update_btn_title', { tag });
    btnUpdate.classList.remove('hidden');
    updateBanner.textContent = t('update_banner_available', { tag });
    updateBanner.classList.remove('hidden');
  });
  window.runtime.EventsOn('update:downloading', (tag) => {
    updateBanner.textContent = t('update_installing', { tag });
    updateBanner.classList.remove('hidden');
  });
  window.runtime.EventsOn('update:failed', (message) => {
    updateBanner.textContent = t('update_failed', { message });
    updateBanner.classList.remove('hidden');
    btnUpdate.disabled = false;
  });
  // Fired by windows/networkwatch.go's native route-change callback (see
  // app.go's Connect) when the physical network changes out from under an
  // active tunnel - Status()'s own 4s poll already reflects the brief
  // disconnected-then-reconnected transition on the tile itself, this banner
  // just explains *why* rather than leaving it looking like a random drop.
  window.runtime.EventsOn('tunnel:reconnecting', () => {
    updateBanner.textContent = t('reconnecting');
    updateBanner.classList.remove('hidden');
    setTimeout(() => updateBanner.classList.add('hidden'), 4000);
  });
}

setInterval(refreshStatus, 4000);

(async () => {
  // Load the persisted language before the first render so every dynamically
  // built string starts out in the right language, then translate the static
  // markup and mark the active language button.
  try {
    setLang(await GetLanguage());
  } catch (e) {
    // default (ru) stays if the Go call fails
  }
  applyStaticTranslations();
  document.getElementById('btn-lang-ru').classList.toggle('active', getLang() === 'ru');
  document.getElementById('btn-lang-en').classList.toggle('active', getLang() === 'en');

  await reloadConfigs();
  await refreshStatus();
  await reloadResources();
  await reloadExcludedApps();

  // Backfill the cached server IP (and any country field from the yaml) once for
  // configs that don't have it yet - keyed on the IP, not the country, so a
  // config whose yaml simply has no country doesn't re-Ping on every launch.
  for (const config of configs) {
    if (!config.ip) resolveConfigGeo(config.id, config.yaml);
  }
})();
