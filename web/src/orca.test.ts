import {describe, expect, it} from 'vitest';
import type {ExistingSandbox, OrcaProject} from './api';
import {existingKind, orcaRuntimeId, plainText, preparing, projectPreview, projectStatus, REPO, reportKind} from './orca';

const record = (status: OrcaProject['status'], step?: string): OrcaProject => ({name: 'infra', repo: 'steve/infra', actor: 'steve', requestedAt: '2026-10-20T00:00:00Z', status, step});

describe('Orca project records', () => {
  it('accepts only OWNER/NAME repositories', () => {
    expect(REPO.test('steve/agent-workplace')).toBe(true);
    for (const bad of ['steve', '../x/y', 'steve/infra/extra', 'https://git/steve/x', ' steve/x']) expect(REPO.test(bad)).toBe(false);
  });
  it('polls only while a record is preparing', () => {
    expect(preparing([record('published'), record('failed')])).toBe(false);
    expect(preparing([record('published'), record('preparing')])).toBe(true);
  });
  it('labels status with the failing step', () => {
    expect(projectStatus(record('failed', 'clone'))).toEqual({label: 'Failed at clone', variant: 'danger'});
    expect(projectStatus(record('preparing', 'register')).busy).toBe(true);
    expect(projectStatus(record('published')).variant).toBe('success');
    expect(projectStatus({name: 'factory', repo: 'steve/factory', status: 'registered', clone: '/workspace/factory'})).toEqual({label: 'In Orca', variant: 'neutral'});
  });
  it('does not poll for repos Orca already holds', () => {
    expect(preparing([record('registered')])).toBe(false);
  });
});

const sandbox = (id: string, namespace: string): ExistingSandbox => ({id, namespace, sandbox: 'main', mode: 'Running', revision: 1, status: 'Active', pending: 0, approvals: 0});

describe('Orca projects per sandbox', () => {
  const existing = [sandbox('herdr', 'agent-workspace-herdr'), sandbox('orca-pilot', 'agent-workspace-orca-pilot')];
  it('finds the runtime by its configured id', () => {
    expect(orcaRuntimeId({orcaProjects: true, orcaSandbox: 'orca-pilot'}, existing)).toBe('orca-pilot');
    expect(orcaRuntimeId({orcaProjects: true, orcaSandbox: 'missing'}, existing)).toBeUndefined();
    // A configured id wins over the namespace.
    expect(orcaRuntimeId({orcaProjects: true, orcaSandbox: 'missing', orcaNamespace: 'agent-workspace-orca-pilot'}, existing)).toBeUndefined();
  });
  it('falls back to the only sandbox in the intake namespace', () => {
    expect(orcaRuntimeId({orcaProjects: true, orcaNamespace: 'agent-workspace-orca-pilot'}, existing)).toBe('orca-pilot');
    expect(orcaRuntimeId({orcaProjects: true, orcaNamespace: 'shared'}, [sandbox('a', 'shared'), sandbox('b', 'shared')])).toBeUndefined();
    expect(orcaRuntimeId({orcaProjects: true}, existing)).toBeUndefined();
  });
  it('has no runtime without an intake', () => {
    expect(orcaRuntimeId({orcaSandbox: 'orca-pilot', orcaNamespace: 'agent-workspace-orca-pilot'}, existing)).toBeUndefined();
  });
  it('labels each sandbox kind', () => {
    expect(existingKind(existing[1], 'orca-pilot')).toBe('Orca runtime');
    expect(existingKind(existing[0], 'orca-pilot')).toBe('Coding sandbox');
    expect(existingKind(existing[1], undefined)).toBe('Coding sandbox');
  });
  it('previews the projects Orca holds, sorted, then a count', () => {
    const names = ['opnsense-kubernetes-bgp', 'factory', 'agent-project-controller', 'agent-workplace', 'herdr', 'library', 'voice', 'audio'];
    const records: OrcaProject[] = names.map(name => ({name, repo: '', status: 'registered'}));
    records.push({...record('failed'), name: 'broken'}, {...record('preparing'), name: 'new'}, {...record('published'), name: 'factory'});
    expect(projectPreview(records)).toEqual({names: ['agent-project-controller', 'agent-workplace', 'audio', 'factory', 'herdr', 'library'], more: 2, total: 8});
    expect(projectPreview([record('published')])).toEqual({names: ['infra'], more: 0, total: 1});
    expect(projectPreview([])).toEqual({names: [], more: 0, total: 0});
  });
});

describe('agent report text', () => {
  it('removes control and bidi characters and clamps length', () => {
    expect(plainText('a\u0000b\nc‮d', 100)).toBe('a b c d');
    expect(Array.from(plainText('x'.repeat(600), 500))).toHaveLength(500);
    expect(plainText('<a href="javascript:x">click</a>', 100)).toBe('<a href="javascript:x">click</a>');
    expect(plainText(undefined, 10)).toBe('');
  });
  it('maps unknown kinds to Other', () => {
    expect(reportKind('web')).toBe('Web request');
    expect(reportKind('<script>')).toBe('Other');
  });
});
