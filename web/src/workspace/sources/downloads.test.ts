import {afterEach, describe, expect, it, vi} from 'vitest';
import {parse, projectHref} from '../../router';
import {checkDestination, checkSha256, checkUrl, final, formatBytes, hostsOf, jobSummary, maskQuery, normalSha256, progressPercent, projectDownloads, sandboxDownloads, splitUrl, statusLabel, toCheck, toJob, tooLarge} from './downloads';

// A check body as the console backend serves it (web/downloads.go Check).
const checkBody = {
  id: 'abc123abc123', workspace: 'tools', input: 'https://github.com/o/r/releases/download/v1/tool.tgz', state: 'ready', digest: 'd'.repeat(64), error: '',
  defaultDestination: '~/downloads/tool.tgz', requestedBy: 'https://auth.example.com#steve', started: '2026-10-26T10:00:00Z', expires: '2026-10-26T10:15:00Z',
  hops: [
    {url: 'https://github.com/o/r/releases/download/v1/tool.tgz', redacted: 'https://github.com/o/r/releases/download/v1/tool.tgz', host: 'github.com', status: 302, contentLength: -1, probe: 'HEAD'},
    {url: 'https://objects.example.com/o/tool.tgz?X-Amz-Signature=s3cr3t&X-Amz-Date=1', redacted: 'https://objects.example.com/o/tool.tgz?<query sha256:0123456789abcdef>', host: 'objects.example.com', status: 200, crossHost: true, queryHash: '0123456789abcdef', contentLength: 3145728, contentType: 'application/gzip', probe: 'GET'},
  ],
};

describe('download contract mapping', () => {
  it('maps a check and its hops', () => {
    const c = toCheck(checkBody);
    expect(c.state).toBe('ready');
    expect(c.hops).toHaveLength(2);
    expect(c.hops[1]).toMatchObject({host: 'objects.example.com', crossHost: true, status: 200, contentLength: 3145728, probe: 'GET', refused: ''});
    expect(c.hops[0].crossHost).toBe(false);
    expect(c.started).toBe(Date.parse('2026-10-26T10:00:00Z'));
    expect(final(c)?.contentType).toBe('application/gzip');
    expect(hostsOf(c.hops)).toEqual(['github.com', 'objects.example.com']);
  });

  it('degrades unknown or missing fields instead of failing', () => {
    const c = toCheck({state: 'weird', hops: [{}]});
    expect(c.state).toBe('failed');
    expect(c.hops[0]).toMatchObject({url: '', status: 0, contentLength: -1, crossHost: false});
    const j = toJob({state: 'downloading', bytes: 5, change: {hop: 1, queryOnly: true}});
    expect(j).toMatchObject({state: 'downloading', bytes: 5, total: -1, finished: 0, change: {hop: 1, queryOnly: true, expected: ''}});
    expect(toJob(null).state).toBe('failed');
  });

  it('flags a file the server announces above the limit', () => {
    const c = toCheck(checkBody);
    expect(tooLarge(c, 2 * 1024 ** 3)).toBe(false);
    expect(tooLarge(c, 1024)).toBe(true);
  });
});

describe('query masking', () => {
  it('hides every value and keeps parameter names', () => {
    expect(maskQuery('?X-Amz-Signature=s3cr3t&X-Amz-Date=1&flag')).toBe('?X-Amz-Signature=•••&X-Amz-Date=•••&flag');
    expect(maskQuery('')).toBe('');
    const p = splitUrl(checkBody.hops[1].url);
    expect(p).toEqual({origin: 'https://', host: 'objects.example.com', path: '/o/tool.tgz', query: '?X-Amz-Signature=s3cr3t&X-Amz-Date=1'});
    expect(maskQuery(p.query)).not.toContain('s3cr3t');
  });
});

describe('form checks mirror the backend', () => {
  it('URL', () => {
    expect(checkUrl('https://files.example.com/a.bin')).toBeNull();
    expect(checkUrl('http://files.example.com/a.bin')).toMatch(/http is refused/);
    expect(checkUrl('files.example.com/a')).toMatch(/https/);
    expect(checkUrl('https://files.example.com/a b')).toMatch(/spaces/);
    expect(checkUrl('https://files.example.com/a#x')).toMatch(/fragment/);
  });

  it('destination: home or /workspace, no traversal, names a file', () => {
    for (const ok of ['~/downloads/a.bin', '/workspace/repo/a.tgz', '~/a']) expect(checkDestination(ok)).toBeNull();
    for (const bad of ['', '/etc/passwd', '/workspace', '/workspace/', '~/', '~/a/', '~/../etc/x', '/workspace/a/../../etc', '~/a//b', '~/./a', 'rel/a', '/dev/null', '~/a\\b', '~/a\u0000b']) expect(checkDestination(bad)).not.toBeNull();
  });

  it('SHA-256', () => {
    expect(checkSha256('')).toBeNull();
    expect(checkSha256('sha256:' + 'A'.repeat(64))).toBeNull();
    expect(normalSha256(' SHA256:' + 'A'.repeat(64))).toBe('a'.repeat(64));
    expect(checkSha256('abc')).not.toBeNull();
  });
});

describe('labels', () => {
  it('formats sizes, statuses and job summaries', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(3 * 1024 ** 2)).toBe('3.0 MiB');
    expect(formatBytes(2 * 1024 ** 3)).toBe('2.0 GiB');
    expect(formatBytes(-1)).toBe('unknown size');
    expect(statusLabel(302)).toBe('302 redirect');
    expect(statusLabel(0)).toBe('no answer');
    const j = toJob({state: 'downloading', bytes: 512, total: 1024});
    expect(progressPercent(j)).toBe(50);
    expect(jobSummary(j)).toBe('Downloading: 512 B of 1.0 KiB');
    expect(jobSummary(toJob({state: 'done', path: '~/a', bytes: 2048}))).toBe('Saved ~/a · 2.0 KiB');
    expect(progressPercent(toJob({state: 'downloading', bytes: 1}))).toBeUndefined();
  });
});

describe('sources', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('project source calls the console API and sends the approved chain digest', async () => {
    const calls: {url: string; method: string; body: unknown}[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
      calls.push({url, method: init.method || 'GET', body: init.body ? JSON.parse(String(init.body)) : undefined});
      return new Response(JSON.stringify(url.includes('download-checks') ? checkBody : {id: 'job1', state: 'starting'}), {status: 200, headers: {'Content-Type': 'application/json'}});
    }));
    const src = projectDownloads('tools', {downloads: true, downloadMaxBytes: 1024});
    expect(src.available).toBe(true);
    expect(src.maxBytes).toBe(1024);
    const c = await src.check('https://github.com/o/r/releases/download/v1/tool.tgz');
    await src.start(c, {destination: '~/downloads/tool.tgz', sha256: '', overwrite: false, acceptQueryChanges: true});
    await src.cancel('job1');
    expect(calls.map(c => c.method + ' ' + c.url)).toEqual([
      'POST /api/v1/workspaces/tools/download-checks',
      'POST /api/v1/workspaces/tools/downloads',
      'POST /api/v1/workspaces/tools/downloads/job1/cancel',
    ]);
    expect(calls[1].body).toEqual({checkId: 'abc123abc123', digest: 'd'.repeat(64), destination: '~/downloads/tool.tgz', overwrite: false, acceptQueryChanges: true});
  });

  it('is unavailable until the controller enables it, and for sandbox-control workspaces', async () => {
    expect(projectDownloads('tools', {}).available).toBe(false);
    const s = sandboxDownloads();
    expect(s.available).toBe(false);
    expect(s.reason).toMatch(/sandbox-control/);
    await expect(s.check('https://x.example.com/a')).rejects.toThrow();
    expect(await s.list()).toEqual([]);
  });

  it('routes the Downloads section', () => {
    expect(parse('#/projects/tools/downloads')).toEqual({view: 'project', name: 'tools', section: 'downloads'});
    expect(projectHref('tools', 'downloads')).toBe('#/projects/tools/downloads');
  });
});
