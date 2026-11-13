import {describe, expect, it} from 'vitest';
import type {RunSummary} from './api';
import {sampleRun, trialRun} from './fixtures';
import {countRuns, failureText, filterRuns, formatDuration, recordFailures, recordSpan, runDuration, runFilter, runView, STUCK_MS} from './runs';

const NOW = Date.parse('2030-01-02T12:00:00Z');
const iso = (offset: number) => new Date(NOW + offset).toISOString();
const run = (id: string, extra: Partial<RunSummary> = {}): RunSummary => ({id, objective: 'objective ' + id, host: 'ns/pod', collectedAt: iso(-60_000), tasks: 1, ...extra});

describe('run list', () => {
  it('derives the view, with stuck runs and unknown status on older controllers', () => {
    expect(runView(run('a'), NOW)).toBe('unknown');
    expect(runView(run('a', {status: 'failed'}), NOW)).toBe('failed');
    expect(runView(run('a', {status: 'running', lastActivity: iso(-STUCK_MS - 1000)}), NOW)).toBe('stuck');
    expect(runView(run('a', {status: 'running', startedAt: iso(-STUCK_MS + 60_000)}), NOW)).toBe('running');
    expect(runView(run('a', {status: 'running'}), NOW)).toBe('running');
  });

  it('measures duration from the first start to the last end, or to now while running', () => {
    expect(runDuration(run('a', {status: 'failed', startedAt: iso(-3600_000), endedAt: iso(-600_000)}), NOW)).toBe(3000_000);
    expect(runDuration(run('a', {status: 'running', startedAt: iso(-90_000)}), NOW)).toBe(90_000);
    expect(runDuration(run('a', {status: 'failed'}), NOW)).toBeUndefined();
    expect(runDuration(run('a', {status: 'failed', startedAt: 'garbage'}), NOW)).toBeUndefined();
    expect([45_000, 12 * 60_000, 65 * 60_000, 51 * 3600_000].map(formatDuration)).toEqual(['45 s', '12 min', '1 h 05 min', '2 d 3 h']);
    expect(formatDuration(undefined)).toBe('');
  });

  it('filters by status and text and counts each status', () => {
    const runs = [run('one', {status: 'failed', failure: {task: 't', title: 'Build', stage: 'checks', error: 'lint failed'}}), run('two', {status: 'succeeded'}), run('three', {status: 'running', lastActivity: iso(-STUCK_MS * 2)})];
    expect(filterRuns(runs, 'failed', '', NOW).map(r => r.id)).toEqual(['one']);
    expect(filterRuns(runs, 'all', 'LINT', NOW).map(r => r.id)).toEqual(['one']);
    expect(filterRuns(runs, 'stuck', '', NOW).map(r => r.id)).toEqual(['three']);
    expect(countRuns(runs, NOW)).toMatchObject({all: 3, failed: 1, succeeded: 1, stuck: 1, running: 0});
    expect(failureText(runs[0])).toBe('checks: lint failed');
    expect(runFilter('stuck')).toBe('stuck');
    expect(runFilter('nope')).toBe('all');
  });

  it('surfaces each failure of a record with its stage and error', () => {
    expect(recordFailures(trialRun).map(f => [f.title, f.stage, f.error])).toEqual([
      ['slugify helper', 'agent_readiness', 'Agent startup blocked: agent-update-prompt'],
      ['top_words helper', 'dispatch_input', 'agent_not_in_foreground'],
    ]);
    // A failed check counts even while its task is still running.
    expect(recordFailures(sampleRun).map(f => [f.taskId, f.checks])).toEqual([['task_cli', ['unit tests']]]);
    expect(recordSpan(sampleRun)).toEqual({startedAt: '2026-10-18T09:01:00.000Z', endedAt: undefined, running: true});
    expect(recordSpan(trialRun)).toEqual({startedAt: undefined, endedAt: undefined, running: false});
  });
});
