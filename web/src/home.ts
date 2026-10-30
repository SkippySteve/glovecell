// The Home dashboard's rows, filters and sort orders, kept free of React so they are testable.
import {projectAccess, sandboxAccess, type Access} from './access';
import type {ExistingSandbox, Project} from './api';
import {projectHref, sandboxHref} from './router';
import {existingState, projectState, type Status} from './state';

// Access summaries live in ./access so Home and the sandbox Overview use the same words.
export {projectAccess, sandboxAccess, type Access} from './access';

export interface Row {
  key: string; kind: 'sandbox' | 'project'; name: string; kindLabel: string; sub: string; href: string;
  state: Status; running: boolean; access: Access; waiting: number; attention: string;
  // Open LAN grants (sandboxes) or static LAN hosts (projects).
  lan: number;
  lastActivity?: number;
}

export function sandboxRow(s: ExistingSandbox, kindLabel: string, lanOpen = 0): Row {
  const state = existingState(s);
  return {key: 's:' + s.id, kind: 'sandbox', name: s.id, kindLabel, sub: s.namespace, href: sandboxHref(s.id), state, running: state.label === 'Running',
    access: sandboxAccess(s), waiting: (s.pending || 0) + (s.approvals || 0), attention: state.variant === 'danger' ? state.label : '', lan: lanOpen};
}

export function projectRow(p: Project): Row {
  const state = projectState(p);
  return {key: 'p:' + p.metadata.name, kind: 'project', name: p.metadata.name, kindLabel: 'Project sandbox', sub: p.spec.repository, href: projectHref(p.metadata.name), state, running: state.label === 'Ready',
    access: projectAccess(p), waiting: 0, attention: state.attention || '', lan: (p.spec.lanHosts || []).length};
}

// Open or exposed first, then anything waiting on a decision, then the rest.
export const urgency = (r: Row) => r.access.rank === 0 || r.attention ? 0 : r.waiting || r.access.rank === 1 ? 1 : 2;

export type Show = 'all' | 'running' | 'stopped' | 'open' | 'waiting' | 'attention';
export const SHOWS: [Show, string][] = [['all', 'Any state'], ['running', 'Running'], ['stopped', 'Stopped or paused'], ['open', 'Open to the web'], ['waiting', 'Waiting on you'], ['attention', 'Needs attention']];
export type Sort = 'urgency' | 'name' | 'status' | 'access' | 'activity';
export const SORTS: [Sort, string][] = [['urgency', 'Most urgent first'], ['name', 'Name'], ['status', 'Running first'], ['access', 'Widest access first'], ['activity', 'Recent activity']];
export type Kind = 'all' | 'sandbox' | 'project';

export const toShow = (value: string | null): Show => SHOWS.find(([k]) => k === value)?.[0] || 'all';
export const toSort = (value: string | null): Sort => SORTS.find(([k]) => k === value)?.[0] || 'urgency';
export const toKind = (value: string | null): Kind => value === 'sandbox' || value === 'project' ? value : 'all';

const matches = (r: Row, show: Show) => show === 'all' ? true
  : show === 'running' ? r.running
  : show === 'stopped' ? !r.running
  : show === 'open' ? r.access.rank <= 1
  : show === 'waiting' ? r.waiting > 0
  : Boolean(r.attention) || r.state.variant === 'warning';

export function filterRows(rows: Row[], {q = '', show = 'all', kind = 'all'}: {q?: string; show?: Show; kind?: Kind}): Row[] {
  const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
  return rows.filter(r => (kind === 'all' || r.kind === kind) && matches(r, show)
    && terms.every(t => [r.name, r.sub, r.kindLabel, r.state.label, r.access.label].some(f => f.toLowerCase().includes(t))));
}

export function sortRows(rows: Row[], sort: Sort): Row[] {
  const byName = (a: Row, b: Row) => a.name.localeCompare(b.name);
  const compare: Record<Sort, (a: Row, b: Row) => number> = {
    urgency: (a, b) => urgency(a) - urgency(b) || byName(a, b),
    name: byName,
    status: (a, b) => Number(b.running) - Number(a.running) || a.state.label.localeCompare(b.state.label) || byName(a, b),
    access: (a, b) => a.access.rank - b.access.rank || byName(a, b),
    activity: (a, b) => (b.lastActivity || 0) - (a.lastActivity || 0) || byName(a, b),
  };
  return [...rows].sort(compare[sort]);
}

export interface Totals { total: number; running: number; open: number; waiting: number; attention: number }
export function totals(rows: Row[]): Totals {
  return {
    total: rows.length,
    running: rows.filter(r => r.running).length,
    open: rows.filter(r => r.access.rank <= 1).length,
    waiting: rows.reduce((n, r) => n + r.waiting, 0),
    attention: rows.filter(r => matches(r, 'attention')).length,
  };
}

// "4 min ago", "3 h ago", "2 days ago" or a date, for activity times in ms.
export function ago(at: number, now: number): string {
  const s = Math.max(0, Math.round((now - at) / 1000));
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  if (s < 7 * 86400) { const d = Math.floor(s / 86400); return `${d} day${d === 1 ? '' : 's'} ago`; }
  return new Date(at).toLocaleDateString([], {dateStyle: 'medium'});
}
