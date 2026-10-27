// Internet section: data source and actions.
//
// Today: web access state from sandbox-control GET /api/workspace (learning),
// request lists, web rules and list size from GET /api/egress, actions on POST
// /egress. Current sandbox-control releases page server-side (page, size); older
// ones return the newest `limit` (≤ 500) rows and the UI pages them locally.
import {sandboxObservations, type AllowOptions, type EgressCapacity, type EgressState, type Observation, type ObservationPage, type Paging, type PassthroughTool, type RuleEvent, type SandboxDetail, type WebRule} from '../../api';

export const WEB_WINDOWS: [number, string][] = [[15, '15 min'], [60, '1 hour'], [240, '4 hours'], [1440, '24 hours']];
export const PAGE_SIZE = 10;
// The most rows sandbox-control returns per list.
export const FETCH_LIMIT = 500;

export interface WebAccess { controllable: boolean; phase: EgressState; minutes: number; started: number; expires: number }

// Null when the workspace has fixed policy rules instead of learning egress.
export function webAccess(d: SandboxDetail): WebAccess | null {
  if (!d.learningEgress || !d.learning) return null;
  const l = d.learning;
  return {controllable: true, phase: l.phase, minutes: l.minutes, started: l.started * 1000, expires: l.phase === 'Locked' ? 0 : l.expires * 1000};
}

export interface RequestList { total: number; items: Observation[]; truncated: boolean; paging?: Paging }
export interface Requests {
  pending: RequestList; approved: RequestList;
  // Present on sandbox-control releases with web rules and server paging.
  rules?: WebRule[]; ruleHistory?: RuleEvent[]; tools?: {note: string; tools: PassthroughTool[]}; capacity?: EgressCapacity;
}
const list = (l: ObservationPage['pending'] | undefined): RequestList => ({total: l?.total || 0, items: l?.items || [], truncated: !l?.paging && (l?.total || 0) > (l?.items || []).length, paging: l?.paging});

// Page sizes for the request lists; 100 is sandbox-control's cap.
export const PAGE_SIZES = [10, 25, 50, 100];
export const DEFAULT_PAGE_SIZE = 25;
export const pageSize = (value: unknown): number => PAGE_SIZES.includes(Number(value)) ? Number(value) : DEFAULT_PAGE_SIZE;
// The page that keeps the first item shown when the size changes (pages from 1).
export const pageKeeping = (first: number, size: number): number => Math.max(1, Math.floor(Math.max(first - 1, 0) / size) + 1);

// page counts from 1. A release that pages server-side answers with `paging`;
// an older one returns its newest FETCH_LIMIT rows and the UI pages them.
export async function loadRequests(workspace: string, filter: string, page = 1, size = DEFAULT_PAGE_SIZE): Promise<Requests> {
  const body = await sandboxObservations(workspace, filter, FETCH_LIMIT, page, size);
  return {pending: list(body.pending), approved: list(body.approved), rules: body.rules, ruleHistory: body.ruleHistory, tools: body.passthroughTools, capacity: body.capacity};
}

// One page to show: from the server's paging, or sliced locally.
export function pageOf(l: RequestList, page: number, size: number): {rows: Observation[]; page: number; pages: number; from: number; to: number; total: number} {
  if (l.paging) return {rows: l.items, page: l.paging.page, pages: l.paging.pages, from: l.paging.first, to: l.paging.last, total: l.total};
  const p = paginate(l.items, page - 1, size);
  return {rows: p.rows, page: p.page + 1, pages: p.pages, from: p.from, to: p.to, total: l.items.length};
}

export const requestUrl = (o: Observation) => `${o.scheme}://${o.host}${o.port === 443 || o.port === 80 ? '' : ':' + o.port}${o.path}`;

// One page of a list, clamped to the last page.
export function paginate<T>(items: T[], page: number, size = PAGE_SIZE): {rows: T[]; page: number; pages: number; from: number; to: number} {
  const pages = Math.max(1, Math.ceil(items.length / size));
  const p = Math.min(Math.max(0, page), pages - 1);
  const rows = items.slice(p * size, p * size + size);
  return {rows, page: p, pages, from: rows.length ? p * size + 1 : 0, to: p * size + rows.length};
}

// POST /egress forms. A grouped item (fingerprints present) decides every
// fingerprint of its method and URL; older releases decide one fingerprint.
type Form = {path: '/egress'; fields: Record<string, string>};
export const webOpen = (minutes: number): Form => ({path: '/egress', fields: {action: 'learning-open', minutes: String(minutes)}});
export const webLearn = (minutes: number): Form => ({path: '/egress', fields: {action: 'learning-resume', minutes: String(minutes)}});
export const webStartFresh = (minutes: number): Form => ({path: '/egress', fields: {action: 'learning-start', minutes: String(minutes)}});
export const webLock = (minutes: number): Form => ({path: '/egress', fields: {action: 'learning-lock', minutes: String(minutes)}});
export const decideRequest = (o: Observation, approve: boolean): Form => ({path: '/egress', fields: {action: (approve ? 'approve-' : 'revoke-') + (o.fingerprints !== undefined ? 'endpoint' : 'learning'), observation: o.observation}});
export const clearUnapproved = (): Form => ({path: '/egress', fields: {action: 'clear-unapproved'}});
// Waiting requests not seen for `days`; nothing is approved.
export const dismissOlder = (days: number): Form => ({path: '/egress', fields: {action: 'clear-older', days: String(days)}});
// Every recorded fingerprint of one method and path.
export const approveExact = (host: string, method: string, path: string): Form => ({path: '/egress', fields: {action: 'approve-request', 'request.host': host, 'request.method': method, 'request.path': path}});

// A web rule draft, as the "More ways to allow" panel edits it.
export type RuleKind = 'path' | 'domain' | 'wildcard' | 'github-repo' | 'github-owner';
export interface RuleDraft {
  kind: RuleKind; host: string; path: string; wildcard: string; apex: boolean; github: string; write: boolean;
  methods: string[]; anyMethod: boolean; passthrough: boolean; expires: '1d' | '7d' | '30d' | 'permanent'; confirm: boolean;
}
export const METHODS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'];
export const EXPIRIES: [RuleDraft['expires'], string][] = [['1d', 'after 1 day'], ['7d', 'after 7 days'], ['30d', 'after 30 days'], ['permanent', 'never (until removed)']];

// The narrowest start: a path pattern one level up, the request's own method,
// one day.
export function draftFor(a?: AllowOptions): RuleDraft {
  return {kind: 'path', host: a?.host || '', path: a?.pattern || '/', wildcard: a?.wildcard || '', apex: false, github: a?.githubOwner ? `${a.githubOwner}/${a.githubRepo}` : '', write: false,
    methods: a?.method && METHODS.includes(a.method) ? [a.method] : ['GET'], anyMethod: false, passthrough: false, expires: '1d', confirm: false};
}

const github = (k: RuleKind) => k === 'github-repo' || k === 'github-owner';
// Any method, GitHub write and pass-through need the explicit confirmation.
export const needsConfirm = (d: RuleDraft) => (!github(d.kind) && (d.anyMethod || d.passthrough)) || (github(d.kind) && d.write);
export const passthroughAllowed = (d: RuleDraft) => d.kind === 'domain' || d.kind === 'wildcard';

// Plain-language risks of a draft, shown next to its options.
export function draftRisks(d: RuleDraft): string[] {
  const out: string[] = [];
  const base = d.wildcard.replace(/^\*\./, '');
  if (d.kind === 'path' && d.path.includes('*')) out.push('Every path matching the pattern is allowed, including ones not seen yet.');
  if (d.kind === 'domain') out.push(`Every path on ${d.host || 'the domain'} is allowed, including ones not seen yet.`);
  if (d.kind === 'wildcard') out.push(`Every current and future subdomain of ${base || 'the domain'} is allowed${d.apex ? ', and ' + base + ' itself' : ` (not ${base || 'the base domain'} itself)`}, including ones run by other teams of the same company.`);
  if (github(d.kind)) {
    if (d.kind === 'github-owner') out.push('Covers every repository the owner has now or creates later.');
    out.push('Read lets the agent clone and download the repository and its releases.');
    if (d.write) out.push('Write on a GitHub repo lets the agent push to it and change its issues, pull requests and releases.');
  } else if (d.passthrough) {
    out.push('With inspection off, the console cannot show or limit what is sent to this domain: History shows only the domain and connection, not requests.');
  } else if (d.anyMethod) {
    out.push('Any method on a whole domain lets the agent upload data there.');
  } else if (d.methods.some(m => ['POST', 'PUT', 'PATCH', 'DELETE'].includes(m))) {
    out.push('POST, PUT, PATCH or DELETE let the agent send data there, so it could upload files or secrets.');
  }
  if (d.expires === 'permanent' && (d.kind !== 'path' || d.anyMethod || d.write)) out.push('Permanent: the rule stays until you remove it.');
  return out;
}

// POST /egress action=web-rule-add. The server validates everything again.
export function addRule(d: RuleDraft): {path: '/egress'; fields: Record<string, string | string[]>} {
  const fields: Record<string, string | string[]> = {action: 'web-rule-add', 'rule.kind': d.kind, 'rule.expires': d.expires};
  if (d.kind === 'path' || d.kind === 'domain') fields['rule.host'] = d.host.trim();
  if (d.kind === 'path') fields['rule.path'] = d.path.trim();
  if (d.kind === 'wildcard') { fields['rule.wildcard'] = d.wildcard.trim(); if (d.apex) fields['rule.apex'] = 'on'; }
  if (github(d.kind)) { fields['rule.github'] = d.github.trim(); if (d.write) fields['rule.write'] = 'on'; }
  else if (d.passthrough && passthroughAllowed(d)) fields['rule.passthrough'] = 'on';
  else if (d.anyMethod) fields['rule.any_method'] = 'on';
  else fields['rule.method'] = d.methods;
  if (d.confirm) fields['rule.confirm'] = 'on';
  return {path: '/egress', fields};
}
export const removeRule = (r: WebRule): Form => ({path: '/egress', fields: {action: 'web-rule-remove', 'rule.id': r.id}});
