// Thermal receipt printer support.
// Arabic cannot be printed reliably with printer fonts, so the receipt is drawn on a canvas
// (the browser shapes Arabic and handles right-to-left) and sent as an ESC/POS raster image.
//
// Two transports:
//  * Android app (Capacitor, mobile/): native "PaxvePrinter" plugin → classic Bluetooth (SPP).
//    Most cheap 58/80mm printers are classic Bluetooth; pair them once in Android settings.
//  * Browsers: Web Bluetooth (BLE printers only) in Chrome / Edge on Android, Windows, macOS.
//    iPhone Safari has no Web Bluetooth.

export interface PrintLine {
  text: string;
  right?: string; // second column (amount), aligned to the other side
  bold?: boolean;
  size?: number; // font px, default 24
  align?: 'start' | 'center';
  rule?: boolean; // dashed separator
}

export interface PairedPrinter { name: string; address: string }

/* eslint-disable @typescript-eslint/no-explicit-any */

// ---------- native (Android app) ----------

function capacitor(): any {
  const c = (window as any).Capacitor;
  return c && typeof c.isNativePlatform === 'function' && c.isNativePlatform() ? c : null;
}

/** True inside the Paxve Android app (classic Bluetooth printing available). */
export function isNativeApp(): boolean {
  const c = capacitor();
  return !!c && (!!c.Plugins?.PaxvePrinter || typeof c.nativePromise === 'function');
}

function nativeCall<T>(method: string, options: Record<string, unknown> = {}): Promise<T> {
  const c = capacitor();
  if (!c) return Promise.reject(new Error('Not running in the Paxve app'));
  const plugin = c.Plugins?.PaxvePrinter;
  if (plugin && typeof plugin[method] === 'function') return plugin[method](options);
  return c.nativePromise('PaxvePrinter', method, options);
}

const SAVED = 'paxve.printer';
let nativeConnected: PairedPrinter | null = null;

function savedPrinter(): PairedPrinter | null {
  try {
    const v = localStorage.getItem(SAVED);
    return v ? (JSON.parse(v) as PairedPrinter) : null;
  } catch {
    return null;
  }
}

export async function listPairedPrinters(): Promise<PairedPrinter[]> {
  const r = await nativeCall<{ devices: PairedPrinter[] }>('listPaired');
  return r.devices ?? [];
}

export async function connectNativePrinter(p: PairedPrinter): Promise<string> {
  const r = await nativeCall<{ name: string; address: string }>('connect', { address: p.address });
  nativeConnected = { name: r.name || p.name, address: p.address };
  try {
    localStorage.setItem(SAVED, JSON.stringify(nativeConnected));
  } catch {
    /* ignore */
  }
  return nativeConnected.name;
}

/** Thrown when the app needs the user to choose a paired printer. */
export class ChoosePrinterError extends Error {
  constructor() {
    super('choose_printer');
  }
}

// ---------- Web Bluetooth (BLE) ----------

// Common BLE printer services (generic Chinese 58/80mm printers, Xprinter, Rongta, MTP...).
const SERVICES = [
  '000018f0-0000-1000-8000-00805f9b34fb',
  'e7810a71-73ae-499d-8c15-faa9aef0c3f2',
  '49535343-fe7d-4ae5-8fa9-9fafd205e455',
  '0000ff00-0000-1000-8000-00805f9b34fb',
  '0000ffe0-0000-1000-8000-00805f9b34fb',
];

let characteristic: any = null;
let deviceName = '';

export function bluetoothSupported(): boolean {
  return isNativeApp() || (typeof navigator !== 'undefined' && 'bluetooth' in navigator);
}

export function printerName(): string {
  if (isNativeApp()) return nativeConnected?.name ?? '';
  return characteristic ? deviceName : '';
}

export function paperWidth(): number {
  try {
    return localStorage.getItem('paxve.paper') === '80' ? 576 : 384;
  } catch {
    return 384;
  }
}

export function setPaper(mm: '58' | '80') {
  try {
    localStorage.setItem('paxve.paper', mm);
  } catch {
    /* ignore */
  }
}

/** Browser only: opens the Web Bluetooth device chooser. In the app use listPairedPrinters + connectNativePrinter. */
export async function connectPrinter(): Promise<string> {
  if (isNativeApp()) throw new ChoosePrinterError();
  const bt = (navigator as any).bluetooth;
  const device = await bt.requestDevice({ acceptAllDevices: true, optionalServices: SERVICES });
  const server = await device.gatt.connect();
  for (const uuid of SERVICES) {
    try {
      const service = await server.getPrimaryService(uuid);
      const chars = await service.getCharacteristics();
      const w = chars.find((c: any) => c.properties.write || c.properties.writeWithoutResponse);
      if (w) {
        characteristic = w;
        deviceName = device.name || 'Printer';
        device.addEventListener('gattserverdisconnected', () => { characteristic = null; });
        return deviceName;
      }
    } catch {
      /* try next service */
    }
  }
  throw new Error('No printable characteristic found on this device');
}

// ---------- rendering ----------

/** Draws the lines on a canvas of the printer width. */
export function renderReceipt(lines: PrintLine[], width = paperWidth(), rtl = document.dir === 'rtl'): HTMLCanvasElement {
  const pad = 8;
  let height = pad;
  for (const l of lines) height += l.rule ? 16 : Math.round((l.size ?? 24) * 1.35);
  height += 40; // feed margin
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext('2d')!;
  ctx.fillStyle = '#fff';
  ctx.fillRect(0, 0, width, height);
  ctx.fillStyle = '#000';
  ctx.textBaseline = 'top';
  ctx.direction = rtl ? 'rtl' : 'ltr';
  const font = '"IBM Plex Sans Arabic", Tahoma, Arial, sans-serif';
  let y = pad;
  for (const l of lines) {
    if (l.rule) {
      ctx.fillRect(pad, y + 7, width - pad * 2, 2);
      y += 16;
      continue;
    }
    const size = l.size ?? 24;
    ctx.font = `${l.bold ? 700 : 400} ${size}px ${font}`;
    const startX = rtl ? width - pad : pad;
    const endX = rtl ? pad : width - pad;
    if (l.align === 'center') {
      ctx.textAlign = 'center';
      ctx.fillText(l.text, width / 2, y, width - pad * 2);
    } else {
      ctx.textAlign = 'start';
      ctx.fillText(l.text, startX, y, l.right ? width * 0.6 : width - pad * 2);
      if (l.right) {
        ctx.save();
        ctx.direction = 'ltr';
        ctx.textAlign = rtl ? 'left' : 'right';
        ctx.fillText(l.right, endX, y);
        ctx.restore();
      }
    }
    y += Math.round(size * 1.35);
  }
  return canvas;
}

/** Converts a canvas to ESC/POS bytes: init, "GS v 0" raster image, feed and partial cut. */
export function toEscPos(canvas: HTMLCanvasElement): Uint8Array {
  const ctx = canvas.getContext('2d')!;
  const { width, height } = canvas;
  const img = ctx.getImageData(0, 0, width, height).data;
  const bytesPerRow = Math.ceil(width / 8);
  const head = [
    0x1b, 0x40, // ESC @ init
    0x1d, 0x76, 0x30, 0x00, // GS v 0, normal size
    bytesPerRow & 0xff, (bytesPerRow >> 8) & 0xff,
    height & 0xff, (height >> 8) & 0xff,
  ];
  const tail = [0x1b, 0x64, 0x03, 0x1d, 0x56, 0x01]; // feed 3 lines + partial cut
  const out = new Uint8Array(head.length + bytesPerRow * height + tail.length);
  out.set(head, 0);
  const body = head.length;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      const lum = 0.299 * img[i] + 0.587 * img[i + 1] + 0.114 * img[i + 2];
      if (lum < 128) out[body + y * bytesPerRow + (x >> 3)] |= 0x80 >> (x & 7);
    }
  }
  out.set(tail, body + bytesPerRow * height);
  return out;
}

/** ESC/POS bytes for a receipt (shared by both transports). */
export function receiptBytes(lines: PrintLine[]): Uint8Array {
  return toEscPos(renderReceipt(lines));
}

function toBase64(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

// ---------- printing ----------

export async function printLines(lines: PrintLine[]): Promise<void> {
  const data = receiptBytes(lines);
  if (isNativeApp()) {
    if (!nativeConnected) {
      const saved = savedPrinter();
      if (!saved) throw new ChoosePrinterError();
      await connectNativePrinter(saved);
    }
    try {
      await nativeCall('write', { data: toBase64(data) });
    } catch {
      // the printer may have been switched off: reconnect once and retry
      nativeConnected = null;
      const saved = savedPrinter();
      if (!saved) throw new ChoosePrinterError();
      await connectNativePrinter(saved);
      await nativeCall('write', { data: toBase64(data) });
    }
    return;
  }
  if (!characteristic) await connectPrinter();
  const chunk = 180; // below typical BLE MTU
  for (let i = 0; i < data.length; i += chunk) {
    const part = data.slice(i, i + chunk);
    if (characteristic.properties.writeWithoutResponse && characteristic.writeValueWithoutResponse) {
      await characteristic.writeValueWithoutResponse(part);
      await new Promise((r) => setTimeout(r, 12));
    } else {
      await characteristic.writeValue(part);
    }
  }
}
