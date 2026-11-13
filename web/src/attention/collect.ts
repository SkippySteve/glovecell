// "Needs attention": every item across sandboxes, projects and runs that waits
// on the operator, from data the console already reads. Pure functions, so the
// aggregation and the urgency order are tested without React or a backend.
//
// Sources (all existing read APIs):
//   sandbox-control GET /workspaces (summaries), /api/workspace, /api/egress,
//   /api/lan, /api/reports, /api/devlinks, /api/previews; console /api/v1/workspaces
//   (projects) and /api/v1/runs (run summaries).
import {egressExpires, egressState, type AgentReport, type DevLink, type ExistingSandbox, type Observation, type Preview, type Project, type SandboxDetail} from '../api';
import type {RunSummary} from '../factory/api';
import {runView} from '../factory/runs';
import {plainText, reportKind, RESOURCE_LIMIT} from '../orca';
import {projectState, existingState} from '../state';
import type {Requests} from '../workspace/sources/internet';
import type {LanView} from '../workspace/sources/lan';
import {reportLanHost, reportRequests} from '../workspace/sources/reports';

export type AttentionKind =
  | 'sandbox-error' | 'project-error' | 'revision-error' | 'report'
  | 'request' | 'package' | 'more-requests' | 'devlink' | 'preview' | 'gate'
  | 'run-failed' | 'run-stuck'
  | 'web-window' | 'lan-grant';

// Groups, most urgent first.
export type Tier = 'now' | 'decide' | 'runs' | 'open';
export const TIERS: {tier: Tier; title: string; hint: string}[] = [
  {tier: 'now', title: 'Act now', hint: 'Errors, agents that say they are blocked, and grants ending within 15 minutes.'},
  {tier: 'decide', title: 'Waiting on your decision', hint: 'Requests, packages, previews, dev links and run decisions. Nothing is allowed until you approve it.'},
  {tier: 'runs', title: 'Failed or stuck runs', hint: 'Runs collected in the last 3 days that failed, or have recorded no progress for 2 hours.'},
  {tier: 'open', title: 'Open access', hint: 'Grants that end on their own. Lock or restrict early if they are no longer needed.'},
];

// A grant ending this soon moves to "Act now".
export const SOON_MS = 15 * 60_000;
// Failed and stuck runs older than this are left out.
export const RUN_RECENT_MS = 3 * 86400_000;
// Requests listed one by one per sandbox; the rest are one "more" item.
export const REQUESTS_SHOWN = 4;

export type Owner = {kind: 'sandbox' | 'project'; id: string} | {kind: 'run'; id: string; host: string};

export interface AttentionItem {
  key: string;
  kind: AttentionKind;
  tier: Tier;
  owner: Owner;
  title: string;
  detail: string;
  // Epoch ms: when it started waiting, and when it ends on its own.
  since?: number;
  deadline?: number;
  // How many things this item stands for (an aggregate of requests counts each).
  count: number;
  // The data its one-click actions need.
  observation?: Observation;
  report?: AgentReport;
  // A report's links: the Internet filter that finds it, or the allowlisted LAN host it names.
  reportWeb?: {filter: string; waiting: number; note: string};
  reportLan?: {address: string; open: boolean};
  devlink?: DevLink;
  preview?: Preview;
  web?: {phase: 'Open' | 'Learning'; minutes: number};
  lan?: {address: string; podUid: string; windows: number[]; listed: boolean; open: number; state: string};
  run?: RunSummary;
}

export interface SandboxInput {
  summary: ExistingSandbox;
  // Undefined while loading, null when the release does not serve that view.
  detail?: SandboxDetail | null;
  requests?: Requests | null;
  lan?: LanView | null;
  reports?: AgentReport[] | null;
  devlinks?: DevLink[] | null;
  previews?: Preview[] | null;
}
export interface AttentionInput { sandboxes: SandboxInput[]; projects: Project[]; runs?: RunSummary[] | null }

// Order inside a group after deadlines: errors, then what blocks an agent, then the rest.
const RANK: Record<AttentionKind, number> = {
  'sandbox-error': 0, 'project-error': 0, 'revision-error': 1, report: 2, 'web-window': 3, 'lan-grant': 3,
  gate: 4, request: 5, package: 5, 'more-requests': 6, devlink: 7, preview: 7, 'run-failed': 8, 'run-stuck': 9,
};

// Sort: tier; then the soonest deadline; then kind; then the longest waiting; then key, so the order is stable.
export function compareAttention(a: AttentionItem, b: AttentionItem): number {
  const tier = TIERS.findIndex(t => t.tier === a.tier) - TIERS.findIndex(t => t.tier === b.tier);
  if (tier) return tier;
  const da = a.deadline ?? Infinity, db = b.deadline ?? Infinity;
  if (da !== db) return da < db ? -1 : 1;
  if (RANK[a.kind] !== RANK[b.kind]) return RANK[a.kind] - RANK[b.kind];
  const sa = a.since ?? Infinity, sb = b.since ?? Infinity;
  if (sa !== sb) return sa < sb ? -1 : 1;
  return a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
}

const grantTier = (deadline: number, now: number): Tier => deadline && deadline - now <= SOON_MS ? 'now' : 'open';
const iso = (v: string) => { const t = Date.parse(v); return Number.isFinite(t) ? t : undefined; };
const sec = (unix?: number) => unix ? unix * 1000 : undefined;

function sandboxItems(s: SandboxInput, now: number): AttentionItem[] {
  const id = s.summary.id, owner: Owner = {kind: 'sandbox', id};
  const out: AttentionItem[] = [];
  const add = (item: Omit<AttentionItem, 'owner' | 'count'> & {count?: number}) => out.push({owner, count: 1, ...item});

  const state = existingState(s.summary);
  if (state.variant === 'danger') add({key: `${id}:state`, kind: 'sandbox-error', tier: 'now', title: `${id}: ${state.label}`, detail: 'The state sandbox-control reports for this sandbox.'});
  const d = s.detail;
  if (d?.revision.error) add({key: `${id}:revision:${d.revision.id}`, kind: 'revision-error', tier: 'now', title: `Policy revision ${d.revision.id} ${d.revision.status.toLowerCase()}`, detail: plainText(d.revision.error, 300), since: sec(d.revision.at)});

  for (const r of s.reports || []) {
    add({key: `${id}:report:${r.id}`, kind: 'report', tier: 'now', title: `Agent blocked: ${reportKind(r.kind)}`, detail: plainText(r.resource, RESOURCE_LIMIT), since: iso(r.first) ?? iso(r.last), count: 1, report: r, reportWeb: reportRequests(r), reportLan: reportLanHost(r, s.lan || null)});
  }

  // Web access window (summary first: it is polled every 5 s and always present).
  const phase = egressState(s.summary), expires = egressExpires(s.summary) * 1000;
  if ((phase === 'Open' || phase === 'Learning') && expires > now) {
    add({key: `${id}:web`, kind: 'web-window', tier: grantTier(expires, now), title: phase === 'Open' ? 'Web access open' : 'Web access learning', detail: phase === 'Open' ? 'Any public web destination is reachable.' : 'Every request made now becomes an approval.',
      deadline: expires, since: d?.learning ? d.learning.started * 1000 : undefined, web: {phase, minutes: d?.learning?.minutes || 60}});
  }

  for (const h of s.lan?.hosts || []) {
    if (!h.grant) continue;
    const g = h.grant;
    add({key: `${id}:lan:${h.address}`, kind: 'lan-grant', tier: g.state === 'Open' ? grantTier(g.deadline, now) : 'open', title: `LAN ${h.address} ${g.state === 'Open' ? 'open' : g.state.toLowerCase()}`,
      detail: 'TCP ' + (g.all ? 'all ports' : g.ports.join(', ')), deadline: g.state === 'Open' && g.deadline ? g.deadline : undefined, since: g.openedAt || undefined,
      lan: {address: h.address, podUid: s.lan!.podUid, windows: s.lan!.windows, listed: h.listed, open: s.lan!.open, state: g.state}});
  }

  // Waiting requests: the first few one by one, the rest as one item.
  const pending = s.requests?.pending;
  const total = pending ? pending.total : s.summary.pending || 0;
  const shown = (pending?.items || []).slice(0, REQUESTS_SHOWN);
  for (const o of shown) {
    const pkg = o.package?.name ? o.package : undefined;
    add({key: `${id}:req:${o.observation}`, kind: pkg ? 'package' : 'request', tier: 'decide',
      title: pkg ? `Package ${pkg.ecosystem} ${pkg.name}${pkg.version ? ' ' + pkg.version : ''}` : `${o.method} ${o.host}`,
      detail: `${o.method} ${o.scheme}://${o.host}${o.port === 443 || o.port === 80 ? '' : ':' + o.port}${o.path}`, since: sec(o.first) ?? sec(o.last), observation: o});
  }
  if (total > shown.length) add({key: `${id}:more-requests`, kind: 'more-requests', tier: 'decide', title: `${total - shown.length} more web request${total - shown.length === 1 ? '' : 's'} waiting`, detail: 'Review them on the Internet page.', count: total - shown.length});

  // Dev links and previews: one item each when listed, else the detail's counts.
  const links = (s.devlinks || []).filter(l => l.State === 'Pending');
  for (const l of links) add({key: `${id}:devlink:${l.Name}`, kind: 'devlink', tier: 'decide', title: `Dev link ${l.Name}`, detail: l.Scan.Status === 'Complete' ? `Scan complete: ${l.Counts.critical} critical, ${l.Counts.high} high, ${l.Counts.secrets} possible secrets` : `Scan ${l.Scan.Status.toLowerCase()}`, since: sec(l.Requested), devlink: l});
  const previews = (s.previews || []).filter(p => p.state === 'Pending');
  for (const p of previews) add({key: `${id}:preview:${p.id}`, kind: 'preview', tier: 'decide', title: `Preview ${p.proposedName || 'server'} on port ${p.listener.port}`, detail: `${p.listener.project} / ${p.listener.worktree}`, since: sec(p.first), preview: p});
  const devPending = d ? d.devLinkPending || 0 : 0, previewPending = d ? d.previewPending : 0;
  const unlisted = d ? Math.max(0, devPending - links.length) + Math.max(0, previewPending - previews.length) : Math.max(0, (s.summary.approvals || 0) - links.length - previews.length);
  if (unlisted) add({key: `${id}:previews`, kind: 'preview', tier: 'decide', title: `${unlisted} preview${unlisted === 1 ? '' : 's'} or dev link${unlisted === 1 ? '' : 's'} waiting`, detail: 'Review them on the Previews page.', count: unlisted});
  return out;
}

function projectItems(p: Project): AttentionItem[] {
  if (p.status?.phase === 'Removed') return [];
  const st = projectState(p);
  if (!st.attention) return [];
  return [{key: `project:${p.metadata.name}`, kind: 'project-error', tier: 'now', owner: {kind: 'project', id: p.metadata.name}, title: `${p.metadata.name}: ${st.attention.toLowerCase()}`, detail: plainText(st.message || '', 400), count: 1}];
}

function runItems(runs: RunSummary[], now: number): AttentionItem[] {
  const out: AttentionItem[] = [];
  for (const r of runs) {
    const owner: Owner = {kind: 'run', id: r.id, host: r.host};
    const collected = Date.parse(r.collectedAt) || 0;
    if (r.pendingGates) out.push({key: `run:${r.id}:gates`, kind: 'gate', tier: 'decide', owner, title: `${r.pendingGates} decision${r.pendingGates === 1 ? '' : 's'} in run ${r.id}`, detail: plainText(r.objective, 200), since: Date.parse(r.lastActivity || '') || collected || undefined, count: r.pendingGates, run: r});
    if (now - collected > RUN_RECENT_MS) continue;
    const v = runView(r, now);
    if (v === 'failed') out.push({key: `run:${r.id}`, kind: 'run-failed', tier: 'runs', owner, title: `Run ${r.id} failed`, detail: r.failure ? plainText(`${r.failure.title}: ${r.failure.stage ? r.failure.stage + ': ' : ''}${r.failure.error || 'no error recorded'}`, 300) : plainText(r.objective, 200), since: Date.parse(r.endedAt || '') || collected || undefined, count: 1, run: r});
    if (v === 'stuck') out.push({key: `run:${r.id}`, kind: 'run-stuck', tier: 'runs', owner, title: `Run ${r.id} has stopped progressing`, detail: plainText(r.objective, 200), since: Date.parse(r.lastActivity || r.startedAt || '') || undefined, count: 1, run: r});
  }
  return out;
}

export function collectAttention(input: AttentionInput, now: number): AttentionItem[] {
  return [
    ...input.sandboxes.flatMap(s => sandboxItems(s, now)),
    ...input.projects.flatMap(projectItems),
    ...runItems(input.runs || [], now),
  ].sort(compareAttention);
}

export function groupAttention(items: AttentionItem[]): {tier: Tier; title: string; hint: string; items: AttentionItem[]; count: number}[] {
  return TIERS.map(t => {
    const list = items.filter(i => i.tier === t.tier);
    return {...t, items: list, count: list.reduce((n, i) => n + i.count, 0)};
  }).filter(g => g.items.length);
}

// The badge number: everything except open access that is not ending soon.
export function attentionCount(items: AttentionItem[]): {count: number; urgent: number} {
  let count = 0, urgent = 0;
  for (const i of items) {
    if (i.tier === 'open') continue;
    count += i.count;
    if (i.tier === 'now') urgent += i.count;
  }
  return {count, urgent};
}

// The inbox's kind filter, kept in the URL query (?show=).
export type AttentionFilter = 'all' | 'approvals' | 'blocked' | 'grants' | 'errors' | 'runs';
export const ATTENTION_FILTERS: [AttentionFilter, string][] = [['all', 'All'], ['approvals', 'Approvals'], ['blocked', 'Blocked agents'], ['grants', 'Open grants'], ['errors', 'Errors'], ['runs', 'Runs']];
const FILTER_KINDS: Record<Exclude<AttentionFilter, 'all'>, AttentionKind[]> = {
  approvals: ['request', 'package', 'more-requests', 'devlink', 'preview', 'gate'], blocked: ['report'], grants: ['web-window', 'lan-grant'],
  errors: ['sandbox-error', 'project-error', 'revision-error'], runs: ['run-failed', 'run-stuck', 'gate'],
};
export const attentionFilter = (value: string | null): AttentionFilter => ATTENTION_FILTERS.some(([k]) => k === value) ? value as AttentionFilter : 'all';
export const filterAttention = (items: AttentionItem[], f: AttentionFilter) => f === 'all' ? items : items.filter(i => FILTER_KINDS[f].includes(i.kind));
