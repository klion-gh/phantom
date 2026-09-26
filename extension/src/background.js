// Background: context-menu entries, the optional watcher for resources a page
// failed to load, and the toolbar badge.
//
// Chrome runs this as a service worker (bridge.js pulled in below); Firefox
// runs it as an event page with bridge.js listed before it in the manifest.
if (typeof importScripts === 'function' && typeof PhantomBridge === 'undefined') {
  importScripts('bridge.js');
}

const msg = (key) => chrome.i18n.getMessage(key) || key;

// --- Context menus ----------------------------------------------------------

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.removeAll(() => {
    chrome.contextMenus.create({ id: 'add-page', title: msg('menuAddPage'), contexts: ['page'] });
    chrome.contextMenus.create({ id: 'add-link', title: msg('menuAddLink'), contexts: ['link'] });
  });
});

chrome.contextMenus.onClicked.addListener(async (info, tab) => {
  const url = info.menuItemId === 'add-link' ? info.linkUrl : info.pageUrl;
  const host = PhantomBridge.hostOf(url || '');
  const tabId = tab && tab.id >= 0 ? tab.id : undefined;
  if (!host) return;
  try {
    await PhantomBridge.add(host);
    flashBadge(tabId, '✓', '#22C55E');
    if (tabId !== undefined) refreshBadge(tabId);
  } catch (e) {
    flashBadge(tabId, '!', '#EF4444');
    // Not paired or the app isn't running: the popup explains and fixes
    // that. Opening it needs a user gesture, which not every browser counts
    // a menu click as - the badge above is the fallback.
    if (chrome.action.openPopup) chrome.action.openPopup().catch(() => {});
  }
});

function flashBadge(tabId, text, color) {
  const target = tabId === undefined ? {} : { tabId };
  chrome.action.setBadgeBackgroundColor({ ...target, color });
  chrome.action.setBadgeText({ ...target, text });
  setTimeout(() => {
    if (tabId !== undefined) refreshBadge(tabId);
    else chrome.action.setBadgeText({ text: '' });
  }, 2500);
}

// --- Badge: is the site in this tab routed through Phantom? ------------------
//
// Needs to read tabs' addresses, which only the optional "all sites"
// permission allows - without it the badge simply stays empty.

async function refreshBadge(tabId) {
  let tab;
  try {
    tab = await chrome.tabs.get(tabId);
  } catch (e) {
    return;
  }
  const host = PhantomBridge.hostOf(tab.url || '');
  let text = '';
  if (host) {
    try {
      const site = await PhantomBridge.lookup(host);
      if (site.listed) text = '●';
    } catch (e) {
      // app not running / not paired: no badge
    }
  }
  chrome.action.setBadgeBackgroundColor({ tabId, color: '#8B7CF6' });
  chrome.action.setBadgeText({ tabId, text });
}

chrome.tabs.onActivated.addListener(({ tabId }) => refreshBadge(tabId));
chrome.tabs.onUpdated.addListener((tabId, change) => {
  if (change.status === 'complete' || change.url) refreshBadge(tabId);
});

chrome.runtime.onMessage.addListener((message) => {
  if (message && message.type === 'refreshBadge' && message.tabId !== undefined) {
    refreshBadge(message.tabId);
  }
});

// --- Resources the page failed to load ---------------------------------------
//
// A site that loads its page but not its video or images is the classic case
// of a half-listed site: the media lives on another domain, which went direct
// and is blocked. With the optional permissions granted, failed requests are
// noted per tab (in session storage - a service worker doesn't live long) so
// the popup can offer those domains too.

// Failures that say nothing about blocking: cancelled requests, ad blockers,
// cache-only lookups.
const IGNORED_ERRORS = [
  'net::ERR_ABORTED', 'net::ERR_BLOCKED_BY_CLIENT', 'net::ERR_BLOCKED_BY_RESPONSE',
  'net::ERR_BLOCKED_BY_ORB', 'net::ERR_CACHE_MISS', 'net::ERR_UNKNOWN_URL_SCHEME',
  'NS_BINDING_ABORTED', 'NS_ERROR_ABORT', 'NS_ERROR_CONTENT_BLOCKED',
];
const MAX_HOSTS_PER_TAB = 30;

let watching = false;

function watchFailures() {
  if (watching || !chrome.webRequest) return;
  watching = true;
  chrome.webRequest.onBeforeRequest.addListener(
    (d) => {
      if (d.type === 'main_frame' && d.tabId >= 0) {
        chrome.storage.session.remove(`failed:${d.tabId}`);
      }
    },
    { urls: ['<all_urls>'], types: ['main_frame'] },
  );
  chrome.webRequest.onErrorOccurred.addListener(
    (d) => {
      if (d.tabId < 0 || IGNORED_ERRORS.some((e) => d.error.startsWith(e))) return;
      const host = PhantomBridge.hostOf(d.url);
      if (!host) return;
      // One at a time: failures arrive in bursts, and overlapping
      // read-modify-writes of the same key would drop some.
      recording = recording.then(() => recordFailure(d.tabId, host, d.error)).catch(() => {});
    },
    { urls: ['<all_urls>'] },
  );
}

let recording = Promise.resolve();

async function recordFailure(tabId, host, error) {
  const key = `failed:${tabId}`;
  const stored = (await chrome.storage.session.get(key))[key] || {};
  if (!stored[host] && Object.keys(stored).length >= MAX_HOSTS_PER_TAB) return;
  const entry = stored[host] || { count: 0, error: '' };
  entry.count += 1;
  entry.error = error;
  stored[host] = entry;
  await chrome.storage.session.set({ [key]: stored });
}

chrome.tabs.onRemoved.addListener((tabId) => chrome.storage.session.remove(`failed:${tabId}`));

// Listeners have to be registered while the worker starts, not later in some
// callback, or events that wake it are missed - so this runs at top level
// whenever the permission is already there, and again right after it's
// granted.
watchFailures();
chrome.permissions.onAdded.addListener(() => watchFailures());
