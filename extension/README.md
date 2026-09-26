# Phantom browser extension

Adds the site in the current tab to the Phantom Windows app's Умный VPN list.
Talks only to the app on this computer (`127.0.0.1:47815`, see
`windows/browserbridge.go` and PROTOCOL.md §11.4a).

```bash
node build.mjs     # -> dist/chromium, dist/firefox
```

Load `dist/chromium` unpacked (`chrome://extensions` → Developer mode → Load
unpacked) or `dist/firefox/manifest.json` as a temporary add-on
(`about:debugging`). Releases ship both as zips.

- `src/bridge.js` - the bridge client, shared by the popup and background.
- `src/popup.*` - pairing and the current site.
- `src/background.js` - context menus, the optional failed-request watcher,
  the badge.
- `src/_locales` - ru (default) and en.
