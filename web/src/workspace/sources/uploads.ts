// Uploads ("Upload into sandbox"): data source, the part-by-part driver and
// pure helpers. The file is read in this browser and sent in parts; the sandbox
// writes it. Nothing here touches the operator's other sites or credentials.
//
// Project sandboxes (console backend, web/uploads.go):
//   POST /api/v1/workspaces/<name>/uploads {name, destination, size, sha256, overwrite} -> UploadJob
//   PUT  /api/v1/workspaces/<name>/uploads/<id>/data   (X-Upload-Offset, application/octet-stream)
//   GET  /api/v1/workspaces/<name>/uploads[/<id>] · POST .../uploads/<id>/cancel {}
// sandbox-control workspaces, through the /sandboxes mount:
//   GET  /api/downloads?workspace=  ({uploads, uploadChunkBytes, uploadList} when supported)
//   POST /upload-start {name, destination, size, sha256, overwrite} · POST /upload-data?workspace=&id=&offset= (raw part)
//   GET  /api/upload?workspace=&id= · POST /upload-cancel {id}
import {api, failure, sandboxGet, sandboxGetOptional, sandboxPostBytes, sandboxPostJSON, type Session} from '../../api';
import {DEFAULT_MAX_BYTES} from './downloads';
import {Sha256} from './sha256';

export type UploadState = 'starting' | 'receiving' | 'finishing' | 'done' | 'failed' | 'cancelled';
export interface UploadJob {
  id: string; workspace: string; name: string; destination: string; size: number; received: number; written: number; overwrite: boolean;
  expectedSha256: string; sha256: string; path: string; state: UploadState; code: string; error: string; chunkBytes: number; maxBytes: number;
  requestedBy: string; started: number; finished: number;
}
export interface UploadRequest { name: string; destination: string; size: number; sha256: string; overwrite: boolean }
export interface UploadAvailability { available: boolean; reason: string; maxBytes: number }
export interface UploadSource {
  status?: () => Promise<UploadAvailability>;
  available: boolean; reason: string; maxBytes: number; chunkBytes: number;
  start(r: UploadRequest): Promise<UploadJob>;
  send(job: UploadJob, offset: number, part: Uint8Array): Promise<UploadJob>;
  get(id: string): Promise<UploadJob>;
  list(): Promise<UploadJob[]>;
  cancel(id: string): Promise<void>;
}

export const UPLOAD_TERMINAL: UploadState[] = ['done', 'failed', 'cancelled'];
export const DEFAULT_CHUNK = 8 * 1024 ** 2;
const str = (v: unknown) => typeof v === 'string' ? v : '';
const num = (v: unknown, fallback = 0) => typeof v === 'number' && Number.isFinite(v) ? v : fallback;
const time = (v: unknown) => { const t = typeof v === 'number' ? v * 1000 : Date.parse(str(v)); return Number.isFinite(t) ? t : 0; };
const list = <T>(v: unknown): T[] => Array.isArray(v) ? v as T[] : [];
const STATES: UploadState[] = ['starting', 'receiving', 'finishing', 'done', 'failed', 'cancelled'];

export function toUpload(raw: unknown): UploadJob {
  const u = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>;
  return {id: str(u.id), workspace: str(u.workspace), name: str(u.name), destination: str(u.destination), size: num(u.size), received: num(u.received), written: num(u.written),
    overwrite: u.overwrite === true, expectedSha256: str(u.expectedSha256), sha256: str(u.sha256), path: str(u.path), state: STATES.find(s => s === u.state) || 'failed',
    code: str(u.code), error: str(u.error), chunkBytes: num(u.chunkBytes), maxBytes: num(u.maxBytes), requestedBy: str(u.requestedBy), started: time(u.started), finished: time(u.finished)};
}

export function projectUploads(name: string, session: Session): UploadSource {
  const base = 'workspaces/' + encodeURIComponent(name) + '/uploads';
  const enabled = session.uploads === true;
  return {
    available: enabled,
    reason: enabled ? '' : session.downloads ? 'This controller release has no uploads yet; it gets them with the next controller image.' : 'Uploads are off on this controller (DOWNLOADS_ENABLED is not set).',
    maxBytes: session.downloadMaxBytes || DEFAULT_MAX_BYTES,
    chunkBytes: session.uploadChunkBytes || DEFAULT_CHUNK,
    start: async r => toUpload(await api<unknown>(base, 'POST', {name: r.name, destination: r.destination, size: r.size, ...(r.sha256 ? {sha256: r.sha256} : {}), overwrite: r.overwrite})),
    send: async (job, offset, part) => {
      const response = await fetch('/api/v1/' + base + '/' + encodeURIComponent(job.id) + '/data', {method: 'PUT', credentials: 'same-origin', headers: {'Content-Type': 'application/octet-stream', 'X-Upload-Offset': String(offset)}, body: part as BodyInit});
      if (!response.ok) throw await failure(response);
      return toUpload(await response.json());
    },
    get: async id => toUpload(await api<unknown>(base + '/' + encodeURIComponent(id))),
    list: async () => list(await api<unknown>(base)).map(toUpload),
    cancel: async id => { await api<unknown>(base + '/' + encodeURIComponent(id) + '/cancel', 'POST', {}); },
  };
}

// sandbox-control workspaces. Availability comes from /api/downloads: a release
// without uploads says so instead of failing on the first part.
export function sandboxUploads(id: string): UploadSource {
  let maxBytes = DEFAULT_MAX_BYTES, chunkBytes = DEFAULT_CHUNK;
  const overview = () => sandboxGetOptional<Record<string, unknown>>('/api/downloads', {workspace: id});
  return {
    available: true, reason: '', get maxBytes() { return maxBytes; }, get chunkBytes() { return chunkBytes; },
    status: async () => {
      const raw = await overview();
      if (!raw || raw.uploads === undefined) return {available: false, reason: 'This sandbox-control release has no uploads yet.', maxBytes: 0};
      maxBytes = num(raw.maxBytes) || maxBytes;
      chunkBytes = num(raw.uploadChunkBytes) || chunkBytes;
      return {available: raw.enabled === true && raw.uploads === true, reason: str(raw.reason) || (raw.uploads === true ? '' : 'Uploads are not enabled for this workspace.'), maxBytes};
    },
    start: async r => toUpload(await sandboxPostJSON<unknown>('/upload-start', id, {name: r.name, destination: r.destination, size: String(r.size), sha256: r.sha256, overwrite: r.overwrite ? 'on' : ''})),
    send: async (job, offset, part) => toUpload(await sandboxPostBytes<unknown>('/upload-data', {workspace: id, id: job.id, offset: String(offset)}, part)),
    get: async uid => toUpload(await sandboxGet<unknown>('/api/upload', {workspace: id, id: uid})),
    list: async () => { const raw = await overview(); return raw ? list(raw.uploadList).map(toUpload) : []; },
    cancel: async uid => { await sandboxPostJSON<unknown>('/upload-cancel', id, {id: uid}); },
  };
}

export interface UploadProgress { sent: number; total: number; job: UploadJob }
export interface UploadResult { job: UploadJob; browserSha256: string }

// Sends file part by part, hashing each part as it goes, then waits for the
// sandbox's result. Only one part is in memory at a time. A thrown error or an
// abort leaves the upload to the backend, which keeps nothing.
export async function sendFile(source: UploadSource, file: Blob, job: UploadJob, opts: {onProgress?: (p: UploadProgress) => void; signal?: AbortSignal; pollMs?: number} = {}): Promise<UploadResult> {
  const hash = new Sha256();
  const step = job.chunkBytes > 0 ? job.chunkBytes : source.chunkBytes;
  let current = job;
  for (let offset = 0; offset < file.size; offset += step) {
    if (opts.signal?.aborted) throw new Error('Upload cancelled.');
    const part = new Uint8Array(await file.slice(offset, Math.min(file.size, offset + step)).arrayBuffer());
    hash.update(part);
    current = await source.send(current, offset, part);
    opts.onProgress?.({sent: offset + part.length, total: file.size, job: current});
  }
  const browserSha256 = hash.hex();
  while (!UPLOAD_TERMINAL.includes(current.state)) {
    await new Promise(resolve => setTimeout(resolve, opts.pollMs ?? 700));
    current = await source.get(current.id);
  }
  return {job: current, browserSha256};
}

// --- pure helpers ---

// A file name safe as the last path segment of the default destination.
export function safeFileName(name: string): string {
  const base = name.split(/[\\/]/).pop() || '';
  const clean = base.replace(/[^A-Za-z0-9._+-]+/g, '_').replace(/^[._]+|[._]+$/g, '').slice(-128);
  return clean || 'upload';
}
export const defaultUploadDestination = (name: string) => '~/uploads/' + safeFileName(name);

export function uploadSummary(u: UploadJob, browserSha256 = ''): string {
  switch (u.state) {
    case 'starting': return 'Starting: the sandbox is checking the destination.';
    case 'receiving': return 'Uploading…';
    case 'finishing': return 'The sandbox is checking and saving the file…';
    case 'done': return `Saved ${u.path || u.destination} · ${u.written.toLocaleString()} bytes` + (browserSha256 ? (browserSha256 === u.sha256 ? ' · matches what your browser read' : ' · DOES NOT match what your browser read') : '');
    case 'cancelled': return u.error || 'Cancelled; nothing was kept.';
    default: return u.error || 'The upload failed; nothing was kept.';
  }
}

export const tooOld = (u: UploadJob | null, message = '') => u?.code === 'too-old' || /image too old/i.test(message);
