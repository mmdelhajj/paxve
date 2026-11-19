// Window navigation for the Magic-style shell. A plain hash change reuses the current window (like a link);
// openScreen() asks the shell for a new window (ribbon tiles, F2, "open" from a grid).

let wantNew = false;

export function openScreen(page: string, param = '') {
  wantNew = true;
  const h = `#/${page}${param ? '/' + param : ''}`;
  if (window.location.hash === h) {
    wantNew = false;
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    return;
  }
  window.location.hash = h;
}

/** Read and clear the "new window" request (called by the shell on every hash change). */
export function takeNewWindowRequest(): boolean {
  const v = wantNew;
  wantNew = false;
  return v;
}
