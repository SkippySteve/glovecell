import {describe, expect, it} from 'vitest';
import type {ExistingSandbox, Project} from './api';
import {ago, filterRows, projectAccess, projectRow, sandboxAccess, sandboxRow, sortRows, toShow, toSort, totals, urgency} from './home';

const sandbox = (id: string, over: Partial<ExistingSandbox> = {}): ExistingSandbox => ({id, namespace: 'ns-' + id, sandbox: 'workspace', mode: 'Running', revision: 1, status: 'Active', pending: 0, approvals: 0, egress: 'Locked', egressControl: true, ...over});
const project = (name: string, over: Partial<Project['spec']> = {}, phase = 'Ready'): Project => ({
  metadata: {name, generation: 1}, spec: {repository: 'example/' + name, desiredState: 'Running', ...over},
  status: {phase, observedGeneration: 1, conditions: [{type: 'Ready', status: phase === 'Ready' ? 'True' : 'False', reason: phase, message: phase, observedGeneration: 1}]},
});

describe('access', () => {
  it('falls back to the web access state', () => {
    expect(sandboxAccess(sandbox('a', {egress: 'Open', egressExpires: 100}))).toMatchObject({label: 'Open to the web', variant: 'danger', rank: 0, expires: 100});
    expect(sandboxAccess(sandbox('a', {egress: 'Learning'}))).toMatchObject({label: 'Learning', rank: 1});
    expect(sandboxAccess(sandbox('a'))).toMatchObject({label: 'Locked', variant: 'success', rank: 3});
    expect(sandboxAccess(sandbox('a', {egress: '', learning: 'Learning'}))).toMatchObject({label: 'Learning'});
    expect(sandboxAccess(sandbox('a', {egress: '', egressControl: false}))).toMatchObject({label: 'Policy rules', rank: 2});
  });

  it('shows a named access level when the API reports one', () => {
    const leveled = {...sandbox('a'), accessLevel: 'standard'} as ExistingSandbox;
    expect(sandboxAccess(leveled)).toMatchObject({label: 'Standard', level: 'standard', rank: 2});
    // An open window still wins: it is the widest access in force.
    expect(sandboxAccess({...leveled, egress: 'Open'} as ExistingSandbox)).toMatchObject({label: 'Open to the web', level: 'standard'});
    const p = {...project('p'), status: {phase: 'Ready', level: 'locked'}} as unknown as Project;
    expect(projectAccess(p)).toMatchObject({label: 'Locked', variant: 'success'});
  });

  it('ignores a level that is not a plain name', () => {
    expect(sandboxAccess({...sandbox('a'), accessLevel: '<b>x</b>'} as ExistingSandbox).label).toBe('Locked');
    expect(sandboxAccess({...sandbox('a'), accessLevel: 7} as unknown as ExistingSandbox).label).toBe('Locked');
  });

  it('describes project rules', () => {
    expect(projectAccess(project('p'))).toMatchObject({label: 'Repositories only', rank: 3});
    expect(projectAccess(project('p', {https: [{host: 'a', methods: ['GET'], paths: ['/']}]}))).toMatchObject({label: '1 fixed rule'});
  });
});

describe('rows', () => {
  const rows = [
    sandboxRow(sandbox('zeta', {egress: 'Open'}), 'Coding sandbox'),
    sandboxRow(sandbox('alpha', {pending: 2, approvals: 1}), 'Coding sandbox', 1),
    sandboxRow(sandbox('idle', {mode: 'Suspended'}), 'Coding sandbox'),
    projectRow(project('docs', {}, 'Paused')),
    projectRow(project('beta', {lanHosts: [{address: '192.0.2.10', ports: [22]}]})),
  ].map((r, i) => ({...r, lastActivity: [5, 50, 0, 20, 10][i] || undefined}));

  it('counts waiting requests and open LAN grants', () => {
    expect(rows[1]).toMatchObject({waiting: 3, lan: 1, href: '#/sandboxes/alpha'});
    expect(rows[4]).toMatchObject({kind: 'project', lan: 1, href: '#/projects/beta'});
  });

  it('sorts the most urgent first by default', () => {
    expect(sortRows(rows, 'urgency').map(r => r.name)).toEqual(['zeta', 'alpha', 'beta', 'docs', 'idle']);
    expect(urgency(rows[0])).toBe(0);
  });

  it('sorts by name, status, access and recent activity', () => {
    expect(sortRows(rows, 'name').map(r => r.name)).toEqual(['alpha', 'beta', 'docs', 'idle', 'zeta']);
    expect(sortRows(rows, 'status').map(r => r.running)).toEqual([true, true, true, false, false]);
    expect(sortRows(rows, 'access')[0].name).toBe('zeta');
    expect(sortRows(rows, 'activity').map(r => r.name)).toEqual(['alpha', 'docs', 'beta', 'zeta', 'idle']);
  });

  it('filters by text, state and kind', () => {
    expect(filterRows(rows, {q: 'ALP'}).map(r => r.name)).toEqual(['alpha']);
    expect(filterRows(rows, {q: 'example/'}).map(r => r.name)).toEqual(['docs', 'beta']);
    expect(filterRows(rows, {q: 'ns zeta'}).map(r => r.name)).toEqual(['zeta']);
    expect(filterRows(rows, {show: 'open'}).map(r => r.name)).toEqual(['zeta']);
    expect(filterRows(rows, {show: 'waiting'}).map(r => r.name)).toEqual(['alpha']);
    expect(filterRows(rows, {show: 'stopped'}).map(r => r.name)).toEqual(['idle', 'docs']);
    expect(filterRows(rows, {kind: 'project'}).map(r => r.name)).toEqual(['docs', 'beta']);
    expect(filterRows(rows, {kind: 'sandbox', show: 'running'}).map(r => r.name)).toEqual(['zeta', 'alpha']);
  });

  it('totals what the summary shows', () => {
    expect(totals(rows)).toEqual({total: 5, running: 3, open: 1, waiting: 3, attention: 0});
  });

  it('reads unknown query values as the defaults', () => {
    expect(toShow('bogus')).toBe('all');
    expect(toSort(null)).toBe('urgency');
    expect(toSort('activity')).toBe('activity');
  });
});

describe('ago', () => {
  const now = Date.UTC(2026, 9, 29, 12);
  it('reads as a short relative time', () => {
    expect(ago(now - 10_000, now)).toBe('just now');
    expect(ago(now - 5 * 60_000, now)).toBe('5 min ago');
    expect(ago(now - 3 * 3600_000, now)).toBe('3 h ago');
    expect(ago(now - 86400_000, now)).toBe('1 day ago');
    expect(ago(now - 3 * 86400_000, now)).toBe('3 days ago');
    expect(ago(now + 60_000, now)).toBe('just now');
  });
});
