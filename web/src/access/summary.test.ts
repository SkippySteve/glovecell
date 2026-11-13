import {describe, expect, it} from 'vitest';
import type {SandboxDetail} from '../api';
import {detailLevel, levelWord, summarize} from './summary';
import type {LevelState} from './levels';

const state = (over: Partial<LevelState>): LevelState => ({level: null, standing: null, expires: 0, learning: false, supported: true, label: '', ...over});
const detail = (extra: Partial<SandboxDetail> = {}) => ({id: 'a', namespace: 'ns', sandbox: 'a', mode: 'Running', learningEgress: true, providerHosts: [], previewPending: 0, ...extra}) as SandboxDetail;

describe('summarize', () => {
  it('orders the widest access first', () => {
    const ranks = [state({level: 'open'}), state({level: 'open', learning: true}), state({level: 'custom'}), state({level: 'standard'}), state({level: 'ask'}), state({}), state({level: 'locked'})].map(s => summarize(s).rank);
    expect(ranks).toEqual([...ranks].sort((a, b) => a - b));
    expect(new Set(ranks).size).toBe(ranks.length);
  });
  it('words a level for search', () => {
    expect(levelWord(state({level: 'standard', label: 'Standard'}))).toBe('Standard');
    expect(levelWord(state({supported: false, label: 'Fixed policy'}))).toBe('Fixed policy');
  });
});

describe('detailLevel', () => {
  it('reads the window and rules of the sandbox detail, as the Internet page does', () => {
    expect(detailLevel(detail({learning: {phase: 'Open', minutes: 60, started: 1, expires: 99}}), [])).toMatchObject({level: 'open', expires: 99000});
    expect(detailLevel(detail({learning: {phase: 'Locked', minutes: 60, started: 1, expires: 99}, egressCounts: {pending: 0, approved: 0}}), [])).toMatchObject({level: 'locked', expires: 0});
    expect(detailLevel(detail({learning: {phase: 'Locked', minutes: 60, started: 1, expires: 99}, egressCounts: {pending: 0, approved: 2}}), null)).toMatchObject({level: 'ask'});
    expect(detailLevel(detail({learningEgress: false}), undefined)).toMatchObject({supported: false});
  });
});
