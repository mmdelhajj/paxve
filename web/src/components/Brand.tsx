// The Paxve mark "two currencies": a dollar square and a lira square; where they overlap is the exchange rate.
// Colours come from CSS variables (brand.css) so the mark follows light / dark; the wordmark is Sora 700 outlined to
// paths (no font download needed).
import type { CSSProperties } from 'react';
import './brand.css';

export const MARK_A = 'M15 8h18a7 7 0 0 1 7 7v18a7 7 0 0 1-7 7H15a7 7 0 0 1-7-7V15a7 7 0 0 1 7-7z';
export const MARK_B = 'M31 24h18a7 7 0 0 1 7 7v18a7 7 0 0 1-7 7H31a7 7 0 0 1-7-7V31a7 7 0 0 1 7-7z';
export const MARK_X = 'M24 31a7 7 0 0 1 7-7h9v9a7 7 0 0 1-7 7h-9z';
// "Paxve", Sora 700, tracking -0.02em, 1000 units per em, baseline at y = 0 (cap height 730)
export const WORD = 'M226 -209V-350.5H356.75Q395.25 -350.5 422.5 -365.75Q449.75 -381 464.625 -408.5Q479.5 -436 479.5 -472.25Q479.5 -509.5 464.625 -537Q449.75 -564.5 422.5 -579.25Q395.25 -594 356.75 -594H226V-735.5H348.5Q441.5 -735.5 508 -704.75Q574.5 -674 610 -616.75Q645.5 -559.5 645.5 -480.25V-464.25Q645.5 -386 610 -328.625Q574.5 -271.25 508 -240.125Q441.5 -209 348.5 -209ZM82 0V-735.5H246V0Z M1043.75 0V-161.5H1017.25V-337.5Q1017.25 -379.75 997 -400.625Q976.75 -421.5 933 -421.5Q911 -421.5 875 -420.5Q839 -419.5 800.75 -417.75Q762.5 -416 731.5 -414V-549Q755 -551 785.875 -552.875Q816.75 -554.75 849.875 -555.625Q883 -556.5 911.5 -556.5Q995.75 -556.5 1053.375 -532.875Q1111 -509.25 1140.875 -460.75Q1170.75 -412.25 1170.75 -335.75V0ZM868.75 14Q810.25 14 765.625 -6.875Q721 -27.75 696 -67.125Q671 -106.5 671 -161.5Q671 -222.25 702.125 -260.625Q733.25 -299 789 -317.375Q844.75 -335.75 918.25 -335.75H1034.75V-247.25H917.25Q875 -247.25 852.25 -226.375Q829.5 -205.5 829.5 -171.25Q829.5 -137.75 852.25 -117.125Q875 -96.5 917.25 -96.5Q943.75 -96.5 965.375 -106Q987 -115.5 1001.125 -138.625Q1015.25 -161.75 1017.25 -202.5L1055.25 -163Q1050.25 -106 1027.75 -66.75Q1005.25 -27.5 965.75 -6.75Q926.25 14 868.75 14Z M1225.5 0 1395.5 -293.75 1391.75 -269.25 1231.5 -547.5H1405L1504 -361.5H1519.5L1613 -547.5H1775.75L1623.25 -267.25L1627 -288.25L1806.75 0H1633.25L1514.75 -199H1499.25L1388.25 0Z M1974 0 1810 -547.5H1974.5L2129.25 0ZM2023.5 0V-133H2185.25V0ZM2083 0 2215.5 -547.5H2369.5L2228 0Z M2682.5 19.5Q2612.5 19.5 2559.25 -4.5Q2506 -28.5 2470.25 -69.125Q2434.5 -109.75 2416 -160.25Q2397.5 -210.75 2397.5 -264V-283.5Q2397.5 -338.5 2416 -389.375Q2434.5 -440.25 2469.875 -480.125Q2505.25 -520 2557.375 -543.5Q2609.5 -567 2676.5 -567Q2765 -567 2825.625 -527.625Q2886.25 -488.25 2917.875 -424.375Q2949.5 -360.5 2949.5 -286V-231.75H2465V-323.25H2853.25L2800.5 -280.5Q2800.5 -330 2786.625 -364.75Q2772.75 -399.5 2745.375 -418Q2718 -436.5 2676.5 -436.5Q2635.25 -436.5 2606 -417.625Q2576.75 -398.75 2561.625 -362.375Q2546.5 -326 2546.5 -273Q2546.5 -224.25 2560.875 -187.75Q2575.25 -151.25 2605.25 -131.125Q2635.25 -111 2682.5 -111Q2726.75 -111 2755 -127.75Q2783.25 -144.5 2792.75 -170.25H2939.75Q2928 -114.5 2893.125 -71.5Q2858.25 -28.5 2804.875 -4.5Q2751.5 19.5 2682.5 19.5Z';

/** The mark alone. tone: auto follows the app's light / dark scheme; light = for light grounds; dark = for dark grounds. */
export function Mark({ size = 28, tone = 'auto', style, title }: { size?: number; tone?: 'auto' | 'light' | 'dark' | 'icon'; style?: CSSProperties; title?: string }) {
  return (
    <svg className={`pxv-mark pxv-${tone}`} viewBox="0 0 64 64" width={size} height={size} style={style}
      role={title ? 'img' : undefined} aria-label={title} aria-hidden={title ? undefined : true} focusable="false">
      <path d={MARK_A} className="m1" />
      <path d={MARK_B} className="m3" />
      <path d={MARK_X} className="m2" />
    </svg>
  );
}

/** The wordmark "Paxve" in the current text colour; height = cap height in px. */
export function Wordmark({ height = 16, style }: { height?: number; style?: CSSProperties }) {
  const s = height / 730; // px per font unit (cap height 730)
  return (
    <svg className="pxv-word" viewBox="72 -746 2888 776" height={+(776 * s).toFixed(2)} width={+(2888 * s).toFixed(2)}
      style={style} role="img" aria-label="Paxve" focusable="false">
      <path d={WORD} fill="currentColor" />
    </svg>
  );
}
