import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import {describe, expect, it} from 'vitest';
import type {DevLink, Preview} from '../../api';
import {projectCounts} from '../../orca';
import {ELSEWHERE} from '../sections/Settings';
import {SECTIONS} from '../../router';
import {browserSessionEntry, devlinkRow, linkCounts, linkRows, openBlocked, SERVER_STALE_MS, serverRow} from './previews';

// Invented sandbox-control bodies (GET /api/devlinks, GET /api/previews) at a fixed clock.
const fixture = JSON.parse(readFileSync(join(__dirname, '../../../fixtures/previews.json'), 'utf8')) as {now: number; devlinks: DevLink[]; previews: {expiredCount: number; requests: Preview[]}};
const now = fixture.now * 1000;
const link = (name: string) => fixture.devlinks.find(l => l.Name === name)!;
const server = (id: string) => fixture.previews.requests.find(p => p.id === id)!;

describe('dev link rows', () => {
  it('serves a live link and opens it on its own URL', () => {
    const r = devlinkRow(link('docs-site'));
    expect(r).toMatchObject({status: 'serving', label: 'Serving', variant: 'success', open: 'https://docs-site.dev.example.com/', name: 'docs-site.dev.example.com', kind: 'devlink', target: 'devlink-docs-site'});
    expect(r.urls).toEqual([{label: 'Dev link', url: 'https://docs-site.dev.example.com/'}]);
    expect(r.expires).toBe(link('docs-site').Expires * 1000);
    expect(r.checkedLabel).toBe('Scanned');
    expect(r.checked).toBe(link('docs-site').Scan.Finished * 1000);
    expect(openBlocked(r)).toBe('');
  });

  it('never offers Open for a link that is not live', () => {
    for (const l of fixture.devlinks.filter(x => !x.Live)) {
      const r = devlinkRow(l);
      expect(r.open, l.Name).toBe('');
      expect(openBlocked(r), l.Name).not.toBe('');
      // The address can still be copied: it lands on the verification card.
      expect(r.urls[0].url).toBe(l.URL);
    }
  });

  it('maps each sandbox-control state to a status', () => {
    const status = (n: string) => { const r = devlinkRow(link(n)); return [r.status, r.label]; };
    expect(status('storybook')).toEqual(['pending', 'Scanning']);
    expect(status('docs-preview')).toEqual(['pending', 'Pending approval']);
    expect(status('admin-console')).toEqual(['stale', 'Offline']);
    expect(status('api-mock')).toEqual(['stale', 'Expired']);
    expect(status('chart-demo')).toEqual(['inactive', 'Scan failed']);
    expect(status('old-ui')).toEqual(['inactive', 'Denied']);
    // Approved but not live (for example a listener that went quiet): approved, not serving.
    expect(status('held')).toEqual(['approved', 'Approved']);
  });

  it('says what the scan found on a pending link', () => {
    expect(devlinkRow(link('docs-preview')).note).toBe('Scan complete: 1 critical, 2 high, 1 possible secrets. Review the build below.');
  });

  it('dates a running scan by its start and a failed one carries the error', () => {
    const s = devlinkRow(link('storybook'));
    expect(s.checkedLabel).toBe('Scan started');
    expect(s.checked).toBe(link('storybook').Scan.Started * 1000);
    expect(devlinkRow(link('chart-demo')).note).toBe('Scan failed: the scanner timed out');
  });

  it('omits an unknown project from the location', () => {
    expect(devlinkRow(link('docs-preview')).where).toBe('port 5174');
    expect(devlinkRow(link('docs-site')).where).toBe('example / main · port 4173');
  });

  it('explains why Open is unavailable', () => {
    expect(openBlocked(devlinkRow(link('docs-preview')))).toBe('Opens after approval');
    expect(openBlocked(devlinkRow(link('held')))).toBe('Not serving right now');
    expect(openBlocked(devlinkRow(link('admin-console')))).toBe('App not running');
    expect(openBlocked(devlinkRow(link('api-mock')))).toBe('Approval expired');
    expect(openBlocked(devlinkRow(link('old-ui')))).toBe('Not approved');
  });
});

describe('detected server rows', () => {
  it('serves an approved listener seen recently, Tailscale first', () => {
    const r = serverRow(server('v2'), now);
    expect(r).toMatchObject({status: 'serving', open: 'https://preview-v2-ts.example.com', checkedLabel: 'Seen', target: 'server-v2', where: 'example / docs · port 8000'});
    expect(r.urls.map(u => u.label)).toEqual(['Tailscale', 'LAN']);
    expect(r.note).toBe('Reachable on Tailscale and LAN.');
  });

  it('lists only the channels the approval granted', () => {
    const r = serverRow(server('v6'), now);
    expect(r.urls).toEqual([{label: 'LAN', url: 'https://preview-v6.example.com'}]);
    expect(r.open).toBe('https://preview-v6.example.com');
  });

  it('reports an approved listener that went quiet as stale, without Open', () => {
    const r = serverRow(server('v3'), now);
    expect(r).toMatchObject({status: 'stale', label: 'Not seen', open: ''});
    expect(openBlocked(r)).toBe('App not running');
  });

  it('switches from serving to stale at the threshold', () => {
    const p = server('v2');
    expect(serverRow(p, p.seen * 1000 + SERVER_STALE_MS).status).toBe('serving');
    expect(serverRow(p, p.seen * 1000 + SERVER_STALE_MS + 1).status).toBe('stale');
  });

  it('has no URL to copy before approval', () => {
    for (const id of ['v1', 'v4', 'v5']) {
      const r = serverRow(server(id), now);
      expect(r.status, id).toBe('pending');
      expect(r.urls, id).toEqual([]);
      expect(r.open, id).toBe('');
    }
    expect(serverRow(server('v4'), now).label).toBe('Needs network binding');
    expect(serverRow(server('v5'), now).name).toBe('Unnamed server');
  });

  it('maps inactive states', () => {
    const base = server('v1');
    expect(serverRow({...base, state: 'Denied'}, now).status).toBe('inactive');
    expect(serverRow({...base, state: 'Revoked'}, now).status).toBe('inactive');
    expect(serverRow({...base, state: 'Expired'}, now).status).toBe('stale');
  });
});

describe('the combined list', () => {
  const rows = linkRows(fixture.devlinks, fixture.previews.requests, now);

  it('lists every dev link and server once', () => {
    expect(rows).toHaveLength(fixture.devlinks.length + fixture.previews.requests.length);
    expect(new Set(rows.map(r => r.key)).size).toBe(rows.length);
  });

  it('puts what waits on the operator first, then what can be opened', () => {
    const order = rows.map(r => r.status);
    const rank = {pending: 0, serving: 1, approved: 2, stale: 3, inactive: 4};
    expect(order.map(s => rank[s])).toEqual([...order.map(s => rank[s])].sort((a, b) => a - b));
    expect(rows[0].status).toBe('pending');
  });

  it('counts each status', () => {
    expect(linkCounts(rows)).toEqual({pending: 5, serving: 3, approved: 1, stale: 3, inactive: 2});
  });

  it('handles lists that have not loaded', () => {
    expect(linkRows(null, null, now)).toEqual([]);
    expect(linkCounts([])).toEqual({pending: 0, serving: 0, approved: 0, stale: 0, inactive: 0});
  });
});

describe('browser session entry', () => {
  it('is unavailable for both kinds of sandbox and says why', () => {
    for (const kind of ['sandbox', 'project'] as const) {
      const e = browserSessionEntry(kind);
      expect(e.available).toBe(false);
      expect(e.reason.length).toBeGreaterThan(0);
      expect(e.reason + ' ' + e.detail).toContain('PR #76');
    }
  });
});

describe('settings map', () => {
  it('links only to sections that exist', () => {
    const keys = SECTIONS.map(([key]) => key);
    for (const [key] of ELSEWHERE) expect(keys).toContain(key);
  });
});

describe('Orca project counts', () => {
  it('counts each record state', () => {
    expect(projectCounts([
      {name: 'a', repo: 'o/a', status: 'published'}, {name: 'b', repo: 'o/b', status: 'registered'},
      {name: 'c', repo: 'o/c', status: 'preparing'}, {name: 'd', repo: 'o/d', status: 'failed'}, {name: 'e', repo: 'o/e', status: 'registered'},
    ] as never)).toEqual({total: 5, published: 1, registered: 2, preparing: 1, failed: 1});
  });
});
