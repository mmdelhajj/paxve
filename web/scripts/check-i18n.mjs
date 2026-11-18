// Fails (exit 1) when a translation key exists in one locale file but not in another,
// or when {{placeholders}} differ between languages for the same key.
// Usage: node scripts/check-i18n.mjs
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'locales');

async function load(file) {
  // The locale files are plain TS object literals; strip the TS-only syntax and import as a JS module.
  const src = readFileSync(join(dir, file), 'utf8')
    .replace(/^import .*$/gm, '')
    .replace(/^export type .*$/gm, '')
    .replace(/const (\w+): \w+ =/, 'const $1 =');
  const mod = await import('data:text/javascript;base64,' + Buffer.from(src).toString('base64'));
  return mod.default;
}

function flatten(obj, prefix = '', out = new Map()) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === 'object') flatten(v, key, out);
    else out.set(key, String(v));
  }
  return out;
}

const placeholders = (s) => [...s.matchAll(/\{\{\s*(\w+)\s*\}\}/g)].map((m) => m[1]).sort().join(',');

const files = readdirSync(dir).filter((f) => f.endsWith('.ts'));
const locales = {};
for (const f of files) locales[f.replace('.ts', '')] = flatten(await load(f));

const ref = locales.en;
let problems = 0;
for (const [lang, keys] of Object.entries(locales)) {
  if (lang === 'en') continue;
  for (const k of ref.keys()) {
    if (!keys.has(k)) { console.error(`[${lang}] missing key: ${k}`); problems++; continue; }
    if (placeholders(ref.get(k)) !== placeholders(keys.get(k))) {
      console.error(`[${lang}] placeholders differ for ${k}: en={${placeholders(ref.get(k))}} ${lang}={${placeholders(keys.get(k))}}`);
      problems++;
    }
    if (!keys.get(k).trim() && ref.get(k).trim()) { console.error(`[${lang}] empty value: ${k}`); problems++; }
  }
  for (const k of keys.keys()) if (!ref.has(k)) { console.error(`[${lang}] extra key not in en: ${k}`); problems++; }
}
console.log(`checked ${ref.size} keys in ${Object.keys(locales).length} languages: ${problems ? problems + ' problem(s)' : 'OK'}`);
process.exit(problems ? 1 : 0);
