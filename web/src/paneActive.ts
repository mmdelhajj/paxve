import { createContext, useContext, useEffect, useRef } from 'react';

/** True while the screen (window) holding the component is the one shown. Windows stay mounted when hidden,
 *  so screens use this to refresh their data when the user comes back to them. */
export const PaneActive = createContext(true);

/** Number that grows every time the window becomes active again (not on first show): add it to a data hook's deps. */
export function useActivations(): number {
  const active = useContext(PaneActive);
  const n = useRef(0);
  const was = useRef(active);
  useEffect(() => { was.current = active; });
  if (active && !was.current) n.current += 1;
  return n.current;
}
