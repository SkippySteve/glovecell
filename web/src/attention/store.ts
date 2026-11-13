// One shared poller for the "Needs attention" data, so the inbox page and the
// nav badge (useAttentionCount) read the same requests. It polls only while a
// component uses it, and only while the tab is visible.
import {useEffect, useMemo, useSyncExternalStore} from 'react';
import {sandboxDetail, sandboxPreviews, type ExistingSandbox} from '../api';
import {useData} from '../data';
import {listRuns, type RunSummary} from '../factory/api';
import {useNow} from '../workspace/time';
import {loadRequests} from '../workspace/sources/internet';
import {loadLan} from '../workspace/sources/lan';
import {loadDevLinks} from '../workspace/sources/previews';
import {loadReports} from '../workspace/sources/reports';
import {attentionCount, collectAttention, type AttentionItem, type SandboxInput} from './collect';

const POLL_MS = 30_000;

interface Snapshot {
  // Per-sandbox details, keyed by sandbox id (summaries come from useData).
  sandboxes: Record<string, Omit<SandboxInput, 'summary'>>;
  runs: RunSummary[] | null;
  errors: Record<string, string>;
  loaded: boolean;
  updated: number;
}

let snapshot: Snapshot = {sandboxes: {}, runs: null, errors: {}, loaded: false, updated: 0};
const listeners = new Set<() => void>();
let targets: {ids: string[]; runs: boolean; sandboxControl: boolean} = {ids: [], runs: false, sandboxControl: false};
let users = 0, timer: number | undefined, inflight: Promise<void> | null = null, again = false;

const emit = () => listeners.forEach(l => l());
const settle = <T,>(p: Promise<T>) => p.then(v => ({ok: true as const, v}), (e: Error) => ({ok: false as const, e: e.message}));

async function loadSandbox(id: string): Promise<[Omit<SandboxInput, 'summary'>, string]> {
  const detail = await settle(sandboxDetail(id));
  if (!detail.ok) return [{}, detail.e];
  const d = detail.v;
  const [requests, lan, reports, devlinks, previews] = await Promise.all([
    d.learningEgress && (d.egressCounts?.pending || 0) > 0 ? settle(loadRequests(id, '', 1, 25)) : Promise.resolve({ok: true as const, v: null}),
    settle(loadLan(id)),
    settle(loadReports(id)),
    (d.devLinkPending || 0) > 0 ? settle(loadDevLinks(id)) : Promise.resolve({ok: true as const, v: []}),
    d.previewPending > 0 ? settle(sandboxPreviews(id, false).then(p => p.requests)) : Promise.resolve({ok: true as const, v: []}),
  ]);
  const errors = [requests, lan, reports, devlinks, previews].filter(r => !r.ok).map(r => (r as {e: string}).e);
  return [{detail: d, requests: requests.ok ? requests.v : undefined, lan: lan.ok ? lan.v : undefined, reports: reports.ok ? reports.v : undefined, devlinks: devlinks.ok ? devlinks.v : undefined, previews: previews.ok ? previews.v : undefined}, errors[0] || ''];
}

async function poll(): Promise<void> {
  const ids = targets.sandboxControl ? targets.ids : [];
  const [loaded, runs] = await Promise.all([
    Promise.all(ids.map(async id => [id, ...await loadSandbox(id)] as const)),
    targets.runs ? settle(listRuns()) : Promise.resolve({ok: true as const, v: null}),
  ]);
  const sandboxes: Snapshot['sandboxes'] = {}, errors: Record<string, string> = {};
  for (const [id, data, error] of loaded) {
    // Keep the last good data for a sandbox whose detail failed this time.
    sandboxes[id] = data.detail ? data : snapshot.sandboxes[id] || {};
    if (error) errors[id] = error;
  }
  if (!runs.ok) errors.runs = runs.e;
  snapshot = {sandboxes, runs: runs.ok ? runs.v : snapshot.runs, errors, loaded: true, updated: Date.now()};
  emit();
}

// Runs a poll now; a call during a poll schedules one more after it.
export function refreshAttention(): Promise<void> {
  if (inflight) { again = true; return inflight; }
  inflight = poll().finally(() => {
    inflight = null;
    if (again) { again = false; refreshAttention(); }
  });
  return inflight;
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  if (++users === 1) {
    refreshAttention();
    timer = window.setInterval(() => { if (document.visibilityState === 'visible') refreshAttention(); }, POLL_MS);
  }
  return () => {
    listeners.delete(listener);
    if (--users === 0) window.clearInterval(timer);
  };
}

export interface Attention {
  items: AttentionItem[];
  loaded: boolean;
  updated: number;
  errors: Record<string, string>;
  checked: {sandboxes: number; projects: number; runs: number};
  summaries: Record<string, ExistingSandbox>;
}

// Every attention item, re-derived when data arrives and as time passes (so a
// grant moves to "Act now" when it gets close to ending).
export function useAttention(): Attention {
  const {existing, projects, session, existingError} = useData();
  const snap = useSyncExternalStore(subscribe, () => snapshot);
  const ids = existing.map(s => s.id).join(',');
  useEffect(() => {
    const changed = ids !== targets.ids.join(',') || targets.runs !== Boolean(session.runRecords) || targets.sandboxControl !== Boolean(session.sandboxControl);
    targets = {ids: ids ? ids.split(',') : [], runs: Boolean(session.runRecords), sandboxControl: Boolean(session.sandboxControl)};
    if (changed) refreshAttention();
  }, [ids, session.runRecords, session.sandboxControl]);
  const now = useNow(15_000);
  const items = useMemo(() => collectAttention({
    sandboxes: existing.map(summary => ({summary, ...snap.sandboxes[summary.id]})),
    projects, runs: snap.runs,
  }, now), [existing, projects, snap, now]);
  const errors = existingError ? {...snap.errors, sandboxes: existingError} : snap.errors;
  // Loaded once every listed sandbox has been read at least once.
  const loaded = snap.loaded && existing.every(e => e.id in snap.sandboxes || !session.sandboxControl);
  return {items, loaded, updated: snap.updated, errors, checked: {sandboxes: existing.length, projects: projects.filter(p => p.status?.phase !== 'Removed').length, runs: snap.runs?.length || 0}, summaries: Object.fromEntries(existing.map(s => [s.id, s]))};
}

// For the nav: how many things wait on the operator, and how many of them are
// urgent ("Act now"). Open grants that are not ending soon are not counted.
export function useAttentionCount(): {count: number; urgent: number; loaded: boolean} {
  const {items, loaded} = useAttention();
  return {...attentionCount(items), loaded};
}

// Re-reads the sandbox list too, after an action changes it.
export const reloadAll = (reloadList: () => Promise<void>) => Promise.all([reloadList(), refreshAttention()]).then(() => undefined);
