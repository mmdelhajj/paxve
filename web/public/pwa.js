// Paxve installable apps (phase 6b). Runs before the app code (a plain script: the CSP allows only same-origin files).
// One site, four apps: the main system, "Paxve Field" (collector, driver, technician, van), the customer's "My account"
// and "Paxve Owner". Each has its own manifest; the start addresses /field, /my and /owner open the right screen.
(function () {
  var path = location.pathname.replace(/\/+$/, '');
  var starts = { '/field': '#/collect', '/my': '#/my', '/owner': '#/owner' };
  if (starts[path]) {
    try { sessionStorage.setItem('paxve.app', path.slice(1)); } catch (e) { /* storage blocked */ }
    location.replace('/' + (location.hash && location.hash.length > 2 ? location.hash : starts[path]));
    return;
  }
  var h = location.hash.replace(/^#\/?/, '').split('/')[0];
  var app = 'main';
  if (h === 'collect' || h === 'driver' || h === 'tech' || h === 'van' || h === 'scan') app = 'field';
  else if (h === 'p' || h === 'my') app = 'my';
  else if (h === 'owner') app = 'owner';
  else {
    try { app = sessionStorage.getItem('paxve.app') || 'main'; } catch (e) { app = 'main'; }
    if (app !== 'field' && app !== 'my' && app !== 'owner') app = 'main';
  }
  var link = document.querySelector('link[rel="manifest"]');
  if (link && app !== 'main') link.setAttribute('href', '/manifest-' + app + '.webmanifest');
  document.documentElement.setAttribute('data-app', app);
  // keep the browser's install offer for the app's own "Install" button. No preventDefault(): Chrome then logs
  // "Banner not shown" on every page; letting the browser show its own install icon too is fine, and prompt()
  // still works from our button.
  window.__pxvInstall = null;
  window.addEventListener('beforeinstallprompt', function (e) {
    window.__pxvInstall = e;
    window.dispatchEvent(new Event('paxve:installable'));
  });
  window.addEventListener('appinstalled', function () {
    window.__pxvInstall = null;
    window.dispatchEvent(new Event('paxve:installable'));
  });
})();
