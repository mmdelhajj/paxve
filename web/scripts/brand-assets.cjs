// Builds every Paxve brand file from one source: the "two currencies" mark (dollar square + lira square in front of it,
// split by a white gap) and the Sora 700 wordmark outlined to paths (same data as src/components/Brand.tsx).
// Writes public/favicon.svg, public/brand/*.svg, public/icons/*.png (4 apps x 192/512 x any/maskable, apple-touch) and
// public/og.png. Run in the Playwright image:
//   docker run --rm -v /srv/paxve/web:/web -w /web/e2e -e NODE_PATH=/web/e2e/node_modules \
//     mcr.microsoft.com/playwright:v1.65.1-noble node /web/scripts/brand-assets.cjs
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');

const pub = path.join(__dirname, '..', 'public');
const src = fs.readFileSync(path.join(__dirname, '..', 'src', 'components', 'Brand.tsx'), 'utf8');
const pick = (n) => src.match(new RegExp(`export const ${n} = '([^']+)'`))[1];
const A = pick('MARK_A'), B = pick('MARK_B'), WORD = pick('WORD');

const NAVY = '#0B1F3A';
const TONES = {
  light: ['#041632', '#00929A'], // dollar square, lira square (colours sampled from the artwork)
  dark: ['#FFFFFF', '#2DD4BF'],
};
// one app = one colour on the lira square of its icon
const APPS = { main: '#2DD4BF', field: '#4ADE80', my: '#38BDF8', owner: '#FBBF24' };

const markG = ([m1, m3]) => `<path d="${A}" fill="${m1}"/><path d="${B}" fill="${m3}"/>`;
const monoG = (c) => `<path d="${A} ${B}" fill="${c}"/>`;
const svg = (vb, body, extra = '') => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${vb}"${extra}>${body}</svg>\n`;
// lockup like the approved artwork: mark (64 units) = 1.2 x cap height, caps centred on the mark, gap 0.31 cap
const CAP = 64 / 1.2, S = CAP / 730;
const wordG = (c) => `<path transform="translate(${(64 + 0.31 * CAP - 82 * S).toFixed(3)} ${(32 + CAP / 2).toFixed(3)}) scale(${S.toFixed(6)})" fill="${c}" d="${WORD}"/>`;
const LOCK_VB = '0 0 292 64';

function writeSvgs() {
  fs.mkdirSync(path.join(pub, 'brand'), { recursive: true });
  const w = (f, s) => fs.writeFileSync(path.join(pub, f), s);
  // favicon: colour mark; the dollar square turns white on dark browser tabs
  w('favicon.svg', svg('0 0 64 64', `<style>.a{fill:${TONES.light[0]}}.b{fill:${TONES.light[1]}}@media (prefers-color-scheme:dark){.a{fill:#FFFFFF}.b{fill:#2DD4BF}}</style><path class="a" d="${A}"/><path class="b" d="${B}"/>`));
  w('brand/paxve-mark.svg', svg('0 0 64 64', markG(TONES.light)));
  w('brand/paxve-mark-dark.svg', svg('0 0 64 64', markG(TONES.dark)));
  w('brand/paxve-mark-mono.svg', svg('0 0 64 64', monoG(TONES.light[0])));
  w('brand/paxve-mark-white.svg', svg('0 0 64 64', monoG('#FFFFFF')));
  w('brand/paxve-logo.svg', svg(LOCK_VB, markG(TONES.light) + wordG(TONES.light[0])));
  w('brand/paxve-logo-dark.svg', svg(LOCK_VB, markG(TONES.dark) + wordG('#FFFFFF')));
  w('brand/paxve-logo-mono.svg', svg(LOCK_VB, monoG(TONES.light[0]) + wordG(TONES.light[0])));
  w('brand/paxve-app-icon.svg', svg('0 0 64 64', `<rect width="64" height="64" rx="14" fill="${NAVY}"/><g transform="translate(13 13) scale(.594)">${markG(TONES.dark)}</g>`));
}

// an icon: navy ground (rounded for "any", full bleed for maskable / iOS) and the mark at `share` of the side
function iconHtml(size, lira, kind) {
  const share = kind === 'any' ? 0.6 : kind === 'apple' ? 0.56 : 0.5; // mark (64 units) / side
  const k = (share * size) / 64;
  const off = size / 2 - 32 * k;
  const ground = kind === 'any' ? `<rect width="${size}" height="${size}" rx="${size * 0.22}" fill="${NAVY}"/>` : `<rect width="${size}" height="${size}" fill="${NAVY}"/>`;
  const body = ground + `<g transform="translate(${off} ${off}) scale(${k})">${markG(['#FFFFFF', lira])}</g>`;
  return `<html><body style="margin:0;background:transparent">${svg(`0 0 ${size} ${size}`, body, ` width="${size}" height="${size}" style="display:block"`)}</body></html>`;
}

function ogHtml() {
  const f = (n) => 'file:///web/node_modules/@fontsource/readex-pro/files/' + n;
  return `<html><head><style>
  @font-face{font-family:Rx;font-weight:600;src:url(${f('readex-pro-arabic-600-normal.woff2')});unicode-range:U+0600-06FF,U+0750-077F,U+FB50-FDFF,U+FE70-FEFF,U+200C-200F}
  @font-face{font-family:Rx;font-weight:600;src:url(${f('readex-pro-latin-600-normal.woff2')})}
  @font-face{font-family:Rx;font-weight:500;src:url(${f('readex-pro-arabic-500-normal.woff2')});unicode-range:U+0600-06FF,U+0750-077F,U+FB50-FDFF,U+FE70-FEFF,U+200C-200F}
  @font-face{font-family:Rx;font-weight:500;src:url(${f('readex-pro-latin-500-normal.woff2')})}
  body{margin:0}
  .og{width:1200px;height:630px;background:${NAVY};position:relative;overflow:hidden;font-family:Rx,sans-serif;direction:rtl;color:#fff}
  .big{position:absolute;left:-150px;bottom:-190px;width:720px;height:720px;opacity:.07}
  .rule{position:absolute;right:88px;top:88px;bottom:88px;width:3px;background:#0EA5A4;border-radius:2px}
  .in{position:absolute;right:124px;top:96px;width:820px;display:flex;flex-direction:column;gap:30px}
  .lock{direction:ltr;align-self:flex-start;display:block}
  h1{margin:24px 0 0;font-weight:600;font-size:62px;line-height:1.3}
  h1 b{color:#5EEAD4;font-weight:600}
  p{margin:0;font-weight:500;font-size:27px;color:#B6C3D6;line-height:1.6}
  .url{position:absolute;right:124px;bottom:80px;font-weight:500;font-size:24px;color:#7EA2F5;direction:ltr}
  </style></head><body><div class="og">
  ${svg('0 0 64 64', markG(TONES.dark), ' class="big"')}
  <div class="rule"></div>
  <div class="in">
    ${svg(LOCK_VB, markG(TONES.dark) + wordG('#FFFFFF'), ' class="lock" width="438" height="96"')}
    <h1>نظام إدارة الأعمال<br><b>بالدولار والليرة</b></h1>
    <p>محاسبة · جباية · مخزون · نقاط بيع · متجر · تطبيقات للجابي والمالك</p>
  </div>
  <div class="url">paxve.com</div>
  </div></body></html>`;
}

(async () => {
  writeSvgs();
  const browser = await chromium.launch();
  const page = await browser.newPage({ deviceScaleFactor: 1 });
  const shot = async (html, w, h, file, transparent) => {
    await page.setViewportSize({ width: w, height: h });
    await page.setContent(html, { waitUntil: 'load' });
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: path.join(pub, file), omitBackground: transparent, clip: { x: 0, y: 0, width: w, height: h } });
    console.log('wrote', file);
  };
  for (const [app, lira] of Object.entries(APPS)) {
    for (const size of [192, 512]) {
      await shot(iconHtml(size, lira, 'any'), size, size, `icons/${app}-${size}.png`, true);
      await shot(iconHtml(size, lira, 'maskable'), size, size, `icons/${app}-${size}-maskable.png`, false);
    }
  }
  await shot(iconHtml(180, APPS.main, 'apple'), 180, 180, 'icons/apple-touch-icon.png', false);
  // og: file:// fonts need a file:// page
  const ogFile = '/tmp/paxve-og.html';
  fs.writeFileSync(ogFile, ogHtml());
  await page.setViewportSize({ width: 1200, height: 630 });
  await page.goto('file://' + ogFile);
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: path.join(pub, 'og.png'), clip: { x: 0, y: 0, width: 1200, height: 630 } });
  console.log('wrote og.png');
  await browser.close();
})();
