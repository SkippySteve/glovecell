import {createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode} from 'react';
import {api, existingSandboxes, setEgress, type EgressAction, type ExistingSandbox, type Project, type Repository, type Session} from './api';

export interface Toast { id: number; text: string; error: boolean }
interface Data {
  loaded: boolean;
  projects: Project[];
  existing: ExistingSandbox[];
  existingError: string;
  session: Session;
  repositories: Repository[] | null;
  reload: () => Promise<void>;
  egress: (ids: string[], action: EgressAction, minutes: number) => Promise<void>;
  busy: Set<string>;
  toast: (text: string, error?: boolean) => void;
  toasts: Toast[];
}
const Context = createContext<Data | null>(null);
export const useData = () => useContext(Context)!;

// Poll often enough that a lock made elsewhere, or an expired window, shows up quickly.
const POLL_MS = 5000;

export function DataProvider({children}: {children: ReactNode}) {
  const [loaded, setLoaded] = useState(false);
  const [projects, setProjects] = useState<Project[]>([]);
  const [existing, setExisting] = useState<ExistingSandbox[]>([]);
  const [existingError, setExistingError] = useState('');
  const [session, setSession] = useState<Session>({});
  const [repositories, setRepositories] = useState<Repository[] | null>(null);
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const [toasts, setToasts] = useState<Toast[]>([]);
  const sessionRef = useRef<Session>({});
  const inflight = useRef(false);
  // Egress changes in flight win over polls that started before them.
  const generation = useRef(0);

  const toast = useCallback((text: string, error = false) => {
    const id = Date.now() + Math.random();
    setToasts(list => [...list.slice(-3), {id, text, error}]);
    setTimeout(() => setToasts(list => list.filter(t => t.id !== id)), error ? 12000 : 6000);
  }, []);

  const reload = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    const started = generation.current;
    try {
      const [items, sandboxes] = await Promise.all([
        api<Project[]>('workspaces').then(list => list || []),
        sessionRef.current.sandboxControl ? existingSandboxes().then(list => { setExistingError(''); return list; }, (error: Error) => { setExistingError(error.message); return null; }) : Promise.resolve([] as ExistingSandbox[]),
      ]);
      setProjects(items);
      if (sandboxes && started === generation.current) setExisting(sandboxes);
      setLoaded(true);
    } catch (error) {
      toast((error as Error).message, true);
    } finally {
      inflight.current = false;
    }
  }, [toast]);

  const egress = useCallback(async (ids: string[], action: EgressAction, minutes: number) => {
    generation.current++;
    setBusy(current => new Set([...current, ...ids]));
    const results = await Promise.allSettled(ids.map(id => setEgress(id, action, minutes)));
    generation.current++;
    setBusy(current => { const next = new Set(current); ids.forEach(id => next.delete(id)); return next; });
    const updated = new Map<string, ExistingSandbox>();
    const failed: string[] = [];
    results.forEach((result, i) => {
      if (result.status === 'fulfilled') updated.set(ids[i], result.value);
      else failed.push((result.reason as Error).message);
    });
    setExisting(list => list.map(item => updated.get(item.id) || item));
    const verb = action === 'open' ? 'opened' : 'locked';
    if (updated.size) toast(updated.size === 1 ? `${[...updated.keys()][0]}: web access ${verb}` : `Web access ${verb} on ${updated.size} sandboxes`);
    for (const message of failed) toast(message, true);
  }, [toast]);

  useEffect(() => {
    api<Repository[]>('repositories').then(items => setRepositories(items || []), (error: Error) => { setRepositories([]); toast(error.message, true); });
    let timer: number | undefined;
    api<Session>('session').then(value => { sessionRef.current = value; setSession(value); }, (error: Error) => toast(error.message, true))
      .finally(() => { reload(); timer = window.setInterval(() => { if (document.visibilityState === 'visible') reload(); }, POLL_MS); });
    return () => window.clearInterval(timer);
  }, [reload, toast]);

  return <Context.Provider value={{loaded, projects, existing, existingError, session, repositories, reload, egress, busy, toast, toasts}}>{children}</Context.Provider>;
}
