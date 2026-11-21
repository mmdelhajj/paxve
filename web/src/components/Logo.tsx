import { Wordmark } from './Brand';

/** The Paxve logo for the app screens (login, POS, collector, owner, portal...); follows light / dark. */
export default function Logo({ size = 'lg' }: { size?: 'lg' | 'xl' }) {
  return (
    <span className="pxv-lockup">
      <Wordmark height={size === 'xl' ? 56 : 26} />
    </span>
  );
}