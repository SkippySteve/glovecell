// Internet section: data source and actions.
//
// Today: web access state from sandbox-control GET /api/workspace (learning),
// request lists from GET /api/egress, actions on POST /egress. sandbox-control
// returns at most the newest `limit` (≤ 500) rows per list, so paging here is
// client side over that window; the UI says when the list is truncated.
import {sandboxObservations, type EgressState, type Observation, type ObservationPage, type SandboxDetail} from '../../api';

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

export interface RequestList { total: number; items: Observation[]; truncated: boolean }
export interface Requests { pending: RequestList; approved: RequestList }
const list = (l: ObservationPage['pending'] | undefined): RequestList => ({total: l?.total || 0, items: l?.items || [], truncated: (l?.total || 0) > (l?.items || []).length});

export async function loadRequests(workspace: string, filter: string): Promise<Requests> {
  const page = await sandboxObservations(workspace, filter, FETCH_LIMIT);
  return {pending: list(page.pending), approved: list(page.approved)};
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
