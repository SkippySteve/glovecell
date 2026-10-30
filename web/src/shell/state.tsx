// State the app shell shares with pages: open LAN grants per sandbox (one slow
// poll for the sidebar and Home) and the current workspace's section badges,
// which the workspace view publishes for the sidebar.
import {createContext, useContext, useEffect, useMemo, useState, type ReactNode} from 'react';
import {useData} from '../data';
import type {Section} from '../router';
import {loadLan} from '../workspace/sources/lan';

export type SectionBadges = Partial<Record<Section, {n: number; live?: boolean; label?: string}>>;
interface SectionNav { key: string; badges: SectionBadges }
interface Shell {
  lanOpen: Record<string, number>;
  sectionNav: SectionNav | null;
  setSectionNav: (nav: SectionNav | null) => void;
}
const Context = createContext<Shell>({lanOpen: {}, sectionNav: null, setSectionNav: () => undefined});
export const useShell = () => useContext(Context);

const LAN_MS = 30000;

// Open LAN grants per sandbox, polled slowly while the page is visible.
function useLanCounts(ids: string[]) {
  const [counts, setCounts] = useState<Record<string, number>>({});
  const key = ids.join(',');
  useEffect(() => {
    if (!ids.length) return;
    let live = true;
    const load = () => Promise.all(ids.map(id => loadLan(id).then(v => [id, v?.open || 0] as const, () => [id, 0] as const))).then(pairs => { if (live) setCounts(Object.fromEntries(pairs)); });
    load();
    const t = window.setInterval(() => { if (document.visibilityState === 'visible') load(); }, LAN_MS);
    return () => { live = false; window.clearInterval(t); };
  }, [key]); // ids is derived from key
  return counts;
}

export function ShellProvider({children}: {children: ReactNode}) {
  const {existing} = useData();
  const lanOpen = useLanCounts(existing.map(s => s.id));
  const [sectionNav, setSectionNav] = useState<SectionNav | null>(null);
  const value = useMemo(() => ({lanOpen, sectionNav, setSectionNav}), [lanOpen, sectionNav]);
  return <Context.Provider value={value}>{children}</Context.Provider>;
}

// Called by the workspace view: the sidebar shows these badges next to the
// sections of workspace `key`. Fresh per-sandbox counts (the open LAN grants
// of the sandbox on screen) also override the slow poll.
export function useSectionBadges(key: string, badges: SectionBadges) {
  const {setSectionNav} = useShell();
  const signature = JSON.stringify(badges);
  useEffect(() => { setSectionNav({key, badges: JSON.parse(signature) as SectionBadges}); }, [key, signature, setSectionNav]);
  useEffect(() => () => setSectionNav(null), [setSectionNav]);
}
