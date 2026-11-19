// Amount in Arabic words (تفقيط), as written on Lebanese paper receipts:
// 1250.5 USD → "ألف ومئتان وخمسون دولاراً أميركياً وخمسون سنتاً فقط لا غير" (simplified grammar, no case endings).

const ONES = ['', 'واحد', 'اثنان', 'ثلاثة', 'أربعة', 'خمسة', 'ستة', 'سبعة', 'ثمانية', 'تسعة', 'عشرة',
  'أحد عشر', 'اثنا عشر', 'ثلاثة عشر', 'أربعة عشر', 'خمسة عشر', 'ستة عشر', 'سبعة عشر', 'ثمانية عشر', 'تسعة عشر'];
const TENS = ['', '', 'عشرون', 'ثلاثون', 'أربعون', 'خمسون', 'ستون', 'سبعون', 'ثمانون', 'تسعون'];
const HUNDREDS = ['', 'مئة', 'مئتان', 'ثلاثمئة', 'أربعمئة', 'خمسمئة', 'ستمئة', 'سبعمئة', 'ثمانمئة', 'تسعمئة'];

function below1000(n: number): string {
  const h = Math.floor(n / 100);
  const r = n % 100;
  const parts: string[] = [];
  if (h) parts.push(HUNDREDS[h]);
  if (r) {
    if (r < 20) parts.push(ONES[r]);
    else {
      const u = r % 10;
      const tn = Math.floor(r / 10);
      parts.push(u ? `${ONES[u]} و${TENS[tn]}` : TENS[tn]);
    }
  }
  return parts.join(' و');
}

// [singular, dual, plural (3-10), accusative singular (11+)]
const SCALES: [string, string, string, string][] = [
  ['', '', '', ''],
  ['ألف', 'ألفان', 'آلاف', 'ألفاً'],
  ['مليون', 'مليونان', 'ملايين', 'مليوناً'],
  ['مليار', 'ملياران', 'مليارات', 'ملياراً'],
];

/** Whole number in Arabic words (0 … 999,999,999,999). */
export function arabicWords(n: number): string {
  n = Math.floor(Math.abs(n));
  if (n === 0) return 'صفر';
  const groups: number[] = [];
  while (n > 0) { groups.push(n % 1000); n = Math.floor(n / 1000); }
  const parts: string[] = [];
  for (let i = groups.length - 1; i >= 0; i--) {
    const g = groups[i];
    if (!g) continue;
    if (i === 0) { parts.push(below1000(g)); continue; }
    const [one, two, few, many] = SCALES[i];
    if (g === 1) parts.push(one);
    else if (g === 2) parts.push(two);
    else if (g >= 3 && g <= 10) parts.push(`${below1000(g)} ${few}`);
    else parts.push(`${below1000(g)} ${many}`);
  }
  return parts.join(' و');
}

const NAMES: Record<string, { main: [string, string, string, string]; sub?: [string, string, string, string]; subUnits: number }> = {
  // [1, 2, 3-10, 11+]
  USD: { main: ['دولار أميركي', 'دولاران أميركيان', 'دولارات أميركية', 'دولاراً أميركياً'], sub: ['سنت', 'سنتان', 'سنتات', 'سنتاً'], subUnits: 100 },
  LBP: { main: ['ليرة لبنانية', 'ليرتان لبنانيتان', 'ليرات لبنانية', 'ليرة لبنانية'], subUnits: 1 },
  EUR: { main: ['يورو', 'يورو', 'يورو', 'يورو'], sub: ['سنت', 'سنتان', 'سنتات', 'سنتاً'], subUnits: 100 },
};

function counted(n: number, forms: [string, string, string, string]): string {
  const last2 = n % 100;
  if (n === 1) return forms[0];
  if (n === 2) return forms[1];
  const words = arabicWords(n);
  if (last2 >= 3 && last2 <= 10) return `${words} ${forms[2]}`;
  if (last2 === 0 || last2 === 1 || last2 === 2) return `${words} ${forms[0]}`;
  return `${words} ${forms[3]}`;
}

/** "مئة وخمسون دولاراً أميركياً فقط لا غير" for (150, 'USD'). Unknown currencies keep their code. */
export function amountInWords(amount: number | string, currency: string): string {
  const v = Math.abs(Number(amount) || 0);
  const info = NAMES[currency];
  const whole = Math.floor(v + 1e-9);
  if (!info) return `${arabicWords(whole)} ${currency} فقط لا غير`;
  let text = whole ? counted(whole, info.main) : '';
  if (info.sub && info.subUnits > 1) {
    const cents = Math.round((v - whole) * info.subUnits);
    if (cents) text = (text ? text + ' و' : '') + counted(cents, info.sub);
  }
  return `${text || 'صفر'} فقط لا غير`;
}
