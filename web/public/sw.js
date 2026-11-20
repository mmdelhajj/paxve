// Paxve service worker: keeps the whole app available offline (collectors in the field).
// API calls are never cached here; offline data lives in the app (localStorage outbox/cache).
// The page code is split into chunks; on every app start the page asks the worker to "sync",
// which precaches all chunks listed in the Vite build manifest and drops chunks of old builds.
const CACHE = 'paxve-shell-v12';
const SHELL = ['/', '/favicon.svg', '/pwa.js', '/manifest.webmanifest', '/manifest-field.webmanifest', '/manifest-my.webmanifest',
  '/manifest-owner.webmanifest', '/icons/main-192.png', '/icons/field-192.png', '/icons/my-192.png', '/icons/owner-192.png'];

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => syncAssets()).catch(() => {}));
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim()),
  );
});

async function syncAssets() {
  const res = await fetch('/.vite/manifest.json', { cache: 'no-store' });
  if (!res.ok) return;
  const manifest = await res.json();
  const wanted = new Set();
  for (const entry of Object.values(manifest)) {
    if (entry.file) wanted.add('/' + entry.file);
    for (const css of entry.css || []) wanted.add('/' + css);
    for (const asset of entry.assets || []) wanted.add('/' + asset);
  }
  const cache = await caches.open(CACHE);
  const have = new Set((await cache.keys()).map((r) => new URL(r.url).pathname));
  // drop chunks from previous builds
  await Promise.all([...have].filter((p) => p.startsWith('/assets/') && !wanted.has(p)).map((p) => cache.delete(p)));
  // fetch what is missing (a few at a time)
  const missing = [...wanted].filter((p) => !have.has(p));
  for (let i = 0; i < missing.length; i += 6) {
    await Promise.all(missing.slice(i, i + 6).map((p) => fetch(p).then((r) => (r.ok ? cache.put(p, r) : null)).catch(() => null)));
  }
}

self.addEventListener('message', (event) => {
  if (event.data === 'sync') event.waitUntil(syncAssets().catch(() => {}));
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== self.location.origin || url.pathname.startsWith('/api/') ||
      url.pathname.startsWith('/download/')) return; // never cache API calls or the Android APK

  // built assets have hashed names: cache first
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(
      caches.match(req).then((hit) => hit || fetch(req).then((res) => {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(req, copy));
        return res;
      })),
    );
    return;
  }
  // the boot script, manifests and icons: network first (they change rarely), cached copy offline
  if (SHELL.includes(url.pathname) && url.pathname !== '/') {
    event.respondWith(fetch(req).then((res) => { const copy = res.clone(); caches.open(CACHE).then((c) => c.put(req, copy)); return res; })
      .catch(() => caches.match(req)));
    return;
  }
  // pages: network first, fall back to the cached shell
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req).then((res) => {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put('/', copy));
        return res;
      }).catch(() => caches.match('/')),
    );
  }
});
