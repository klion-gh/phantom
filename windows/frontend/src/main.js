import './style.css';
import { Connect, Disconnect, Status, ReadLog, ListConfigs, AddConfig, UpdateConfig, DeleteConfig, Ping } from '../wailsjs/go/main/App';

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

let configs = [];
let editingId = null;
let pendingConnectId = null; // id currently mid-Connect() call - drives the spinner state
let currentStatus = { connected: false, alive: false, stats: '{}', activeConfigId: '' };

const pingData = new Map(); // id -> { ip, latencyMs, country, countryCode }
const pingTimers = new Map(); // id -> interval handle

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

const lastGeoAttempt = new Map(); // id -> epoch ms of the last (possibly failed) geo lookup

async function pollPing(config) {
  const prev = pingData.get(config.id) || {};
  try {
    const json = JSON.parse(await Ping(config.yaml));
    if (json.ip) {
      let country = prev.country;
      let countryCode = prev.countryCode;
      const ipChanged = json.ip !== prev.ip;
      const now = Date.now();
      // Retrying a failed lookup on every 6s ping cycle is what rate-limited ipapi.co -
      // only retry a failure every couple of minutes, but always retry immediately if
      // the resolved IP actually changed.
      if (ipChanged || (!countryCode && now - (lastGeoAttempt.get(config.id) || 0) > 120000)) {
        lastGeoAttempt.set(config.id, now);
        const geo = await fetchGeo(json.ip);
        if (geo) {
          country = geo.country;
          countryCode = geo.countryCode;
        }
      }
      pingData.set(config.id, { ip: json.ip, latencyMs: json.latency_ms, country, countryCode });
    } else {
      pingData.set(config.id, { ...prev, latencyMs: null });
    }
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
  card.querySelector('.config-ip').textContent = info.ip || parseYamlField(config.yaml, 'server') || '—';
  card.querySelector('.ping-text').textContent = info.latencyMs != null ? `Пинг: ${info.latencyMs} мс` : 'Пинг: —';

  const flagImg = card.querySelector('.geo-flag');
  const geoText = card.querySelector('.geo-text');
  if (info.countryCode) {
    // Windows' Segoe UI Emoji has no flag glyphs (shows the bare letter pair
    // instead) - a real flag image is the only reliable way to show one.
    flagImg.src = `https://flagcdn.com/24x18/${info.countryCode.toLowerCase()}.png`;
    flagImg.classList.remove('hidden');
    geoText.textContent = info.country || '';
  } else {
    flagImg.classList.add('hidden');
    geoText.textContent = '';
  }
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
  if (editingId) {
    await UpdateConfig(editingId, yaml);
  } else {
    await AddConfig(yaml);
  }
  await reloadConfigs();
  showScreen('main');
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

setInterval(refreshStatus, 4000);

(async () => {
  await reloadConfigs();
  await refreshStatus();
})();
