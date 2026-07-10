import './style.css';
import { Connect, Disconnect, Status, ReadLog, ListConfigs, AddConfig, UpdateConfig, DeleteConfig, SetConfigGeo, Ping, ListResources, AddResource, DeleteResource } from '../wailsjs/go/main/App';

const screens = {
  main: document.getElementById('screen-main'),
  config: document.getElementById('screen-config'),
  settings: document.getElementById('screen-settings'),
  log: document.getElementById('screen-log'),
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
const addResourceOverlay = document.getElementById('add-resource-overlay');
const resourceNameInput = document.getElementById('resource-name-input');
const resourceUrlInput = document.getElementById('resource-url-input');

let configs = [];
let editingId = null;
let pendingConnectId = null; // id currently mid-Connect() call - drives the spinner state
let currentStatus = { connected: false, alive: false, stats: '{}', activeConfigId: '' };

const pingData = new Map(); // id -> { ip, latencyMs } - just live latency; country/flag
                             // come from the config's own cached fields (see SetConfigGeo)
const pingTimers = new Map(); // id -> interval handle

let resources = [];
const resourceTimers = new Map(); // id -> interval handle

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

// ipwho.is rather than ipapi.co - the latter's free tier rate-limited itself into 429s
// during development from repeated polling across both this app and the Android one.
// Only called once, right after a config is added/edited (see resolveConfigGeo) - not
// on every ping cycle, since a saved server's location essentially never changes.
async function fetchGeo(ip) {
  try {
    const resp = await fetch(`https://ipwho.is/${ip}`);
    const data = await resp.json();
    if (data.success !== false && data.country && data.country_code) {
      return { country: data.country, countryCode: data.country_code };
    }
  } catch (e) {
    console.error(e);
  }
  return null;
}

// Resolves and persists a config's server IP/country/flag exactly once (via
// SetConfigGeo), then re-renders so the tile picks it up. Called after every
// Add/UpdateConfig - Update clears the old cached geo first (see
// windows/configstore.go) since the edited yaml may point at a new server.
async function resolveConfigGeo(id, yaml) {
  try {
    const ping = JSON.parse(await Ping(yaml));
    if (!ping.ip) return;
    const geo = await fetchGeo(ping.ip);
    await SetConfigGeo(id, ping.ip, geo?.country || '', geo?.countryCode || '');
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
  card.querySelector('.ping-text').textContent = info.latencyMs != null ? `Пинг: ${info.latencyMs} мс` : 'Пинг: —';

  // Country/flag come from the config's own cached fields (resolved once at
  // save time via resolveConfigGeo/SetConfigGeo), not from the live ping.
  const flagImg = card.querySelector('.geo-flag');
  const geoText = card.querySelector('.geo-text');
  if (config.countryCode) {
    // Windows' Segoe UI Emoji has no flag glyphs (shows the bare letter pair
    // instead) - a real flag image is the only reliable way to show one.
    flagImg.src = `https://flagcdn.com/24x18/${config.countryCode.toLowerCase()}.png`;
    flagImg.classList.remove('hidden');
    geoText.textContent = config.country || '';
  } else {
    flagImg.classList.add('hidden');
    geoText.textContent = '';
  }
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
    statusEl.textContent = `Доступен, ${ms} мс`;
    statusEl.className = 'resource-status reachable';
  } catch (e) {
    statusEl.textContent = 'Недоступен';
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
      <button class="resource-remove-btn" title="Удалить">&times;</button>
      <div class="resource-name">${escapeHtml(resource.name)}</div>
      <div class="resource-status">Проверка...</div>
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
          <span class="ping-text">Пинг: —</span>
          <img class="geo-flag hidden" alt="" />
          <span class="geo-text"></span>
        </div>
      </div>
      <button class="config-edit-btn" title="Редактировать">&#8942;</button>
      <button class="power-btn idle" title="Подключить">
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
    card.querySelector('.power-btn').addEventListener('click', () => toggleConnection(config));

    configList.appendChild(card);
    startPingLoop(config);
  }

  refreshTileStatuses();
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
  configScreenTitle.textContent = 'Редактировать конфигурацию';
  btnDelete.classList.remove('hidden');
  showScreen('config');
}

document.getElementById('btn-add').addEventListener('click', () => {
  editingId = null;
  configTextarea.value = '';
  configScreenTitle.textContent = 'Добавить конфигурацию';
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
    await reloadConfigs();
  }
  showScreen('main');
});

document.getElementById('btn-gear').addEventListener('click', () => showScreen('settings'));
document.getElementById('btn-back-settings').addEventListener('click', () => showScreen('main'));

document.getElementById('btn-view-log').addEventListener('click', async () => {
  logText.textContent = await ReadLog();
  showScreen('log');
});
document.getElementById('btn-back-log').addEventListener('click', () => showScreen('settings'));
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

// The Go side (updater.go) checks GitHub for a newer release shortly after
// startup and, if found, downloads+swaps+relaunches on its own - these just
// surface that it's happening, since a silent relaunch with no explanation
// would look like the app crashed.
if (window.runtime) {
  window.runtime.EventsOn('update:downloading', (tag) => {
    updateBanner.textContent = `Найдено обновление ${tag} — скачивание и перезапуск...`;
    updateBanner.classList.remove('hidden');
  });
  window.runtime.EventsOn('update:failed', (message) => {
    updateBanner.textContent = `Не удалось обновиться: ${message}`;
    updateBanner.classList.remove('hidden');
  });
}

setInterval(refreshStatus, 4000);

(async () => {
  await reloadConfigs();
  await refreshStatus();
  await reloadResources();

  // Configs saved before this per-config geo cache existed have no country/flag yet -
  // backfill them once on launch rather than leaving those tiles blank forever.
  for (const config of configs) {
    if (!config.countryCode) resolveConfigGeo(config.id, config.yaml);
  }
})();
