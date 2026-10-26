// State of one sandbox-control workspace for the workspace view: its detail
// plus the per-section data the header and badges need. Each part loads through
// its section's source module and fails on its own.
import {useCallback, useEffect, useRef, useState} from 'react';
import {sandboxAction, sandboxDetail, type DevLink, type SandboxDetail} from '../api';
import {useData} from '../data';
import {loadLan, type LanView} from './sources/lan';
import {loadDevLinks} from './sources/previews';
import {loadReports, type AgentReport} from './sources/reports';

const DETAIL_MS = 10000, SLOW_MS = 30000, APPLYING_MS = 2000;

function usePoll(load: () => Promise<void>, ms: number) {
  useEffect(() => {
    load();
    const t = window.setInterval(() => { if (document.visibilityState === 'visible') load(); }, ms);
    return () => window.clearInterval(t);
  }, [load, ms]);
}

export interface SandboxState {
  id: string;
  detail: SandboxDetail | null; error: string;
  // undefined while loading; null when the release does not serve the view.
  lan: LanView | null | undefined; lanError: string;
  devlinks: DevLink[] | null; reports: AgentReport[] | null | undefined;
  reload: () => Promise<void>; reloadLan: () => Promise<void>; reloadDevLinks: () => Promise<void>; reloadReports: () => Promise<void>;
  // Runs a sandbox-control POST, reports it, then refreshes this view and the list.
  act: (path: string, fields: Record<string, string | string[]>, done: string) => Promise<boolean>;
}

export function useSandbox(id: string): SandboxState {
  const {reload: reloadList, toast, existing} = useData();
  const [detail, setDetail] = useState<SandboxDetail | null>(null);
  const [error, setError] = useState('');
  const [lan, setLan] = useState<LanView | null | undefined>(undefined);
  const [lanError, setLanError] = useState('');
  const [devlinks, setDevLinks] = useState<DevLink[] | null>(null);
  const [reports, setReports] = useState<AgentReport[] | null | undefined>(undefined);
  const current = useRef(id);
  current.current = id;
  // Responses for a workspace the operator already left are dropped.
  const guard = <T,>(set: (v: T) => void) => (v: T) => { if (current.current === id) set(v); };

  const reload = useCallback(async () => {
    try { guard(setDetail)(await sandboxDetail(id)); guard(setError)(''); } catch (e) { guard(setError)((e as Error).message); }
  }, [id]);
  const reloadLan = useCallback(async () => {
    try { guard(setLan)(await loadLan(id)); guard(setLanError)(''); } catch (e) { guard(setLanError)((e as Error).message); }
  }, [id]);
  const reloadDevLinks = useCallback(async () => {
    try { guard(setDevLinks)(await loadDevLinks(id)); } catch { /* the section shows its own error */ }
  }, [id]);
  const reloadReports = useCallback(async () => {
    try { guard(setReports)(await loadReports(id)); } catch { /* the Activity view shows the error */ }
  }, [id]);

  useEffect(() => { setDetail(null); setError(''); setLan(undefined); setLanError(''); setDevLinks(null); setReports(undefined); }, [id]);
  usePoll(reload, DETAIL_MS);
  usePoll(reloadLan, lan?.applying ? APPLYING_MS : DETAIL_MS);
  usePoll(reloadDevLinks, SLOW_MS);
  usePoll(reloadReports, SLOW_MS);

  // The list poll notices egress changes made elsewhere sooner than this view's own poll.
  const summary = existing.find(s => s.id === id);
  useEffect(() => { if (summary) reload(); }, [summary?.egress, summary?.egressExpires, summary?.mode, reload]);

  const act = useCallback(async (path: string, fields: Record<string, string | string[]>, done: string) => {
    try {
      await sandboxAction(path, id, fields);
      toast(id + ': ' + done);
      await Promise.all([reload(), reloadLan(), reloadList()]);
      return true;
    } catch (e) { toast((e as Error).message, true); return false; }
  }, [id, reload, reloadLan, reloadList, toast]);
  return {id, detail, error, lan, lanError, devlinks, reports, reload, reloadLan, reloadDevLinks, reloadReports, act};
}
