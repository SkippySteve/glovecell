// Client for the console's run-record API. Records are validated again here, so a
// record the schema rejects is never projected or rendered.
import {api} from '../api';
import {RunRecordSchema, type RunRecord} from './record';

// Status, counts, times and the first failure come from controllers that derive
// them from the record (internal/web/runs.go); older ones send only the first five.
export type RunStatus = 'failed' | 'waiting' | 'running' | 'succeeded' | 'pending';
export interface RunFailure { task: string; title: string; stage?: string; error?: string }
export interface RunSummary {
  id: string; objective: string; host: string; collectedAt: string; tasks: number;
  createdAt?: string; status?: RunStatus; failed?: number; succeeded?: number; running?: number; pendingGates?: number;
  startedAt?: string; endedAt?: string; lastActivity?: string; failure?: RunFailure;
}

export const listRuns = () => api<RunSummary[]>('runs');

export async function getRun(id: string): Promise<RunRecord> {
  const parsed = RunRecordSchema.safeParse(await api<unknown>('runs/' + encodeURIComponent(id)));
  if (!parsed.success) throw new Error(id + ': the run record failed validation and was not displayed');
  if (parsed.data.run.id !== id) throw new Error(id + ': the run record names a different run');
  return parsed.data;
}

const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";

// The Orca command that records a gate decision. Shown for the operator to run until
// the console can reach the Orca runtime itself.
export function gateCommand(record: RunRecord, gateId: string, option: string): string {
  const resolve = `orca orchestration gate-resolve --id ${quote(gateId)} --resolution ${quote(option)}`;
  const [namespace, pod] = record.run.host.split('/');
  return namespace && pod && /^[a-z0-9-]+$/.test(namespace + pod) ? `kubectl -n ${namespace} exec ${pod} -- ${resolve}` : resolve;
}
