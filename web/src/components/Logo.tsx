import { Mark, Wordmark } from './Brand';

/** Mark + wordmark lockup for the app screens (login, POS, collector, owner, portal…); follows light / dark. */
export default function Logo({ size = 'lg' }: { size?: 'lg' | 'xl' }) {
  const xl = size === 'xl';
  return (
    <span className="pxv-lockup" style={{ gap: xl ? 8 : 6, color: 'var(--mantine-color-text)' }}>
      <Mark size={32} />
      <Wordmark height={xl ? 21 : 17} />
    </span>
  );
}
