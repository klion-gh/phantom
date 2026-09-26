// Client for the Phantom app's local bridge (windows/browserbridge.go in the
// Phantom repo). Shared by the popup and the background script: both load it
// as a classic script, so PhantomBridge is a plain global.
//
// The app listens on the first free port of 47815-47819 on 127.0.0.1 and
// answers only this extension's origin; everything but the handshake needs
// the token handed out when the user approves pairing in the app.

// eslint-disable-next-line no-unused-vars
const PhantomBridge = (() => {
  const FIRST_PORT = 47815;
  const PORT_COUNT = 5;
  const LOCAL_ORIGIN = 'http://127.0.0.1/*';

  class BridgeError extends Error {
    // code: 'noPermission' | 'noApp' | 'unpaired' | 'failed'
    constructor(code, message) {
      super(message || code);
      this.code = code;
    }
  }

  async function saved() {
    const { token = '', port = 0 } = await chrome.storage.local.get(['token', 'port']);
    return { token, port };
  }

  async function hasPermission() {
    try {
      return await chrome.permissions.contains({ origins: [LOCAL_ORIGIN] });
    } catch (e) {
      return false;
    }
  }

  // Must run from a user gesture (a click in the popup).
  function requestPermission() {
    return chrome.permissions.request({ origins: [LOCAL_ORIGIN] });
  }

  async function raw(port, path, { method = 'GET', body, token, timeout = 2500 } = {}) {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), timeout);
    try {
      const headers = {};
      if (body !== undefined) headers['Content-Type'] = 'application/json';
      if (token) headers['X-Phantom-Token'] = token;
      const res = await fetch(`http://127.0.0.1:${port}${path}`, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: ctrl.signal,
        cache: 'no-store',
        credentials: 'omit',
      });
      let data = null;
      try {
        data = await res.json();
      } catch (e) {
        // non-JSON error body
      }
      return { status: res.status, data };
    } finally {
      clearTimeout(timer);
    }
  }

  async function isPhantom(port) {
    try {
      const { status, data } = await raw(port, '/v1/hello', { timeout: 800 });
      return status === 200 && data && data.app === 'phantom';
    } catch (e) {
      return false;
    }
  }

  // The port the app is on: the remembered one if it still answers, else the
  // first in the range that does.
  async function findApp() {
    if (!(await hasPermission())) throw new BridgeError('noPermission');
    const { port } = await saved();
    if (port && (await isPhantom(port))) return port;
    for (let p = FIRST_PORT; p < FIRST_PORT + PORT_COUNT; p++) {
      if (p !== port && (await isPhantom(p))) {
        await chrome.storage.local.set({ port: p });
        return p;
      }
    }
    throw new BridgeError('noApp');
  }

  async function call(path, opts = {}) {
    const port = await findApp();
    const { token } = await saved();
    if (!token) throw new BridgeError('unpaired');
    let res;
    try {
      res = await raw(port, path, { ...opts, token });
    } catch (e) {
      throw new BridgeError('noApp', e.message);
    }
    if (res.status === 401) {
      // Revoked in the app (or never valid): forget it and pair again.
      await chrome.storage.local.remove('token');
      throw new BridgeError('unpaired');
    }
    if (res.status < 200 || res.status >= 300) {
      throw new BridgeError('failed', `HTTP ${res.status}`);
    }
    return res.data;
  }

  function browserName() {
    const ua = navigator.userAgent;
    if (/Firefox\//.test(ua)) return 'Firefox';
    const brands = (navigator.userAgentData && navigator.userAgentData.brands) || [];
    const known = ['Microsoft Edge', 'Opera', 'YaBrowser', 'Yandex', 'Brave', 'Vivaldi', 'Google Chrome'];
    for (const k of known) {
      if (brands.some((b) => b.brand === k)) return k === 'YaBrowser' ? 'Yandex' : k;
    }
    if (/YaBrowser\//.test(ua)) return 'Yandex';
    if (/Edg\//.test(ua)) return 'Microsoft Edge';
    if (/OPR\//.test(ua)) return 'Opera';
    return 'Chromium';
  }

  async function startPairing() {
    const port = await findApp();
    const res = await raw(port, '/v1/pair', { method: 'POST', body: { client: browserName() } });
    if (res.status !== 200 || !res.data) throw new BridgeError('failed', `HTTP ${res.status}`);
    return res.data; // { id, code }
  }

  // One poll: 'pending' | 'approved' | 'denied' | 'expired'. On approval the
  // token is stored and pairing is done.
  async function pollPairing(id) {
    const port = await findApp();
    const res = await raw(port, `/v1/pair/${encodeURIComponent(id)}`);
    const status = (res.data && res.data.status) || 'expired';
    if (status === 'approved' && res.data.token) {
      await chrome.storage.local.set({ token: res.data.token });
    }
    return status;
  }

  async function isPaired() {
    return !!(await saved()).token;
  }

  async function forget() {
    await chrome.storage.local.remove('token');
  }

  // The host a page URL belongs to, or '' for anything that isn't a website.
  function hostOf(url) {
    try {
      const u = new URL(url);
      if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
      return u.hostname.replace(/^\[|\]$/g, '');
    } catch (e) {
      return '';
    }
  }

  return {
    BridgeError,
    hasPermission,
    requestPermission,
    findApp,
    startPairing,
    pollPairing,
    isPaired,
    forget,
    hostOf,
    status: () => call('/v1/status'),
    lookup: (host) => call('/v1/site', { method: 'POST', body: { host } }),
    add: (host, entry = '') => call('/v1/site/add', { method: 'POST', body: { host, entry } }),
    remove: (host) => call('/v1/site/remove', { method: 'POST', body: { host } }),
    setSmart: (enabled) => call('/v1/smart', { method: 'POST', body: { enabled } }),
    showApp: () => call('/v1/show', { method: 'POST', body: {} }),
  };
})();
