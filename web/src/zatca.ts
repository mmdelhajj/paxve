import QRCode from 'qrcode';

/**
 * Saudi e-invoicing (ZATCA / FATOORA) phase 1: the QR code on a simplified tax invoice carries five TLV
 * fields — seller name, VAT number, time stamp (ISO 8601), invoice total with VAT, VAT total — base64-encoded.
 * Phase 2 (signed XML cleared through the ZATCA API) is a separate integration (docs/LOCALIZATION.md).
 */
export function zatcaTLV(seller: string, vatNo: string, timestamp: string, total: string, vat: string): string {
  const enc = new TextEncoder();
  const parts: number[] = [];
  [seller, vatNo, timestamp, total, vat].forEach((v, i) => {
    const bytes = enc.encode(v);
    if (bytes.length > 255) throw new Error('zatca: field too long');
    parts.push(i + 1, bytes.length, ...bytes);
  });
  let bin = '';
  for (const b of parts) bin += String.fromCharCode(b);
  return btoa(bin);
}

export function zatcaQR(seller: string, vatNo: string, timestamp: string, total: string, vat: string): Promise<string> {
  return QRCode.toDataURL(zatcaTLV(seller, vatNo, timestamp, total, vat), { margin: 1, width: 150, errorCorrectionLevel: 'M' });
}
