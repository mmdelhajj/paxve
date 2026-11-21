// Builds every Paxve brand file from one source: the owner's wordmark "Paxve" with the teal swoosh x (traced to paths,
// data in src/components/Brand.tsx); the small mark is the swoosh x on a navy badge.
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
const BADGE = pick('MARK_BADGE'), INK = pick('WORD_INK'), X = pick('WORD_X'), XINK = pick('X_INK');
const TRACE = pick('TRACE'), X_FIT = pick('X_FIT'), WORD_VB = pick('WORD_VB');

const NAVY = '#0D2743', TEAL = '#06A4A2', TEAL_LIGHT = '#2DD4BF';
// one app = one colour on the swoosh of its icon
const APPS = { main: TEAL_LIGHT, field: '#4ADE80', my: '#38BDF8', owner: '#FBBF24' };

const svg = (vb, body, extra = '') => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${vb}"${extra}>${body}</svg>\n`;
const wordG = (ink, x) => `<g transform="${TRACE}"><path d="${INK}" fill="${ink}"/><path d="${X}" fill="${x}"/></g>`;
const xG = (ink, x) => `<g transform="${TRACE}"><path d="${XINK}" fill="${ink}"/><path d="${X}" fill="${x}"/></g>`;
const markG = (badge, ink, x) => `<path d="${BADGE}" fill="${badge}"/><g transform="${X_FIT}">${xG(ink, x)}</g>`;

function writeSvgs() {
  fs.mkdirSync(path.join(pub, 'brand'), { recursive: true });
  const w = (f, s) => fs.writeFileSync(path.join(pub, f), s);
  w('favicon.svg', svg('0 0 64 64', markG(NAVY, '#FFFFFF', TEAL_LIGHT)));
  w('brand/paxve-mark.svg', svg('0 0 64 64', markG(NAVY, '#FFFFFF', TEAL_LIGHT)));
  w('brand/paxve-mark-dark.svg', svg('0 0 64 64', markG('#FFFFFF', NAVY, TEAL)));
  w('brand/paxve-mark-mono.svg', svg('0 0 64 64', markG(NAVY, '#FFFFFF', '#FFFFFF')));
  w('brand/paxve-mark-white.svg', svg('0 0 64 64', markG('#FFFFFF', NAVY, NAVY)));
  w('brand/paxve-logo.svg', svg(WORD_VB, wordG(NAVY, TEAL)));
  w('brand/paxve-logo-dark.svg', svg(WORD_VB, wordG('#FFFFFF', TEAL_LIGHT)));
  w('brand/paxve-logo-mono.svg', svg(WORD_VB, wordG(NAVY, NAVY)));
  w('brand/paxve-app-icon.svg', svg('0 0 64 64', markG(NAVY, '#FFFFFF', TEAL_LIGHT)));
}

// an icon: navy ground (rounded for "any", full bleed for maskable / iOS) and the swoosh x at `share` of the side
function iconHtml(size, swoosh, kind) {
  const share = kind === 'any' ? 0.64 : kind === 'apple' ? 0.6 : 0.5; // x width (1125 artwork px) / side
  const k = (share * size) / 1125;
  const tx = size / 2 - (1780 + 562.5) * k, ty = size / 2 - (289 + 439.5) * k;
  const ground = kind === 'any' ? `<rect width="${size}" height="${size}" rx="${size * 0.23}" fill="${NAVY}"/>` : `<rect width="${size}" height="${size}" fill="${NAVY}"/>`;
  const body = ground + `<g transform="translate(${tx} ${ty}) scale(${k})">${xG('#FFFFFF', swoosh)}</g>`;
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
  ${svg('0 0 64 64', markG('#FFFFFF', NAVY, NAVY), ' class="big"')}
  <div class="rule"></div>
  <div class="in">
    ${svg(WORD_VB, wordG('#FFFFFF', TEAL_LIGHT), ' class="lock" width="336" height="85"')}
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
