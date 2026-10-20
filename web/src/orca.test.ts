import {describe, expect, it} from 'vitest';
import type {OrcaProject} from './api';
import {plainText, preparing, projectStatus, REPO, reportKind} from './orca';

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
