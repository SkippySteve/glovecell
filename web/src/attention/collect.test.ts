import {describe, expect, it} from 'vitest';
import type {AgentReport, DevLink, ExistingSandbox, Observation, Preview, Project, SandboxDetail} from '../api';
import type {RunSummary} from '../factory/api';
import type {Requests} from '../workspace/sources/internet';
import {toLanView} from '../workspace/sources/lan';
import {attentionCount, attentionFilter, collectAttention, compareAttention, filterAttention, groupAttention, REQUESTS_SHOWN, SOON_MS, type AttentionItem, type SandboxInput} from './collect';

const NOW = Date.parse('2030-01-02T12:00:00Z');
const s = (unixOffset: number) => Math.floor((NOW + unixOffset) / 1000);
const isoAt = (offset: number) => new Date(NOW + offset).toISOString();
const MIN = 60_000, HOUR = 3600_000;

const summary = (id: string, extra: Partial<ExistingSandbox> = {}): ExistingSandbox => ({id, namespace: 'agent-workspace-' + id, sandbox: 'workspace', mode: 'Running', revision: 1, status: 'Active', pending: 0, approvals: 0, egress: 'Locked', egressExpires: 0, egressControl: true, ...extra});
const detail = (id: string, extra: Partial<SandboxDetail> = {}): SandboxDetail => ({id, namespace: 'agent-workspace-' + id, sandbox: 'workspace', mode: 'Running', revision: {id: 3, status: 'Active', at: s(-HOUR)}, learningEgress: true, tailnetReady: true, serviceOwner: false,
  learning: {phase: 'Locked', minutes: 60, started: s(-HOUR), expires: 0}, providerHosts: [], previewPending: 0, devLinkPending: 0, egressCounts: {pending: 0, approved: 0}, links: {}, services: [], revisions: [], ...extra});
const obs = (n: number, extra: Partial<Observation> = {}): Observation => ({host: 'h' + n + '.example.com', method: 'GET', path: '/p' + n, scheme: 'https', port: 443, observation: 'o' + n, first: s(-n * MIN), last: s(-n * MIN + 1000), fingerprints: 1, ...extra});
const requests = (items: Observation[], total = items.length): Requests => ({pending: {total, items, truncated: false}, approved: {total: 0, items: [], truncated: false}});
const report = (id: number, extra: Partial<AgentReport> = {}): AgentReport => ({id, kind: 'web', resource: 'https://blocked.example.com/x', reason: 'needs it', count: 1, first: isoAt(-30 * MIN), last: isoAt(-5 * MIN), ...extra});
const lanWith = (deadlineOffset: number, state = 'Open') => toLanView({workspace: 'a', enabled: true, running: true, podUid: 'pod-1', windows: [900, 3600], limits: {grantsPerWorkspace: 8, portsPerGrant: 32},
  hosts: [{address: '192.168.1.50', listed: true, all: false, ports: [22], grant: {mode: 'ports', ports: [22], openedBy: 'operator', openedAt: isoAt(-HOUR), deadline: isoAt(deadlineOffset), state, reason: ''}, learned: [], learnedGrant: [], suggestedAllowLine: ''},
    {address: '192.168.1.60', listed: true, all: false, ports: [22], grant: null, learned: [], learnedGrant: [], suggestedAllowLine: ''}], history: []});
const devlink = (name: string, state = 'Pending'): DevLink => ({Name: name, State: state, Requested: s(-20 * MIN), RulesText: 'GET /', Scan: {Status: 'Complete'}, Counts: {critical: 0, high: 1, secrets: 0}} as unknown as DevLink);
const preview = (id: string): Preview => ({id, state: 'Pending', proposedName: 'vite', proposedRulesText: 'GET /', rulesText: '', first: s(-40 * MIN), listener: {project: 'p', worktree: 'w', port: 5173}} as unknown as Preview);
const project = (name: string, ready: boolean): Project => ({metadata: {name, generation: 2}, spec: {repository: 'example/' + name, desiredState: 'Running'},
  status: {phase: ready ? 'Ready' : 'Preparing', observedGeneration: 2, conditions: [{type: 'Ready', status: ready ? 'True' : 'False', reason: ready ? 'Ready' : 'PreparationPending', message: ready ? 'Ready' : 'Forgejo rule falls short', observedGeneration: 2}]}});
const run = (id: string, extra: Partial<RunSummary>): RunSummary => ({id, objective: 'objective ' + id, host: 'agent-workspace-a/pod', collectedAt: isoAt(-10 * MIN), tasks: 2, ...extra});

const kinds = (items: AttentionItem[]) => items.map(i => i.kind);

describe('collectAttention', () => {
  it('turns every source into items', () => {
    const a: SandboxInput = {summary: summary('a', {pending: 2, approvals: 2}), detail: detail('a', {previewPending: 1, devLinkPending: 1, egressCounts: {pending: 2, approved: 0}}),
      requests: requests([obs(1), obs(2, {package: {ecosystem: 'npm', name: 'left-pad', version: '1.0.0'}})]), reports: [report(7)], lan: lanWith(2 * HOUR), devlinks: [devlink('docs'), devlink('live', 'Approved')], previews: [preview('v1')]};
    const items = collectAttention({sandboxes: [a], projects: [project('ok', true), project('stuck', false)], runs: []}, NOW);
    expect(kinds(items).sort()).toEqual(['devlink', 'lan-grant', 'package', 'preview', 'project-error', 'report', 'request'].sort());
    const pkg = items.find(i => i.kind === 'package')!;
    expect(pkg).toMatchObject({title: 'Package npm left-pad 1.0.0', tier: 'decide', owner: {kind: 'sandbox', id: 'a'}});
    expect(items.find(i => i.kind === 'lan-grant')).toMatchObject({tier: 'open', deadline: NOW + 2 * HOUR, lan: {address: '192.168.1.50', podUid: 'pod-1', open: 1}});
    expect(items.find(i => i.kind === 'report')).toMatchObject({tier: 'now', reportWeb: undefined});
  });

  it('reports sandboxes and revisions in error, and nothing for a quiet sandbox', () => {
    const items = collectAttention({sandboxes: [
      {summary: summary('broken', {status: 'Failed'}), detail: detail('broken', {revision: {id: 4, status: 'Rejected', error: 'policy rejected', at: s(-HOUR)}})},
      {summary: summary('quiet'), detail: detail('quiet'), requests: null, lan: null, reports: null, devlinks: [], previews: []},
    ], projects: []}, NOW);
    expect(items.map(i => [i.kind, i.owner.id, i.tier])).toEqual([['sandbox-error', 'broken', 'now'], ['revision-error', 'broken', 'now']]);
  });

  it('lists a few requests one by one and the rest as one counted item', () => {
    const many = Array.from({length: 7}, (_, i) => obs(i + 1));
    const items = collectAttention({sandboxes: [{summary: summary('a', {pending: 30}), detail: detail('a', {egressCounts: {pending: 30, approved: 0}}), requests: requests(many, 30)}], projects: []}, NOW);
    expect(items.filter(i => i.kind === 'request')).toHaveLength(REQUESTS_SHOWN);
    expect(items.find(i => i.kind === 'more-requests')).toMatchObject({count: 30 - REQUESTS_SHOWN, title: `${30 - REQUESTS_SHOWN} more web requests waiting`});
    expect(attentionCount(items).count).toBe(30);
  });

  it('falls back to the summary counts before details load', () => {
    const items = collectAttention({sandboxes: [{summary: summary('a', {pending: 3, approvals: 2})}], projects: []}, NOW);
    expect(items.map(i => [i.kind, i.count])).toEqual([['more-requests', 3], ['preview', 2]]);
  });

  it('moves a grant ending soon to "Act now" and leaves expired windows out', () => {
    const soon = {summary: summary('soon', {egress: 'Open', egressExpires: s(SOON_MS - MIN)}), detail: detail('soon', {learning: {phase: 'Open', minutes: 240, started: s(-HOUR), expires: s(SOON_MS - MIN)}})};
    const later = {summary: summary('later', {egress: 'Learning', egressExpires: s(3 * HOUR)})};
    const gone = {summary: summary('gone', {egress: 'Open', egressExpires: s(-MIN)})};
    const items = collectAttention({sandboxes: [later, soon, gone], projects: []}, NOW);
    expect(items.map(i => [i.owner.id, i.tier])).toEqual([['soon', 'now'], ['later', 'open']]);
    expect(items[0].web).toEqual({phase: 'Open', minutes: 240});
    expect(items[1].web).toEqual({phase: 'Learning', minutes: 60});
  });

  it('treats a restricting LAN grant as open access without a deadline', () => {
    const items = collectAttention({sandboxes: [{summary: summary('a'), lan: lanWith(-MIN, 'Restricting')}], projects: []}, NOW);
    expect(items[0]).toMatchObject({kind: 'lan-grant', tier: 'open', deadline: undefined, title: 'LAN 192.168.1.50 restricting'});
  });

  it('includes recent failed and stuck runs and every pending gate', () => {
    const runs = [
      run('failed', {status: 'failed', failed: 1, failure: {task: 't', title: 'Export button', stage: 'checks', error: '3 failing tests'}, endedAt: isoAt(-20 * MIN)}),
      run('stuck', {status: 'running', running: 1, startedAt: isoAt(-5 * HOUR), lastActivity: isoAt(-5 * HOUR)}),
      run('busy', {status: 'running', running: 1, startedAt: isoAt(-30 * MIN), lastActivity: isoAt(-30 * MIN)}),
      run('gate', {status: 'waiting', pendingGates: 2, lastActivity: isoAt(-40 * MIN)}),
      run('old', {status: 'failed', failed: 1, collectedAt: isoAt(-5 * 24 * HOUR)}),
      run('legacy', {}),
    ];
    const items = collectAttention({sandboxes: [], projects: [], runs}, NOW);
    expect(items.map(i => [i.kind, i.owner.id])).toEqual([['gate', 'gate'], ['run-failed', 'failed'], ['run-stuck', 'stuck']]);
    expect(items.find(i => i.kind === 'run-failed')!.detail).toBe('Export button: checks: 3 failing tests');
    expect(items[0].count).toBe(2);
  });
});

describe('urgency order', () => {
  const item = (key: string, extra: Partial<AttentionItem>): AttentionItem => ({key, kind: 'request', tier: 'decide', owner: {kind: 'sandbox', id: 'a'}, title: key, detail: '', count: 1, ...extra});

  it('sorts by tier, then soonest deadline, then kind, then longest waiting', () => {
    const items = [
      item('open-late', {kind: 'web-window', tier: 'open', deadline: NOW + 3 * HOUR}),
      item('request-new', {since: NOW - MIN}),
      item('runs', {kind: 'run-failed', tier: 'runs', since: NOW - HOUR}),
      item('request-old', {since: NOW - HOUR}),
      item('lan-soon', {kind: 'lan-grant', tier: 'now', deadline: NOW + 5 * MIN}),
      item('error', {kind: 'sandbox-error', tier: 'now'}),
      item('web-sooner', {kind: 'web-window', tier: 'now', deadline: NOW + 2 * MIN}),
      item('report', {kind: 'report', tier: 'now', since: NOW - 2 * HOUR}),
      item('devlink', {kind: 'devlink', since: NOW - 3 * HOUR}),
      item('gate', {kind: 'gate'}),
      item('open-sooner', {kind: 'lan-grant', tier: 'open', deadline: NOW + HOUR}),
    ];
    expect([...items].sort(compareAttention).map(i => i.key)).toEqual([
      'web-sooner', 'lan-soon', 'error', 'report',
      'gate', 'request-old', 'request-new', 'devlink',
      'runs',
      'open-sooner', 'open-late',
    ]);
  });

  it('is stable for equal items', () => {
    const a = item('a', {}), b = item('b', {});
    expect([b, a].sort(compareAttention).map(i => i.key)).toEqual(['a', 'b']);
  });

  it('groups by tier in order and counts aggregates', () => {
    const items = [item('x', {tier: 'open', kind: 'web-window'}), item('y', {count: 5, kind: 'more-requests'}), item('z', {tier: 'now', kind: 'report'})].sort(compareAttention);
    expect(groupAttention(items).map(g => [g.tier, g.count])).toEqual([['now', 1], ['decide', 5], ['open', 1]]);
    expect(attentionCount(items)).toEqual({count: 6, urgent: 1});
  });

  it('filters by kind and reads the filter from the URL safely', () => {
    const items = [item('r', {}), item('l', {kind: 'lan-grant', tier: 'open'}), item('e', {kind: 'project-error', tier: 'now'}), item('g', {kind: 'gate'})];
    expect(filterAttention(items, 'grants').map(i => i.key)).toEqual(['l']);
    expect(filterAttention(items, 'errors').map(i => i.key)).toEqual(['e']);
    expect(filterAttention(items, 'runs').map(i => i.key)).toEqual(['g']);
    expect(filterAttention(items, 'approvals').map(i => i.key)).toEqual(['r', 'g']);
    expect(attentionFilter('grants')).toBe('grants');
    expect(attentionFilter('<script>')).toBe('all');
    expect(attentionFilter(null)).toBe('all');
  });
});

describe('agent text', () => {
  it('stays plain and clipped in items', () => {
    const long = 'x'.repeat(1000) + '\u0007';
    const items = collectAttention({sandboxes: [{summary: summary('a'), reports: [report(1, {resource: long})]}], projects: []}, NOW);
    expect(items[0].detail.length).toBeLessThanOrEqual(200);
    expect(items[0].detail).not.toContain('\u0007');
  });
});
