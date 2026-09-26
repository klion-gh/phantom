// The toolbar popup: pairs with the app, then shows the site in the current
// tab against the Умный VPN list and adds or removes it.

const $ = (id) => document.getElementById(id);

function msg(key, params = {}) {
  let text = chrome.i18n.getMessage(key) || key;
  for (const [k, v] of Object.entries(params)) text = text.split(`{${k}}`).join(String(v));
  return text;
}

const VIEWS = ['perm', 'noapp', 'pair', 'pairing', 'nosite', 'error', 'site'];
const PAIR_TTL_MS = 2 * 60 * 1000;
const WATCH_PERMISSIONS = { permissions: ['webRequest'], origins: ['<all_urls>'] };

let tab = null;
let host = '';
let site = null; // last /v1/site answer
let chosenEntry = '';
let pollTimer = 0;

function show(view) {
  for (const v of VIEWS) $(`view-${v}`).classList.toggle('hidden', v !== view);
  $('footer').classList.toggle('hidden', !(view === 'site' || view === 'nosite'));
}

function renderPill(status) {
  const pill = $('mode-pill');
  if (!status) {
    pill.classList.add('hidden');
    return;
  }
  const on = status.smartEnabled && !status.autoEnabled;
  pill.textContent = status.autoEnabled ? msg('pillAuto') : on ? msg('pillSmartOn') : msg('pillSmartOff');
  pill.classList.toggle('on', status.autoEnabled || (on && status.connected));
  pill.classList.remove('hidden');
}

function handleError(e) {
  const code = e && e.code;
  if (code === 'noPermission') return show('perm');
  if (code === 'noApp') return show('noapp');
  if (code === 'unpaired') return show('pair');
  console.error(e);
  $('error-text').textContent = msg('errorText', { error: (e && e.message) || String(e) });
  show('error');
}

async function render() {
  try {
    if (!(await PhantomBridge.hasPermission())) return show('perm');
    await PhantomBridge.findApp();
    if (await resumePairing()) return;
    if (!(await PhantomBridge.isPaired())) return show('pair');
    if (!host) {
      renderPill(await PhantomBridge.status());
      return show('nosite');
    }
    site = await PhantomBridge.lookup(host);
    renderPill(site.status);
    renderSite();
    show('site');
    renderFailed();
  } catch (e) {
    handleError(e);
  }
}

// --- Pairing -------------------------------------------------------------------
//
// Approving happens in the app's window, and focusing it closes this popup -
// so the request in flight is kept in session storage and picked up again
// (token and all) the next time the popup opens.

async function startPairing() {
  $('btn-pair').disabled = true;
  $('pair-error').classList.add('hidden');
  try {
    const { id, code } = await PhantomBridge.startPairing();
    await chrome.storage.session.set({ pairing: { id, code, started: Date.now() } });
    await resumePairing();
  } catch (e) {
    handleError(e);
  } finally {
    $('btn-pair').disabled = false;
  }
}

// Shows and polls a pairing in progress, if there is one. Returns true when
// it took over the popup.
async function resumePairing() {
  const { pairing } = await chrome.storage.session.get('pairing');
  if (!pairing || Date.now() - pairing.started > PAIR_TTL_MS) {
    if (pairing) await chrome.storage.session.remove('pairing');
    return false;
  }
  $('pair-code').textContent = pairing.code;
  show('pairing');
  clearTimeout(pollTimer);
  const poll = async () => {
    let status;
    try {
      status = await PhantomBridge.pollPairing(pairing.id);
    } catch (e) {
      await chrome.storage.session.remove('pairing');
      return handleError(e);
    }
    if (status === 'pending') {
      pollTimer = setTimeout(poll, 1000);
      return;
    }
    await chrome.storage.session.remove('pairing');
    if (status === 'approved') return render();
    $('pair-error').textContent = status === 'denied' ? msg('pairDenied') : msg('pairExpired');
    $('pair-error').classList.remove('hidden');
    show('pair');
  };
  poll();
  return true;
}

async function cancelPairing() {
  clearTimeout(pollTimer);
  await chrome.storage.session.remove('pairing');
  show('pair');
}

// --- The site in this tab ---------------------------------------------------------

function renderSite() {
  $('site-host').textContent = site.host;
  // The tab's own icon, or the site's first letter where there is none.
  const icon = $('site-favicon');
  const letter = $('site-letter');
  letter.textContent = site.host.replace(/^www\./, '').charAt(0);
  const showLetter = () => {
    icon.classList.add('hidden');
    letter.classList.remove('hidden');
  };
  if (tab && tab.favIconUrl && /^(https?|data):/.test(tab.favIconUrl)) {
    icon.onerror = showLetter;
    icon.onload = () => letter.classList.add('hidden');
    icon.src = tab.favIconUrl;
    icon.classList.remove('hidden');
  } else {
    showLetter();
  }

  const state = $('site-state');
  state.classList.toggle('listed', site.listed);
  if (site.listed) {
    const by = site.listedBy && site.listedBy.toLowerCase() !== site.host ? site.listedBy : '';
    state.textContent = by ? msg('stateListedBy', { entry: by }) : msg('stateListed');
  } else {
    state.textContent = msg('stateDirect');
  }

  const service = $('site-service');
  service.classList.toggle('hidden', !site.service || site.listed);
  if (site.service) {
    service.textContent = msg('serviceNote', { name: site.service, n: site.serviceDomains.length });
  }

  const showOptions = !site.listed && !site.service && site.options.length > 1;
  $('site-options').classList.toggle('hidden', !showOptions);
  if (showOptions) {
    if (!site.options.includes(chosenEntry)) chosenEntry = site.suggested;
    const chips = $('site-option-chips');
    chips.textContent = '';
    for (const option of site.options) {
      const chip = document.createElement('button');
      chip.className = 'chip' + (option === chosenEntry ? ' active' : '');
      chip.textContent = option;
      chip.title = option;
      chip.addEventListener('click', () => {
        chosenEntry = option;
        renderSite();
      });
      chips.appendChild(chip);
    }
  }

  $('btn-add').classList.toggle('hidden', site.listed);
  $('btn-remove').classList.toggle('hidden', !site.listed);
  if (!site.listed) $('site-after').classList.add('hidden');

  const status = site.status;
  const note = $('mode-note');
  if (status.autoEnabled) {
    $('mode-note-text').textContent = msg('noteAuto');
    $('btn-enable-smart').classList.add('hidden');
    note.classList.remove('hidden');
  } else if (!status.smartEnabled) {
    $('mode-note-text').textContent = msg('noteSmartOff');
    $('btn-enable-smart').classList.remove('hidden');
    note.classList.remove('hidden');
  } else {
    note.classList.add('hidden');
  }
}

async function addSite() {
  const btn = $('btn-add');
  btn.disabled = true;
  try {
    const entry = !site.service && site.options.length > 1 ? chosenEntry : '';
    site = await PhantomBridge.add(host, entry);
    renderPill(site.status);
    renderSite();
    $('site-after').classList.remove('hidden');
    renderFailed();
    notifyBadge();
  } catch (e) {
    handleError(e);
  } finally {
    btn.disabled = false;
  }
}

async function removeSite() {
  const btn = $('btn-remove');
  btn.disabled = true;
  try {
    site = await PhantomBridge.remove(host);
    renderPill(site.status);
    $('site-after').classList.add('hidden');
    renderSite();
    renderFailed();
    notifyBadge();
  } catch (e) {
    handleError(e);
  } finally {
    btn.disabled = false;
  }
}

function notifyBadge() {
  if (tab) chrome.runtime.sendMessage({ type: 'refreshBadge', tabId: tab.id }).catch(() => {});
}

async function enableSmart() {
  try {
    await PhantomBridge.setSmart(true);
    // The app turns it on the way its own toggle does - give it a moment.
    setTimeout(render, 1200);
  } catch (e) {
    handleError(e);
  }
}

// --- Resources this page failed to load ---------------------------------------------

async function renderFailed() {
  const granted = await chrome.permissions.contains(WATCH_PERMISSIONS);
  $('failed-off').classList.toggle('hidden', granted);
  const list = $('failed-list');
  const addBtn = $('btn-failed-add');
  if (!granted || !tab) {
    $('failed-empty').classList.add('hidden');
    list.classList.add('hidden');
    addBtn.classList.add('hidden');
    return;
  }
  const key = `failed:${tab.id}`;
  const failed = (await chrome.storage.session.get(key))[key] || {};

  // What each failing host would add, deduplicated: a dozen googlevideo.com
  // mirrors are one YouTube, not twelve rows. Hosts already routed (or this
  // tab's own site, which the card above covers) are left out.
  const rows = new Map();
  await Promise.all(Object.entries(failed).map(async ([h, info]) => {
    if (h === host) return;
    try {
      const m = await PhantomBridge.lookup(h);
      if (m.listed) return;
      const label = m.service ? `${m.service} (${msg('serviceWord')})` : m.suggested;
      const row = rows.get(label) || { hosts: [], errors: 0, error: info.error };
      row.hosts.push(h);
      row.errors += info.count;
      rows.set(label, row);
    } catch (e) {
      // skip hosts the app can't judge
    }
  }));

  list.textContent = '';
  for (const [label, row] of [...rows.entries()].sort((a, b) => b[1].errors - a[1].errors)) {
    const el = document.createElement('label');
    el.className = 'failed-row';
    el.title = row.hosts.join('\n');
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.checked = true;
    box.dataset.host = row.hosts[0];
    const name = document.createElement('span');
    name.className = 'failed-name';
    name.textContent = label;
    const err = document.createElement('span');
    err.className = 'failed-error';
    err.textContent = row.error.replace(/^net::ERR_|^NS_ERROR_/, '');
    el.append(box, name, err);
    list.appendChild(el);
  }
  const any = rows.size > 0;
  list.classList.toggle('hidden', !any);
  addBtn.classList.toggle('hidden', !any);
  $('failed-empty').classList.toggle('hidden', any);
}

async function addFailed() {
  const btn = $('btn-failed-add');
  btn.disabled = true;
  try {
    for (const box of $('failed-list').querySelectorAll('input:checked')) {
      await PhantomBridge.add(box.dataset.host);
    }
    site = await PhantomBridge.lookup(host);
    renderSite();
    $('site-after').classList.remove('hidden');
    await renderFailed();
  } catch (e) {
    handleError(e);
  } finally {
    btn.disabled = false;
  }
}

async function enableWatching() {
  // Must be the first thing the click does: the request needs the gesture.
  const granted = await chrome.permissions.request(WATCH_PERMISSIONS).catch(() => false);
  if (granted) {
    $('failed-empty').textContent = msg('failedJustEnabled');
    renderFailed();
  }
}

// --- Wiring ---------------------------------------------------------------------------

function applyStaticText() {
  for (const el of document.querySelectorAll('[data-i18n]')) {
    el.textContent = msg(el.dataset.i18n);
  }
}

$('btn-perm').addEventListener('click', async () => {
  if (await PhantomBridge.requestPermission().catch(() => false)) render();
});
$('btn-retry').addEventListener('click', render);
$('btn-error-retry').addEventListener('click', render);
$('btn-pair').addEventListener('click', startPairing);
$('btn-pair-cancel').addEventListener('click', cancelPairing);
$('btn-add').addEventListener('click', addSite);
$('btn-remove').addEventListener('click', removeSite);
$('btn-reload').addEventListener('click', () => {
  if (tab) chrome.tabs.reload(tab.id);
  window.close();
});
$('btn-enable-smart').addEventListener('click', enableSmart);
$('btn-failed-enable').addEventListener('click', enableWatching);
$('btn-failed-add').addEventListener('click', addFailed);
$('btn-open-app').addEventListener('click', async () => {
  try {
    await PhantomBridge.showApp();
    window.close();
  } catch (e) {
    handleError(e);
  }
});
$('btn-unpair').addEventListener('click', async () => {
  await PhantomBridge.forget();
  show('pair');
});

(async () => {
  applyStaticText();
  try {
    [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  } catch (e) {
    tab = null;
  }
  host = PhantomBridge.hostOf((tab && tab.url) || '');
  render();
})();
