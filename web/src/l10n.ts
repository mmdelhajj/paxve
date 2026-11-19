/** Country packs (internal/charts): chart of accounts, taxes, invoice rules and currency per country. */
export interface Pack { key: string; country: string; base: string; local: string }

export const PACKS: Pack[] = [
  { key: 'lebanon', country: 'LB', base: 'USD', local: 'LBP' },
  { key: 'sa', country: 'SA', base: 'SAR', local: 'SAR' },
  { key: 'ae', country: 'AE', base: 'AED', local: 'AED' },
  { key: 'kw', country: 'KW', base: 'KWD', local: 'KWD' },
  { key: 'qa', country: 'QA', base: 'QAR', local: 'QAR' },
  { key: 'bh', country: 'BH', base: 'BHD', local: 'BHD' },
  { key: 'om', country: 'OM', base: 'OMR', local: 'OMR' },
  { key: 'jo', country: 'JO', base: 'JOD', local: 'JOD' },
  { key: 'sy', country: 'SY', base: 'USD', local: 'SYP' },
  { key: 'iq', country: 'IQ', base: 'USD', local: 'IQD' },
  { key: 'eg', country: 'EG', base: 'EGP', local: 'EGP' },
  { key: 'ma', country: 'MA', base: 'MAD', local: 'MAD' },
  { key: 'dz', country: 'DZ', base: 'DZD', local: 'DZD' },
  { key: 'tn', country: 'TN', base: 'TND', local: 'TND' },
  { key: 'tr', country: 'TR', base: 'TRY', local: 'TRY' },
  { key: 'fr', country: 'FR', base: 'EUR', local: 'EUR' },
  { key: 'gb', country: 'GB', base: 'GBP', local: 'GBP' },
  { key: 'us', country: 'US', base: 'USD', local: 'USD' },
  { key: 'ifrs', country: '', base: 'USD', local: 'USD' },
];

/** the country's name in the reader's language (the browser knows every country name) */
export function packName(p: Pack, lang: string, other: string): string {
  if (!p.country) return other;
  try {
    return new Intl.DisplayNames([lang], { type: 'region' }).of(p.country) ?? p.country;
  } catch {
    return p.country;
  }
}
