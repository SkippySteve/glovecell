import {describe, expect, it} from 'vitest';
import {accessSummary, detailAccess, namedLevel} from './access';
import type {SandboxDetail} from './api';

const detail = (extra: Partial<SandboxDetail> = {}) => ({id: 'a', namespace: 'ns', sandbox: 'a', mode: 'Running', learningEgress: true, providerHosts: [], previewPending: 0, ...extra}) as SandboxDetail;

describe('accessSummary', () => {
  it('orders open, learning, a named level, locked and fixed rules', () => {
    expect(accessSummary({web: 'Open', expires: 10})).toMatchObject({label: 'Open to the web', tone: 'danger', variant: 'danger', rank: 0, expires: 10});
    expect(accessSummary({web: 'Learning'})).toMatchObject({label: 'Learning', tone: 'warn', variant: 'warning', rank: 1});
    expect(accessSummary({web: 'Locked', level: 'standard'})).toMatchObject({label: 'Standard', tone: 'info', rank: 2});
    expect(accessSummary({web: 'Locked'})).toMatchObject({label: 'Locked', tone: 'ok', rank: 3});
    expect(accessSummary({})).toMatchObject({label: 'Fixed policy rules', tone: 'neutral', rank: 2});
    expect(accessSummary({rules: 0})).toMatchObject({label: 'Repositories only', rank: 3});
    expect(accessSummary({rules: 2})).toMatchObject({label: '2 fixed rules', rank: 2});
  });
  it('ignores a level that is not a plain short name', () => {
    expect(namedLevel({accessLevel: '<b>x</b>'})).toBeUndefined();
    expect(namedLevel({status: {accessLevel: 'read-only'}})).toBe('read-only');
    expect(namedLevel(null)).toBeUndefined();
  });
});

describe('detailAccess', () => {
  it('uses the web window of the sandbox detail, as the sandbox list does', () => {
    expect(detailAccess(detail({learning: {phase: 'Open', minutes: 60, started: 1, expires: 99}}))).toMatchObject({label: 'Open to the web', expires: 99});
    expect(detailAccess(detail({learning: {phase: 'Locked', minutes: 60, started: 1, expires: 99}}))).toMatchObject({label: 'Locked'});
    // Without learning egress the window is not controllable: fixed policy rules.
    expect(detailAccess(detail({learningEgress: false, learning: {phase: 'Open', minutes: 60, started: 1, expires: 99}}))).toMatchObject({label: 'Fixed policy rules'});
  });
});
