// What lists need to name every workspace's access level: for each
// sandbox-control sandbox its own web rules and approved count (one small
// /api/egress read each), and the controller's AccessProfiles. One poller,
// shared by Home and the command palette; it reads only while a component uses
// it, and only while the tab is visible.
import {useEffect, useSyncExternalStore} from 'react';
import {accessProfiles, egressControllable} from '../api';
import {useData} from '../data';
import {loadRequests} from '../workspace/sources/internet';
import type {Profiles, SandboxRules} from './levels';

const POLL_MS = 60_000;

interface Snapshot { rules: Record<string, SandboxRules>; profiles: Profiles }
let snapshot: Snapshot = {rules: {}, profiles: undefined};
let ids: string[] = [], users = 0, timer: number | undefined, inflight: Promise<void> | null = null, again = false;
const listeners = new Set<() => void>();

async function poll() {
  const [profiles, ...rules] = await Promise.all([
    accessProfiles().catch(() => snapshot.profiles ?? null),
    // One row per list is enough: the response carries the rules and the totals.
    ...ids.map(id => loadRequests(id, '', 1, 1).then(r => [id, {rules: r.rules ?? null, approved: r.approved.total}] as const, () => [id, snapshot.rules[id]] as const)),
  ]);
  const next: Record<string, SandboxRules> = {};
  for (const [id, r] of rules) if (r) next[id] = r;
  snapshot = {rules: next, profiles};
  listeners.forEach(l => l());
}

// A call during a read schedules one more after it, so a changed list is read.
function refresh(): Promise<void> {
  if (inflight) { again = true; return inflight; }
  inflight = poll().finally(() => { inflight = null; if (again) { again = false; refresh(); } });
  return inflight;
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  if (++users === 1) {
    refresh();
    timer = window.setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, POLL_MS);
  }
  return () => { listeners.delete(listener); if (--users === 0) window.clearInterval(timer); };
}

// Pass enabled=false to read nothing (for example while the palette is closed).
export function useStandingAccess(enabled = true): Snapshot {
  const {existing, session} = useData();
  const want = session.sandboxControl ? existing.filter(egressControllable).map(s => s.id).sort().join(',') : '';
  const snap = useSyncExternalStore(enabled ? subscribe : noop, () => snapshot);
  useEffect(() => {
    if (!enabled || want === ids.join(',')) return;
    ids = want ? want.split(',') : [];
    refresh();
  }, [enabled, want]);
  return snap;
}
const noop = () => () => undefined;
