// Run list rules, kept free of React so they are testable: status and duration
// from a run summary, "stuck" detection, and the list filters.
import type {RunStatus, RunSummary} from './api';
import type {RunRecord, Task} from './record';
import type {TagVariant} from './ui';

type Tone = 'success' | 'warning' | 'danger' | 'info' | 'neutral';

// One task's state from its last attempt (shared with the projected report).
export function taskState(task: Task): {label: string; tone: Tone} {
  const last = task.attempts.at(-1);
  if (last?.outcome === 'succeeded') return {label: 'Succeeded', tone: 'success'};
  if (last?.outcome === 'failed' || last?.state === 'failed' || task.status === 'failed') return {label: 'Failed', tone: 'danger'};
  if (last && !last.endedAt) return {label: 'Running', tone: 'info'};
  if (task.deps.length && task.status !== 'ready') return {label: 'Waiting on dependencies', tone: 'neutral'};
  return {label: 'Not started', tone: 'neutral'};
}

// A running run with no attempt starting or ending for this long is shown as stuck.
export const STUCK_MS = 2 * 3600_000;

export type RunView = RunStatus | 'stuck' | 'unknown';
export const RUN_FILTERS: [RunView | 'all', string][] = [['all', 'All'], ['failed', 'Failed'], ['waiting', 'Waiting on you'], ['stuck', 'Stuck'], ['running', 'Running'], ['succeeded', 'Succeeded']];
const LABELS: Record<RunView, [string, TagVariant]> = {
  failed: ['Failed', 'danger'], waiting: ['Waiting on you', 'warning'], stuck: ['Stuck', 'warning'], running: ['Running', 'info'],
  succeeded: ['Succeeded', 'success'], pending: ['Not started', 'neutral'], unknown: ['Status unknown', 'neutral'],
};
export const runLabel = (v: RunView) => LABELS[v];

export const ms = (iso?: string) => { const t = iso ? Date.parse(iso) : NaN; return Number.isFinite(t) ? t : 0; };

// The run's state as the list shows it. A controller without derived fields
// reports no status, so the run is "unknown" rather than guessed.
export function runView(r: RunSummary, now: number): RunView {
  if (!r.status) return 'unknown';
  if (r.status === 'running') {
    const last = ms(r.lastActivity) || ms(r.startedAt);
    if (last && now - last > STUCK_MS) return 'stuck';
  }
  return r.status;
}

// Milliseconds from the first attempt start to the last end; a run still going
// counts to `now`. Undefined when the record has no attempt times.
export function runDuration(r: RunSummary, now: number): number | undefined {
  const start = ms(r.startedAt);
  if (!start) return undefined;
  const end = ms(r.endedAt) || (r.status === 'running' || r.status === 'waiting' ? now : ms(r.lastActivity));
  return end >= start ? end - start : undefined;
}

// "45 s", "12 min", "1 h 05 min", "2 d 3 h".
export function formatDuration(d: number | undefined): string {
  if (d === undefined) return '';
  const s = Math.round(d / 1000);
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ${String(Math.floor(s % 3600 / 60)).padStart(2, '0')} min`;
  return `${Math.floor(s / 86400)} d ${Math.floor(s % 86400 / 3600)} h`;
}

export const failureText = (r: RunSummary) => r.failure ? [r.failure.stage ? `${r.failure.stage}: ` : '', r.failure.error || 'no error recorded'].join('') : '';

export const runFilter = (value: string | null): RunView | 'all' => RUN_FILTERS.some(([k]) => k === value) ? value as RunView : 'all';

export function filterRuns(runs: RunSummary[], view: RunView | 'all', query: string, now: number): RunSummary[] {
  const q = query.trim().toLowerCase();
  return runs.filter(r => (view === 'all' || runView(r, now) === view)
    && (!q || [r.id, r.objective, r.host, r.failure?.title, r.failure?.error].some(v => v?.toLowerCase().includes(q))));
}

export function countRuns(runs: RunSummary[], now: number): Record<RunView | 'all', number> {
  const out = {all: runs.length, failed: 0, waiting: 0, stuck: 0, running: 0, succeeded: 0, pending: 0, unknown: 0};
  for (const r of runs) out[runView(r, now)]++;
  return out;
}

// A run's host is "<namespace>/<pod>", as the collector records it.
export const runsFor = (runs: RunSummary[], namespace: string) => runs.filter(run => run.host.split('/')[0] === namespace);

// What a run report shows above the report itself: each failed task with the
// stage and error its last attempt recorded, failed checks, and the run's span.
export interface RunFailureLine { taskId: string; title: string; stage?: string; error?: string; checks: string[] }
export function recordFailures(record: RunRecord): RunFailureLine[] {
  const out: RunFailureLine[] = [];
  for (const task of record.tasks) {
    const last = task.attempts.at(-1);
    const failedChecks = task.checks.filter(c => c.status === 'failed').map(c => c.name);
    if (taskState(task).tone === 'danger' || failedChecks.length) out.push({taskId: task.id, title: task.title, stage: last?.failedStage, error: last?.lastError, checks: failedChecks});
  }
  return out;
}
export function recordSpan(record: RunRecord): {startedAt?: string; endedAt?: string; running: boolean} {
  const attempts = record.tasks.flatMap(t => t.attempts);
  const starts = attempts.map(a => ms(a.startedAt)).filter(Boolean), ends = attempts.map(a => ms(a.endedAt)).filter(Boolean);
  const running = record.tasks.some(t => taskState(t).label === 'Running');
  const iso = (t: number) => new Date(t).toISOString();
  return {startedAt: starts.length ? iso(Math.min(...starts)) : undefined, endedAt: ends.length && !running ? iso(Math.max(...ends)) : undefined, running};
}
