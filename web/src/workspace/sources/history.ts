// History section: data source.
//
// There is no single history API today, so this merges what the console can
// read: LAN grant history (GET /api/lan), dev link decisions (GET /api/devlinks
// Events), policy revisions, which include start and stop (GET /api/workspace),
// open agent reports (GET /api/reports), file transfers (GET /api/downloads) and
// web rule changes (GET /api/egress ruleHistory). Web access open, lock and
// single-request approval events have no read API yet. Blocked requests,
// commands and connections stay in the Activity view (Loki and Tempo through
// GET /api/activity).
import type {AgentReport, DevLink, Revision, RuleEvent} from '../../api';
import {plainText, RESOURCE_LIMIT} from '../../orca';
import type {DownloadEvent} from './downloads';
import type {LanView} from './lan';
import {reportTime} from './reports';

export type HistoryType = 'web' | 'lan' | 'devlink' | 'policy' | 'report' | 'download';
// What an event did to access, for the outcome filter.
export type Outcome = 'granted' | 'ended' | 'blocked' | 'failed' | 'done' | 'info';
export interface HistoryEvent { at: number; type: HistoryType; title: string; detail: string; by: string; host: string; outcome: Outcome }
export const HISTORY_FILTERS: [HistoryType | 'all', string][] = [['all', 'All'], ['web', 'Web rules'], ['lan', 'LAN'], ['devlink', 'Dev links'], ['report', 'Reports'], ['download', 'Files'], ['policy', 'Policy & lifecycle']];
export const OUTCOMES: [Outcome | 'all', string][] = [['all', 'Any outcome'], ['granted', 'Granted or opened'], ['ended', 'Ended or removed'], ['blocked', 'Blocked'], ['failed', 'Failed'], ['done', 'Completed'], ['info', 'Other']];
export const OUTCOME_LABEL: Record<Outcome, string> = {granted: 'Granted', ended: 'Ended', blocked: 'Blocked', failed: 'Failed', done: 'Completed', info: 'Info'};

// The first hostname or IPv4 address in a text, lowercased; '' when none.
export function hostIn(text: string): string {
  const url = /[a-z][a-z0-9+.-]*:\/\/([^/\s?#:]+)/i.exec(text);
  if (url) return url[1].toLowerCase();
  const m = /\b((?:\d{1,3}\.){3}\d{1,3}|(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63})\b/i.exec(text);
  return m ? m[1].toLowerCase() : '';
}

const LAN_GRANTED = new Set(['opened', 'extended', 'applied', 'ports-learned']);
const LAN_ENDED = new Set(['restricted', 'expired', 'revoked', 'controller-restart', 'orphan-removed', 'ports-forgotten']);
function outcomeOf(text: string): Outcome {
  if (/fail|error|reject/i.test(text)) return 'failed';
  if (/approv|grant|open|add|creat|allow|enabl|publish/i.test(text)) return 'granted';
  if (/den|revok|remov|delet|expir|restrict|lock|cancel|disabl|stop/i.test(text)) return 'ended';
  return 'info';
}

const LAN_EVENTS: Record<string, string> = {opened: 'LAN access opened', extended: 'LAN access extended', restricted: 'LAN access restricted', expired: 'LAN access expired', applied: 'LAN rule applied', revoked: 'LAN rule removed', 'ports-learned': 'LAN ports learned', 'ports-forgotten': 'Learned LAN ports forgotten', 'worker-restarted': 'Workspace pod restarted', 'controller-restart': 'Grant ended by a sandbox-control restart', 'orphan-removed': 'Orphaned LAN rule removed'};
export const lanEventTitle = (event: string) => LAN_EVENTS[event] || 'LAN ' + event.replace(/-/g, ' ');

const DOWNLOAD_EVENTS: Record<string, string> = {'download-chain-checked': 'Download URL checked', 'download-requested': 'Download started', 'download-done': 'Download finished', 'download-failed': 'Download failed', 'download-changed': 'Download stopped: redirect chain changed', 'download-cancelled': 'Download cancelled',
  'upload-requested': 'Upload started', 'upload-done': 'Upload finished', 'upload-failed': 'Upload failed', 'upload-cancelled': 'Upload cancelled'};

export function mergeHistory(src: {lan?: LanView | null; devlinks?: DevLink[] | null; revisions?: Revision[]; reports?: AgentReport[] | null; downloads?: DownloadEvent[] | null; rules?: RuleEvent[] | null}): HistoryEvent[] {
  const out: HistoryEvent[] = [];
  for (const e of src.downloads || []) {
    out.push({at: e.at, type: 'download', title: DOWNLOAD_EVENTS[e.action] || e.action.replace(/-/g, ' '), detail: e.detail, by: e.by, host: hostIn(e.detail),
      outcome: /failed|changed/.test(e.action) ? 'failed' : /cancel/.test(e.action) ? 'ended' : /done/.test(e.action) ? 'done' : 'info'});
  }
  for (const e of src.lan?.history || []) {
    out.push({at: e.at, type: 'lan', title: `${lanEventTitle(e.event)}: ${e.host}`, detail: [e.ports ? 'TCP ' + e.ports : '', e.deadline ? 'until ' + new Date(e.deadline).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'}) : '', e.detail].filter(Boolean).join(', '), by: e.by,
      host: e.host, outcome: LAN_GRANTED.has(e.event) ? 'granted' : LAN_ENDED.has(e.event) ? 'ended' : 'info'});
  }
  for (const l of src.devlinks || []) for (const e of l.Events || []) {
    out.push({at: e.At * 1000, type: 'devlink', title: `Dev link ${l.Name}: ${e.Action}`, detail: e.Detail, by: e.Actor, host: (l.Host || '').toLowerCase(), outcome: /^published/.test(e.Action) ? 'info' : outcomeOf(e.Action)});
  }
  for (const r of src.revisions || []) {
    out.push({at: r.at * 1000, type: 'policy', title: `Policy revision ${r.id}: ${r.status}`, detail: r.error || '', by: r.actor || '', host: '', outcome: r.error || /fail|reject|error/i.test(r.status) ? 'failed' : 'info'});
  }
  for (const r of src.reports || []) {
    const at = Date.parse(r.last);
    const resource = plainText(r.resource, RESOURCE_LIMIT);
    out.push({at: Number.isFinite(at) ? at : 0, type: 'report', title: `Agent report (${plainText(r.kind, 40)})`, detail: `${resource}, reported ${r.count} time${r.count === 1 ? '' : 's'}`, by: 'agent', host: hostIn(resource), outcome: 'blocked'});
  }
  for (const e of src.rules || []) {
    const at = Date.parse(e.at);
    const description = plainText(e.description || e.rule, 300);
    out.push({at: Number.isFinite(at) ? at : 0, type: 'web', title: `Web rule ${plainText(e.action, 40)}`, detail: [description, e.expires ? 'expires ' + reportTime(e.expires) : '', plainText(e.detail, 200)].filter(Boolean).join(', '), by: plainText(e.by, 80),
      host: hostIn(description), outcome: outcomeOf(e.action)});
  }
  return out.sort((a, b) => b.at - a.at);
}

// History filters, kept in the URL query: ?type=&host=&outcome=&range=&page=
export const RANGES: [string, string, number][] = [['all', 'Any time', 0], ['1h', 'Last hour', 3600_000], ['24h', 'Last 24 hours', 86400_000], ['7d', 'Last 7 days', 7 * 86400_000], ['30d', 'Last 30 days', 30 * 86400_000]];
export interface HistoryFilter { type: HistoryType | 'all'; host: string; outcome: Outcome | 'all'; range: string }
export const DEFAULT_FILTER: HistoryFilter = {type: 'all', host: '', outcome: 'all', range: 'all'};
export function readFilter(q: URLSearchParams): HistoryFilter {
  const type = q.get('type'), outcome = q.get('outcome'), range = q.get('range');
  return {
    type: HISTORY_FILTERS.some(([k]) => k === type) ? type as HistoryType : 'all',
    host: (q.get('host') || '').slice(0, 253),
    outcome: OUTCOMES.some(([k]) => k === outcome) ? outcome as Outcome : 'all',
    range: RANGES.some(([k]) => k === range) ? range! : 'all',
  };
}
export const filterQuery = (f: HistoryFilter) => ({type: f.type === 'all' ? undefined : f.type, host: f.host || undefined, outcome: f.outcome === 'all' ? undefined : f.outcome, range: f.range === 'all' ? undefined : f.range});
export function filterHistory(events: HistoryEvent[], f: HistoryFilter, now: number): HistoryEvent[] {
  const span = RANGES.find(([k]) => k === f.range)?.[2] || 0;
  const host = f.host.trim().toLowerCase();
  return events.filter(e => (f.type === 'all' || e.type === f.type)
    && (f.outcome === 'all' || e.outcome === f.outcome)
    && (!span || (e.at && now - e.at <= span))
    && (!host || e.host.includes(host) || e.title.toLowerCase().includes(host) || e.detail.toLowerCase().includes(host)));
}
// Distinct hosts in the events, for the host field's suggestions.
export const historyHosts = (events: HistoryEvent[]) => [...new Set(events.map(e => e.host).filter(Boolean))].sort();


// Console projects' history. The section reads it only through AuditSource, so
// the store behind GET /api/v1/workspaces/<name>/audit (today the project's
// status.audit; later a durable database) can change without a UI change, and
// more than one source can be read side by side during a move.
export interface AuditEntry { time: string; action: string; message?: string }
export interface AuditSource { name: string; load: (project: string) => Promise<unknown | null> }

// Maps one audit record {time, action, message}; anything malformed is skipped.
export function toAuditEntry(raw: unknown): AuditEntry | null {
  if (!raw || typeof raw !== 'object') return null;
  const r = raw as Record<string, unknown>;
  const str = (v: unknown) => typeof v === 'string' ? v : '';
  const time = str(r.time), action = str(r.action);
  if (!time || !action || !Number.isFinite(Date.parse(time))) return null;
  return {time, action, message: str(r.message) || undefined};
}
// A list of records, or a page {items: [...]}.
export function toAuditEntries(raw: unknown): AuditEntry[] {
  const list = Array.isArray(raw) ? raw : raw && typeof raw === 'object' && Array.isArray((raw as {items?: unknown}).items) ? (raw as {items: unknown[]}).items : [];
  return list.map(toAuditEntry).filter((e): e is AuditEntry => e !== null);
}
// Entries from several sources, de-duplicated (a move may briefly serve both), newest first.
export function mergeAudit(lists: AuditEntry[][]): AuditEntry[] {
  const seen = new Set<string>();
  const out: AuditEntry[] = [];
  for (const e of lists.flat()) {
    const key = `${Date.parse(e.time)}|${e.action}|${e.message || ''}`;
    if (!seen.has(key)) { seen.add(key); out.push(e); }
  }
  return out.sort((a, b) => Date.parse(b.time) - Date.parse(a.time));
}
const auditType = (action: string): HistoryType => /lan/i.test(action) ? 'lan' : /download|upload/i.test(action) ? 'download' : /rule|https|egress|web/i.test(action) ? 'web' : 'policy';
export const auditToHistory = (entries: AuditEntry[]): HistoryEvent[] => entries.map(e => ({
  at: Date.parse(e.time), type: auditType(e.action), title: e.action, detail: e.message || '', by: '', host: hostIn(e.message || ''), outcome: outcomeOf(e.action),
}));
