import { Mark, Wordmark } from './Brand';

/** Mark + wordmark lockup for the app screens (login, POS, collector, owner, portal…); follows light / dark. */
export default function Logo({ size = 'lg' }: { size?: 'lg' | 'xl' }) {
  const xl = size === 'xl';
  return (
    <span className="pxv-lockup" style={{ gap: xl ? 12 : 6 }}>
      <Mark size={xl ? 48 : 24} />
      <Wordmark height={xl ? 40 : 20} />
    </span>
  );
}
