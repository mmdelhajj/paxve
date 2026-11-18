// Bluetooth (BLE) thermal printer support for receipts.
// Arabic cannot be printed reliably with printer fonts, so the receipt is drawn on a canvas
// (the browser shapes Arabic and handles right-to-left) and sent as an ESC/POS raster image.
// Works in Chrome / Edge on Android, Windows, macOS, ChromeOS (Web Bluetooth). iPhone Safari has no Web Bluetooth.

export interface PrintLine {
  text: string;
  right?: string; // second column (amount), aligned to the other side
  bold?: boolean;
  size?: number; // font px, default 24
  align?: 'start' | 'center';
  rule?: boolean; // dashed separator
}

// Common BLE printer services (generic Chinese 58/80mm printers, Xprinter, Rongta, MTP...).
const SERVICES = [
  '000018f0-0000-1000-8000-00805f9b34fb',
  'e7810a71-73ae-499d-8c15-faa9aef0c3f2',
  '49535343-fe7d-4ae5-8fa9-9fafd205e455',
  '0000ff00-0000-1000-8000-00805f9b34fb',
  '0000ffe0-0000-1000-8000-00805f9b34fb',
];

/* eslint-disable @typescript-eslint/no-explicit-any */
let characteristic: any = null;
let deviceName = '';

export function bluetoothSupported(): boolean {
  return typeof navigator !== 'undefined' && 'bluetooth' in navigator;
}

export function printerName(): string {
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

export async function connectPrinter(): Promise<string> {
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

/** Draws the lines on a canvas of the printer width. */
export function renderReceipt(lines: PrintLine[], width = paperWidth(), rtl = document.dir === 'rtl'): HTMLCanvasElement {
  const pad = 8;
  const measure = document.createElement('canvas').getContext('2d')!;
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
    measure.font = ctx.font;
    const startX = rtl ? width - pad : pad;
    const endX = rtl ? pad : width - pad;
    if (l.align === 'center') {
      ctx.textAlign = 'center';
      ctx.fillText(l.text, width / 2, y, width - pad * 2);
    } else {
      ctx.textAlign = 'start';
      ctx.fillText(l.text, startX, y, l.right ? width * 0.6 : width - pad * 2);
      if (l.right) {
        ctx.textAlign = 'end';
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

/** Converts a canvas to ESC/POS "GS v 0" raster bytes. */
function toEscPos(canvas: HTMLCanvasElement): Uint8Array {
  const ctx = canvas.getContext('2d')!;
  const { width, height } = canvas;
  const img = ctx.getImageData(0, 0, width, height).data;
  const bytesPerRow = Math.ceil(width / 8);
  const out = new Uint8Array(8 + bytesPerRow * height + 2 + 6);
  let o = 0;
  out[o++] = 0x1b; out[o++] = 0x40; // ESC @ init
  out[o++] = 0x1d; out[o++] = 0x76; out[o++] = 0x30; out[o++] = 0x00; // GS v 0
  out[o++] = bytesPerRow & 0xff; out[o++] = (bytesPerRow >> 8) & 0xff;
  const header = new Uint8Array([height & 0xff, (height >> 8) & 0xff]);
  const body = new Uint8Array(bytesPerRow * height);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      const lum = 0.299 * img[i] + 0.587 * img[i + 1] + 0.114 * img[i + 2];
      if (lum < 128) body[y * bytesPerRow + (x >> 3)] |= 0x80 >> (x & 7);
    }
  }
  const result = new Uint8Array(o + 2 + body.length + 6);
  result.set(out.subarray(0, o), 0);
  result.set(header, o);
  result.set(body, o + 2);
  result.set([0x1b, 0x64, 0x03, 0x1d, 0x56, 0x01], o + 2 + body.length); // feed 3 lines + partial cut
  return result;
}

export async function printLines(lines: PrintLine[]): Promise<void> {
  if (!characteristic) await connectPrinter();
  const data = toEscPos(renderReceipt(lines));
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
