// Builds the extension for each browser family into dist/<target>/, ready to
// load unpacked or to zip:
//
//   node build.mjs            -> dist/chromium, dist/firefox
//
// One source tree (src/) and one manifest; the only differences are in the
// manifest: Chromium runs the background as a service worker, Firefox as an
// event page (with bridge.js listed before background.js) and needs its own
// add-on id.
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const base = JSON.parse(readFileSync(join(root, 'manifest.json'), 'utf8'));

const targets = {
  chromium: (m) => m,
  firefox: (m) => {
    const out = { ...m, background: { scripts: ['bridge.js', 'background.js'] } };
    delete out.minimum_chrome_version;
    out.browser_specific_settings = {
      gecko: { id: 'phantom-extension@klion-gh.github.io', strict_min_version: '128.0' },
    };
    return out;
  },
};

for (const [name, shape] of Object.entries(targets)) {
  const out = join(root, 'dist', name);
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  cpSync(join(root, 'src'), out, { recursive: true });
  writeFileSync(join(out, 'manifest.json'), JSON.stringify(shape(base), null, 2) + '\n');
  console.log(`built ${out}`);
}
