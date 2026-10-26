import {createHash, randomBytes} from 'node:crypto';
import {afterEach, describe, expect, it, vi} from 'vitest';
import {mergeHistory} from './history';
import {Sha256} from './sha256';
import {defaultUploadDestination, projectUploads, safeFileName, sandboxUploads, sendFile, toUpload, tooOld, uploadSummary, type UploadJob} from './uploads';

const sha = (b: Uint8Array) => createHash('sha256').update(b).digest('hex');
const json = (v: unknown, h: Record<string, string> = {}, status = 200) => new Response(JSON.stringify(v), {status, headers: {'Content-Type': 'application/json', ...h}});

describe('incremental SHA-256', () => {
  it('matches known vectors', () => {
    expect(new Sha256().hex()).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855');
    expect(new Sha256().update(new TextEncoder().encode('abc')).hex()).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
    const million = new Uint8Array(1_000_000).fill(0x61);
    expect(new Sha256().update(million).hex()).toBe('cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0');
  });

  it('gives the same hash however the input is split', () => {
    for (const size of [0, 1, 55, 56, 63, 64, 65, 119, 120, 128, 1000, 70_001]) {
      const data = new Uint8Array(randomBytes(size));
      const want = sha(data);
      for (const step of [1, 7, 63, 64, 65, 4096]) {
        const h = new Sha256();
        for (let i = 0; i < data.length; i += step) h.update(data.subarray(i, i + step));
        expect(h.hex(), `size ${size} step ${step}`).toBe(want);
      }
    }
  });
});

describe('upload mapping and helpers', () => {
  it('maps an upload and degrades unknown fields', () => {
    const u = toUpload({id: 'u1', state: 'done', size: 3, written: 3, sha256: 'a'.repeat(64), started: '2026-10-26T10:00:00Z', chunkBytes: 8388608});
    expect(u).toMatchObject({id: 'u1', state: 'done', written: 3, chunkBytes: 8388608, started: Date.parse('2026-10-26T10:00:00Z')});
    expect(toUpload({state: 'weird'}).state).toBe('failed');
    expect(toUpload(null)).toMatchObject({state: 'failed', size: 0, error: ''});
  });

  it('builds a safe default destination', () => {
    expect(defaultUploadDestination('Report 2026 (final).pdf')).toBe('~/uploads/Report_2026_final_.pdf');
    expect(safeFileName('../../etc/passwd')).toBe('passwd');
    expect(safeFileName('C:\\Users\\steve\\a.zip')).toBe('a.zip');
    expect(safeFileName('...')).toBe('upload');
  });

  it('summarises the sandbox result against the browser hash', () => {
    const done = toUpload({state: 'done', destination: '~/a', path: '~/a', written: 3, sha256: 'x'});
    expect(uploadSummary(done, 'x')).toMatch(/matches what your browser read/);
    expect(uploadSummary(done, 'y')).toMatch(/DOES NOT match/);
    expect(uploadSummary(toUpload({state: 'failed', error: 'a file already exists'}))).toBe('a file already exists');
    expect(tooOld(toUpload({state: 'failed', code: 'too-old'}))).toBe(true);
    expect(tooOld(null, 'image too old: this sandbox')).toBe(true);
    expect(tooOld(null, 'other')).toBe(false);
  });

  it('labels upload events in History', () => {
    const events = mergeHistory({downloads: [{at: 1, action: 'upload-done', id: 'u', by: 'akadmin', detail: 'a.pdf to ~/uploads/a.pdf'}]});
    expect(events[0]).toMatchObject({type: 'download', title: 'Upload finished'});
  });
});

describe('upload sources', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('project source: start, raw parts with offsets, and the browser hash', async () => {
    const calls: {url: string; method: string; headers: Record<string, string>; size: number; body: unknown}[] = [];
    let received = 0;
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
      const headers = (init.headers || {}) as Record<string, string>;
      const body = init.body instanceof Uint8Array ? init.body : init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({url, method: init.method || 'GET', headers, size: init.body instanceof Uint8Array ? init.body.length : 0, body});
      if (url.endsWith('/uploads') && init.method === 'POST') return json({id: 'u1', state: 'receiving', size: 20, chunkBytes: 8}, {}, 201);
      if (url.endsWith('/data')) {
        received += (init.body as Uint8Array).length;
        return json({id: 'u1', state: received === 20 ? 'done' : 'receiving', size: 20, received, written: received, sha256: received === 20 ? 'sandbox' : ''});
      }
      return json({});
    }));
    const src = projectUploads('tools', {downloads: true, uploads: true, downloadMaxBytes: 1024, uploadChunkBytes: 8});
    expect(src).toMatchObject({available: true, maxBytes: 1024, chunkBytes: 8});
    const data = new Uint8Array(randomBytes(20));
    const job = await src.start({name: 'a.bin', destination: '~/uploads/a.bin', size: 20, sha256: '', overwrite: false});
    const progress: number[] = [];
    const r = await sendFile(src, new Blob([data]), job, {onProgress: p => progress.push(p.sent)});
    expect(r.browserSha256).toBe(sha(data));
    expect(r.job.state).toBe('done');
    expect(progress).toEqual([8, 16, 20]);
    expect(calls[0]).toMatchObject({url: '/api/v1/workspaces/tools/uploads', method: 'POST', body: {name: 'a.bin', destination: '~/uploads/a.bin', size: 20, overwrite: false}});
    const parts = calls.filter(c => c.url.endsWith('/data'));
    expect(parts.map(p => [p.method, p.headers['X-Upload-Offset'], p.headers['Content-Type'], p.size])).toEqual([
      ['PUT', '0', 'application/octet-stream', 8], ['PUT', '8', 'application/octet-stream', 8], ['PUT', '16', 'application/octet-stream', 4],
    ]);
    expect(parts[0].url).toBe('/api/v1/workspaces/tools/uploads/u1/data');
  });

  it('project source: says why uploads are unavailable', () => {
    expect(projectUploads('tools', {}).available).toBe(false);
    expect(projectUploads('tools', {downloads: true}).reason).toMatch(/no uploads yet/);
  });

  it('stops sending when the backend refuses a part', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('the upload is cancelled', {status: 409})));
    const src = projectUploads('tools', {uploads: true});
    const job: UploadJob = toUpload({id: 'u1', state: 'receiving', size: 20, chunkBytes: 8});
    await expect(sendFile(src, new Blob([new Uint8Array(20)]), job)).rejects.toThrow('the upload is cancelled');
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(1);
  });

  it('sandbox-control source: availability, CSRF header on raw parts, and polling the result', async () => {
    const calls: {url: string; method: string; headers: Record<string, string>; body: string}[] = [];
    let polls = 0;
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
      calls.push({url, method: init.method || 'GET', headers: (init.headers || {}) as Record<string, string>, body: init.body instanceof Uint8Array ? `<${init.body.length} bytes>` : String(init.body || '')});
      if (url.startsWith('/sandboxes/workspaces')) return json([], {'X-Sandbox-CSRF': 'tok'});
      if (url.startsWith('/sandboxes/api/downloads')) return json({enabled: true, uploads: true, uploadChunkBytes: 4, maxBytes: 1073741824, uploadList: [{id: 'u0', state: 'done'}]});
      if (url.startsWith('/sandboxes/upload-start')) return json({id: 'u1', state: 'receiving', size: 6, chunkBytes: 4});
      if (url.startsWith('/sandboxes/upload-data')) return json({id: 'u1', state: url.includes('offset=4') ? 'finishing' : 'receiving', size: 6});
      if (url.startsWith('/sandboxes/api/upload')) { polls++; return json({id: 'u1', state: polls > 1 ? 'done' : 'finishing', sha256: 'abc', written: 6}); }
      return json({});
    }));
    const src = sandboxUploads('orca-pilot');
    expect(await src.status!()).toEqual({available: true, reason: '', maxBytes: 1073741824});
    expect(src.chunkBytes).toBe(4);
    const job = await src.start({name: 'a', destination: '~/uploads/a', size: 6, sha256: '', overwrite: true});
    const r = await sendFile(src, new Blob([new Uint8Array(6)]), job, {pollMs: 1});
    expect(r.job).toMatchObject({state: 'done', sha256: 'abc'});
    const start = calls.find(c => c.url === '/sandboxes/upload-start')!;
    expect(Object.fromEntries(new URLSearchParams(start.body))).toEqual({workspace: 'orca-pilot', csrf: 'tok', name: 'a', destination: '~/uploads/a', size: '6', sha256: '', overwrite: 'on'});
    const parts = calls.filter(c => c.url.startsWith('/sandboxes/upload-data'));
    expect(parts.map(p => [p.method, p.url, p.headers['X-Sandbox-CSRF'], p.headers['Content-Type'], p.body])).toEqual([
      ['POST', '/sandboxes/upload-data?workspace=orca-pilot&id=u1&offset=0', 'tok', 'application/octet-stream', '<4 bytes>'],
      ['POST', '/sandboxes/upload-data?workspace=orca-pilot&id=u1&offset=4', 'tok', 'application/octet-stream', '<2 bytes>'],
    ]);
    expect((await src.list())[0].id).toBe('u0');
  });

  it('sandbox-control source: a release without uploads is unavailable, not broken', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({enabled: true, maxBytes: 1})));
    expect(await sandboxUploads('herdr').status!()).toMatchObject({available: false, reason: 'This sandbox-control release has no uploads yet.'});
    vi.stubGlobal('fetch', vi.fn(async () => new Response('not found', {status: 404})));
    expect((await sandboxUploads('herdr').status!()).available).toBe(false);
  });
});
