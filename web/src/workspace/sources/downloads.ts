// Downloads section ("Download into sandbox"): data source and actions.
//
// The section UI sees only DownloadSource and the pure helpers below. Today the
// project-sandbox source talks to the console backend:
//   POST /api/v1/workspaces/<name>/download-checks   {url}       -> DownloadCheck
//   GET  /api/v1/workspaces/<name>/download-checks/<id>          -> DownloadCheck
//   POST /api/v1/workspaces/<name>/downloads   {checkId, digest, destination, sha256, overwrite, acceptQueryChanges}
//   GET  /api/v1/workspaces/<name>/downloads[/<id>]               -> DownloadJob(s)
//   POST /api/v1/workspaces/<name>/downloads/<id>/cancel  {}
// A later backend (Glovecell) supplies another DownloadSource without touching the UI.
import {api, type Session} from '../../api';

export interface DownloadHop {
  url: string; redacted: string; host: string; status: number; crossHost: boolean; queryHash: string;
  contentLength: number; contentType: string; probe: string; refused: string;
}
export type CheckState = 'checking' | 'ready' | 'failed';
export interface DownloadCheck {
  id: string; workspace: string; input: string; state: CheckState; hops: DownloadHop[]; digest: string; error: string;
  defaultDestination: string; requestedBy: string; started: number; expires: number;
}
export type JobState = 'starting' | 'downloading' | 'done' | 'failed' | 'changed' | 'cancelled';
export interface DownloadChange { hop: number; expected: string; observed: string; queryOnly: boolean; reason: string }
export interface DownloadJob {
  id: string; workspace: string; checkId: string; input: string; state: JobState; hops: DownloadHop[]; hop: number; followed: string[];
  destination: string; overwrite: boolean; expectedSha256: string; acceptQueryChanges: boolean; maxBytes: number;
  bytes: number; total: number; sha256: string; path: string; code: string; error: string; change: DownloadChange | null; note: string;
  requestedBy: string; started: number; finished: number;
}
export interface DownloadRequest { destination: string; sha256: string; overwrite: boolean; acceptQueryChanges: boolean }

export interface DownloadSource {
  available: boolean;
  // Why downloads are unavailable, shown in place of the form.
  reason: string;
  maxBytes: number;
  check(url: string): Promise<DownloadCheck>;
  getCheck(id: string): Promise<DownloadCheck>;
  start(check: DownloadCheck, request: DownloadRequest): Promise<DownloadJob>;
  get(id: string): Promise<DownloadJob>;
  list(): Promise<DownloadJob[]>;
  cancel(id: string): Promise<void>;
}

export const TERMINAL: JobState[] = ['done', 'failed', 'changed', 'cancelled'];
export const DEFAULT_MAX_BYTES = 2 * 1024 ** 3;

const str = (v: unknown) => typeof v === 'string' ? v : '';
const num = (v: unknown, fallback = 0) => typeof v === 'number' && Number.isFinite(v) ? v : fallback;
const time = (v: unknown) => { const t = Date.parse(str(v)); return Number.isFinite(t) ? t : 0; };
const list = <T>(v: unknown): T[] => Array.isArray(v) ? v as T[] : [];

export function toHop(raw: unknown): DownloadHop {
  const h = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>;
  return {url: str(h.url), redacted: str(h.redacted), host: str(h.host), status: num(h.status), crossHost: h.crossHost === true, queryHash: str(h.queryHash),
    contentLength: num(h.contentLength, -1), contentType: str(h.contentType), probe: str(h.probe), refused: str(h.refused)};
}

const CHECK_STATES: CheckState[] = ['checking', 'ready', 'failed'];
export function toCheck(raw: unknown): DownloadCheck {
  const c = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>;
  return {id: str(c.id), workspace: str(c.workspace), input: str(c.input), state: CHECK_STATES.find(s => s === c.state) || 'failed', hops: list(c.hops).map(toHop),
    digest: str(c.digest), error: str(c.error), defaultDestination: str(c.defaultDestination), requestedBy: str(c.requestedBy), started: time(c.started), expires: time(c.expires)};
}

const JOB_STATES: JobState[] = ['starting', 'downloading', 'done', 'failed', 'changed', 'cancelled'];
export function toJob(raw: unknown): DownloadJob {
  const j = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>;
  const ch = j.change && typeof j.change === 'object' ? j.change as Record<string, unknown> : null;
  return {id: str(j.id), workspace: str(j.workspace), checkId: str(j.checkId), input: str(j.input), state: JOB_STATES.find(s => s === j.state) || 'failed',
    hops: list(j.hops).map(toHop), hop: num(j.hop), followed: list<unknown>(j.followed).map(str), destination: str(j.destination), overwrite: j.overwrite === true,
    expectedSha256: str(j.expectedSha256), acceptQueryChanges: j.acceptQueryChanges === true, maxBytes: num(j.maxBytes), bytes: num(j.bytes), total: num(j.total, -1),
    sha256: str(j.sha256), path: str(j.path), code: str(j.code), error: str(j.error), note: str(j.note), requestedBy: str(j.requestedBy), started: time(j.started), finished: time(j.finished),
    change: ch ? {hop: num(ch.hop), expected: str(ch.expected), observed: str(ch.observed), queryOnly: ch.queryOnly === true, reason: str(ch.reason)} : null};
}

// The console backend for project sandboxes (ProjectWorkspaces).
export function projectDownloads(name: string, session: Session): DownloadSource {
  const base = 'workspaces/' + encodeURIComponent(name);
  const enabled = session.downloads === true;
  return {
    available: enabled,
    reason: enabled ? '' : 'Downloads are off on this controller (DOWNLOADS_ENABLED is not set).',
    maxBytes: session.downloadMaxBytes || DEFAULT_MAX_BYTES,
    check: async url => toCheck(await api<unknown>(base + '/download-checks', 'POST', {url})),
    getCheck: async id => toCheck(await api<unknown>(base + '/download-checks/' + encodeURIComponent(id))),
    start: async (check, r) => toJob(await api<unknown>(base + '/downloads', 'POST', {checkId: check.id, digest: check.digest, destination: r.destination, ...(r.sha256 ? {sha256: r.sha256} : {}), overwrite: r.overwrite, acceptQueryChanges: r.acceptQueryChanges})),
    get: async id => toJob(await api<unknown>(base + '/downloads/' + encodeURIComponent(id))),
    list: async () => list(await api<unknown>(base + '/downloads')).map(toJob),
    cancel: async id => { await api<unknown>(base + '/downloads/' + encodeURIComponent(id) + '/cancel', 'POST', {}); },
  };
}

// sandbox-control workspaces have no download backend yet: sandbox-control
// cannot run a process inside them (no exec), which the download needs.
export function sandboxDownloads(): DownloadSource {
  const off = () => Promise.reject(new Error('Downloads are not available for this sandbox yet'));
  return {available: false, reason: 'sandbox-control workspaces cannot run the download helper yet: sandbox-control has no way to start a process inside them. Project sandboxes support downloads today.', maxBytes: 0,
    check: off, getCheck: off, start: off, get: off, list: () => Promise.resolve([]), cancel: off};
}

// --- pure helpers for the section ---

export interface UrlParts { origin: string; host: string; path: string; query: string }
export function splitUrl(raw: string): UrlParts {
  try {
    const u = new URL(raw);
    return {origin: u.protocol + '//', host: u.host, path: u.pathname, query: u.search};
  } catch {
    return {origin: '', host: '', path: raw, query: ''};
  }
}

// The query with every value hidden ("?X-Amz-Signature=•••&id=•••"): parameter
// names stay readable, signatures and tokens do not.
export function maskQuery(query: string): string {
  if (!query || query === '?') return query;
  return '?' + query.slice(1).split('&').map(p => { const i = p.indexOf('='); return i < 0 ? p : p.slice(0, i + 1) + '•••'; }).join('&');
}

export function checkUrl(raw: string): string | null {
  const v = raw.trim();
  if (!v) return 'Enter the URL to download.';
  if (/^http:\/\//i.test(v)) return 'Only https URLs are downloaded; http is refused.';
  if (!/^https:\/\//i.test(v)) return 'Enter a full https:// URL.';
  if (/\s/.test(v)) return 'The URL contains spaces; percent-encode them.';
  if (v.includes('#')) return 'Remove the #fragment; it is never sent to the server.';
  if (v.length > 4096) return 'The URL is longer than 4096 characters.';
  return null;
}

// Mirrors download.CheckDestination; the backend and the sandbox check again.
export function checkDestination(raw: string): string | null {
  const v = raw;
  if (!v) return 'Enter where to save the file.';
  if (v.length > 1024) return 'The path is longer than 1024 characters.';
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f\x7f\\]/.test(v)) return 'The path contains control characters or backslashes.';
  let rest: string;
  if (v.startsWith('~/')) rest = v.slice(2);
  else if (v.startsWith('/workspace/')) rest = v.slice('/workspace/'.length);
  else return 'Save under ~/ (the sandbox home) or /workspace/.';
  if (!rest || rest.endsWith('/')) return 'The path must name a file, not a folder.';
  if (rest.split('/').some(s => s === '' || s === '.' || s === '..')) return 'The path must not contain empty, . or .. parts.';
  return null;
}

export function checkSha256(raw: string): string | null {
  const v = raw.trim().toLowerCase().replace(/^sha256:/, '');
  return v === '' || /^[0-9a-f]{64}$/.test(v) ? null : 'A SHA-256 is 64 hexadecimal characters.';
}
export const normalSha256 = (raw: string) => raw.trim().toLowerCase().replace(/^sha256:/, '');

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return 'unknown size';
  if (n < 1024) return `${n} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

export const final = (c: DownloadCheck): DownloadHop | undefined => c.hops[c.hops.length - 1];
export const hostsOf = (hops: DownloadHop[]) => [...new Set(hops.map(h => h.host).filter(Boolean))];
export const tooLarge = (c: DownloadCheck, max: number) => { const f = final(c); return Boolean(f && max > 0 && f.contentLength > max); };

export function statusLabel(status: number): string {
  if (!status) return 'no answer';
  if (status >= 300 && status < 400) return `${status} redirect`;
  if (status === 200) return '200 OK';
  return String(status);
}

// One plain sentence for a job's state.
export function jobSummary(j: DownloadJob): string {
  switch (j.state) {
    case 'starting': return 'Starting: opening a one-time permission for the first URL.';
    case 'downloading': return j.total > 0 ? `Downloading: ${formatBytes(j.bytes)} of ${formatBytes(j.total)}` : `Downloading: ${formatBytes(j.bytes)}`;
    case 'done': return `Saved ${j.path || j.destination} · ${formatBytes(j.bytes)}`;
    case 'changed': return 'Stopped: the redirect chain changed since you checked it.';
    case 'cancelled': return 'Cancelled.';
    default: return j.error || 'The download failed.';
  }
}

export const progressPercent = (j: DownloadJob) => j.total > 0 ? Math.min(100, Math.round(j.bytes / j.total * 100)) : undefined;
