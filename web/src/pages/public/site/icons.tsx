// Line icons for the marketing site: one 24px grid, one stroke width, round joins. Drawn for Paxve (no icon font,
// no emoji) so every business type and feature reads in the same visual language.
import type { ReactNode, SVGProps } from 'react';

const P: Record<string, ReactNode> = {
  // business types
  generator: <><rect x="3" y="7" width="18" height="12" rx="2" /><path d="M7 7V5h4v2M13 11l-2 3h3l-2 3" /><path d="M17 11h1M17 14h1" /></>,
  internet: <><path d="M2.5 9.5a14 14 0 0 1 19 0M5.5 12.8a9.5 9.5 0 0 1 13 0M8.6 16a5 5 0 0 1 6.8 0" /><circle cx="12" cy="19" r="1.2" /></>,
  satellite: <><path d="M5 13a7 7 0 0 0 7 7" /><path d="M4.5 9.5 14.5 19.5a9 9 0 0 1-10-10Z" /><path d="M12.5 11.5 16 8M17 4a3 3 0 0 1 3 3" /><path d="M17 1a6 6 0 0 1 6 6" opacity=".5" /></>,
  water: <><path d="M12 3s6 6.4 6 11a6 6 0 0 1-12 0c0-4.6 6-11 6-11Z" /><path d="M9 15a3 3 0 0 0 3 3" /></>,
  supermarket: <><path d="M3 4h2l2.2 10.2a1.5 1.5 0 0 0 1.5 1.2h8.6a1.5 1.5 0 0 0 1.5-1.1L21 8H6.2" /><circle cx="9.5" cy="19.5" r="1.3" /><circle cx="17" cy="19.5" r="1.3" /></>,
  clothing: <><path d="M9 3a3 3 0 0 0 6 0l5 2.5-2 4.5-2-1V21H8V9l-2 1-2-4.5Z" /></>,
  electronics: <><rect x="6.5" y="2.5" width="11" height="19" rx="2.5" /><path d="M10.5 18.5h3" /><path d="M9.5 6h5" opacity=".5" /></>,
  furniture: <><path d="M5 11V8a3 3 0 0 1 3-3h8a3 3 0 0 1 3 3v3" /><path d="M3 13a2 2 0 0 1 4 0v2h10v-2a2 2 0 0 1 4 0v4a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1Z" /><path d="M5 18v2M19 18v2" /></>,
  wholesale: <><path d="M2.5 6.5h11v9h-11zM13.5 9.5h4l3 3v3h-7" /><circle cx="6.5" cy="17.5" r="1.8" /><circle cx="16.5" cy="17.5" r="1.8" /></>,
  restaurant: <><path d="M7 3v8M4.5 3v4.5a2.5 2.5 0 0 0 5 0V3M7 11v10" /><path d="M17 21V3c-2.2 1.2-3.5 3.8-3.5 7v3H17" /></>,
  pharmacy: <><rect x="3.2" y="8.5" width="17.6" height="7" rx="3.5" transform="rotate(-45 12 12)" /><path d="m9.5 9.5 5 5" /></>,
  factory: <><path d="M3 21V10l5 3V10l5 3V6l4-3v18" /><path d="M3 21h18M17 21V9h4v12" /><path d="M7 17h2M11 17h2" /></>,
  // features
  money: <><circle cx="9" cy="9" r="5.5" /><path d="M15.2 9.6A5.5 5.5 0 1 1 9.6 15.2" /><path d="M9 6.5v5M7.5 8h2.2a.9.9 0 0 1 0 1.8H8.3a.9.9 0 0 0 0 1.8h2.2" /></>,
  field: <><rect x="5" y="2.5" width="10" height="19" rx="2.2" /><path d="M18.5 7.5a2.5 2.5 0 0 1 2.5 2.5c0 2-2.5 4.5-2.5 4.5S16 12 16 10a2.5 2.5 0 0 1 2.5-2.5Z" /><path d="M9 18.5h2" /></>,
  stock: <><path d="M12 3 20 7v10l-8 4-8-4V7Z" /><path d="m4 7 8 4 8-4M12 11v10" /><path d="m8 5 8 4" opacity=".5" /></>,
  mfg: <><circle cx="12" cy="12" r="3" /><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1" /></>,
  smart: <><path d="M12 3 13.8 8.2 19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8Z" /><path d="M19 15.5 19.8 17.7 22 18.5l-2.2.8L19 21.5l-.8-2.2-2.2-.8 2.2-.8Z" /></>,
  shop: <><path d="M5 8h14l-1.2 12H6.2Z" /><path d="M9 8V6.5a3 3 0 0 1 6 0V8" /></>,
  esign: <><path d="M3 20c2-1 3.5-3.2 5-3.2s1 2.2 2.5 2.2 2.4-2 4-2 1.5 1.5 2.5 1.5" /><path d="m14 3 4 4-8 8-4.5.5L6 11Z" /></>,
  apps: <><rect x="3" y="3" width="7.5" height="7.5" rx="2" /><rect x="13.5" y="3" width="7.5" height="7.5" rx="2" /><rect x="3" y="13.5" width="7.5" height="7.5" rx="2" /><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="3.75" /></>,
  security: <><path d="M12 2.8 19.5 5.5v6c0 4.6-3.2 8.3-7.5 9.7-4.3-1.4-7.5-5.1-7.5-9.7v-6Z" /><path d="m8.8 12 2.3 2.3 4.2-4.6" /></>,
  // utility
  check: <path d="m5 12.5 4.5 4.5L19 7.5" />,
  close: <path d="M6 6l12 12M18 6 6 18" />,
  menu: <path d="M4 7h16M4 12h16M4 17h16" />,
  arrow: <path d="M5 12h14M13 6l6 6-6 6" />,
  chevron: <path d="m6 9 6 6 6-6" />,
  globe: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3c2.5 2.8 3.7 5.8 3.7 9S14.5 18.2 12 21c-2.5-2.8-3.7-5.8-3.7-9S9.5 5.8 12 3Z" /></>,
  lock: <><rect x="4.5" y="10.5" width="15" height="10" rx="2.5" /><path d="M8 10.5V7.5a4 4 0 0 1 8 0v3M12 14.5v2" /></>,
  cloud: <><path d="M7 18.5a4.5 4.5 0 0 1-.5-9 6 6 0 0 1 11.4 1.6A3.8 3.8 0 0 1 17.5 18.5Z" /><path d="m9.5 13.5 2 2 3.5-3.5" /></>,
  users: <><circle cx="9" cy="8" r="3.5" /><path d="M2.5 20a6.5 6.5 0 0 1 13 0" /><path d="M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14a6.5 6.5 0 0 1 3.5 6" /></>,
  offline: <><path d="M2.5 9.5a14 14 0 0 1 5-3.1M10.6 5.6a14 14 0 0 1 10.9 3.9M5.5 12.8a9.5 9.5 0 0 1 3.4-2.1M14.5 10.8a9.5 9.5 0 0 1 4 2M8.6 16a5 5 0 0 1 6.8 0" /><path d="M3 3l18 18" /><circle cx="12" cy="19" r="1.1" /></>,
  chat: <><path d="M4 18.5 5.2 15A7.8 7.8 0 1 1 8.5 18Z" /><path d="M9 10.5c.4 1.7 1.8 3.1 3.5 3.5l1.1-1.1 1.9.9-.4 1.6c-3.4.4-7.4-3.5-7-6.9l1.6-.4.9 1.9Z" /></>,
  wallet: <><path d="M4 7.5a2 2 0 0 1 2-2h11.5v3" /><rect x="4" y="8.5" width="16.5" height="11" rx="2" /><circle cx="16.5" cy="14" r="1.2" /></>,
  language: <><path d="M4 5.5h9M8.5 4v1.5c0 4-2 7-4.5 8.5M6 9.5c1.3 2 3.5 3.8 6 4.5" /><path d="m13 20 3.8-9 3.8 9M14.3 17h5" /></>,
  receipt: <><path d="M6 3h12v18l-2-1.4L14 21l-2-1.4L10 21l-2-1.4L6 21Z" /><path d="M9 8h6M9 11.5h6M9 15h3.5" /></>,
  history: <><path d="M3.5 12a8.5 8.5 0 1 0 2.5-6" /><path d="M3.5 4.5V8H7" /><path d="M12 8v4.5l3 1.8" /></>,
  play: <><circle cx="12" cy="12" r="9" /><path d="m10 8.5 5.5 3.5-5.5 3.5Z" /></>,
};

export type IconName = keyof typeof P;

export function Icon({ name, size = 24, ...rest }: { name: string; size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false" {...rest}>
      {P[name] ?? P.apps}
    </svg>
  );
}
