// Machine-translates the English UI (src/locales/en.ts) into the extra languages, writing
// src/locales/extra/<lang>.json (same nested keys). Incremental: keys already translated (and whose English
// text did not change) are kept; run it again after adding keys. Every file carries "_meta.machine": true —
// the strings need a human review (tenants can also fix any string from Settings → Translations).
//
// Usage: node scripts/translate.mjs [lang ...]      (default: all extra languages)
// Only the product's own UI strings are sent to the translation service, never customer data.
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'locales');
const outDir = join(root, 'extra');
const LANGS = ['tr', 'es', 'de', 'fa', 'ur', 'ckb', 'ru', 'pt'];

async function loadEn() {
  const src = readFileSync(join(root, 'en.ts'), 'utf8')
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

function unflatten(map) {
  const out = {};
  for (const [key, v] of map) {
    const parts = key.split('.');
    let o = out;
    for (const p of parts.slice(0, -1)) o = o[p] ??= {};
    o[parts.at(-1)] = v;
  }
  return out;
}

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const unesc = (s) => s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&#(\d+);/g, (_, n) => String.fromCharCode(Number(n))).replace(/&amp;/g, '&');
// protect {{placeholders}}, brand names and line breaks from the translator
const protect = (s) => esc(s)
  .replace(/\{\{\s*\w+\s*\}\}/g, (m) => `<span translate="no">${m}</span>`)
  .replace(/\b(Paxve|WhatsApp|MikroTik|USD|LBP|NSSF|SMTP|API|CSV|PDF|Excel|QR)\b/g, (m) => `<span translate="no">${m}</span>`)
  .replace(/\n/g, '<br>');
const restore = (s) => unesc(s.replace(/<br\s*\/?>/gi, '\n').replace(/<span translate="no">(.*?)<\/span>/g, '$1').replace(/<\/?span[^>]*>/g, ''));
const placeholders = (s) => [...s.matchAll(/\{\{\s*(\w+)\s*\}\}/g)].map((m) => m[1]).sort().join(',');

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function translateBatch(texts, tl) {
  const body = new URLSearchParams({ client: 'gtx', sl: 'en', tl, format: 'html' });
  for (const t of texts) body.append('q', protect(t));
  for (let attempt = 0; attempt < 5; attempt++) {
    const res = await fetch(`https://translate.googleapis.com/translate_a/t?${body}`);
    if (res.ok) {
      let j = await res.json();
      if (!Array.isArray(j)) j = [j];
      return j.map((x) => restore(Array.isArray(x) ? x[0] : String(x)));
    }
    await sleep(2000 * (attempt + 1));
  }
  throw new Error(`translate ${tl}: failed`);
}

const en = flatten(await loadEn());
mkdirSync(outDir, { recursive: true });
const want = process.argv.slice(2).length ? process.argv.slice(2) : LANGS;

for (const lang of want) {
  const file = join(outDir, `${lang}.json`);
  const prev = existsSync(file) ? flatten(JSON.parse(readFileSync(file, 'utf8'))) : new Map();
  const out = new Map();
  const todo = [];
  for (const [k, v] of en) {
    // keep a previous translation (delete the key from the JSON file to have it translated again)
    if (prev.has(k)) out.set(k, prev.get(k));
    else todo.push(k);
  }
  console.log(`${lang}: ${out.size} kept, ${todo.length} to translate`);
  // batches small enough for one GET URL (~6 KB encoded)
  const batches = [];
  let cur = [], size = 0;
  for (const k of todo) {
    const n = encodeURIComponent(protect(en.get(k))).length + 3;
    if (cur.length && (size + n > 6000 || cur.length >= 40)) { batches.push(cur); cur = []; size = 0; }
    cur.push(k); size += n;
  }
  if (cur.length) batches.push(cur);
  for (const keys of batches) {
    const src = keys.map((k) => en.get(k));
    let tr;
    try {
      tr = await translateBatch(src, lang);
    } catch (e) {
      console.error(String(e));
      break;
    }
    keys.forEach((k, j) => {
      const v = tr[j];
      // a translation that lost or invented a placeholder is unusable: keep English for that key
      out.set(k, v && placeholders(v) === placeholders(src[j]) ? v : src[j]);
    });
    await sleep(300);
  }
  out.set('_meta.machine', 'true');
  out.set('_meta.lang', lang);
  writeFileSync(file, JSON.stringify(unflatten(new Map([...out].sort())), null, 1) + '\n');
  console.log(`${lang}: wrote ${out.size - 2} keys`);
}
