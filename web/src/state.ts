// Display state derived from controller conditions. Mirrors the controller's Ready condition;
// blocked preparation is surfaced, not hidden behind a phase.
import type {ExistingSandbox, Project} from './api';
import type {TagVariant} from './ui';

export interface Status { label: string; variant: TagVariant; busy?: boolean; attention?: string; message?: string; removed?: boolean }

export const settled = (p: Project) => p.status?.observedGeneration === p.metadata.generation;
export const pausedForEdits = (p: Project) => p.spec.desiredState === 'Paused' && p.status?.phase === 'Paused' && settled(p);

export function projectState(p: Project): Status {
  const condition = p.status?.conditions?.find(c => c.type === 'Ready'), phase = p.status?.phase || 'Requested';
  if (phase === 'Removed') return {label: 'Removed', variant: 'neutral', removed: true};
  if (condition && condition.observedGeneration !== undefined && condition.observedGeneration !== p.metadata.generation) return {label: 'Updating', variant: 'info', busy: true, message: 'Update requested. Waiting for the controller to apply it.'};
  if (!condition) return {label: phase, variant: 'info', busy: true, message: 'Request saved. Waiting for preparation to begin.'};
  const blocked = condition.status !== 'True' && ['PreparationPending', 'PolicyRejected', 'CleanupPending', 'T3ConnectPending'].includes(condition.reason) && !condition.message.startsWith('Waiting for');
  if (blocked) return {label: 'Needs attention', variant: 'warning', attention: phase === 'Removing' ? 'Removal needs attention' : 'Preparation needs attention', message: condition.message};
  if (phase === 'Ready') return {label: 'Ready', variant: 'success'};
  if (phase === 'Paused') return {label: 'Paused', variant: 'neutral'};
  return {label: phase, variant: 'info', busy: true, message: condition.message};
}

export function existingState(s: ExistingSandbox): Status {
  if (/fail|error|reject/i.test(s.status || '')) return {label: s.status, variant: 'danger'};
  if (s.status && s.status !== 'Active' && s.status !== 'Unavailable') return {label: s.status, variant: 'info', busy: true};
  if (s.mode === 'Running') return {label: 'Running', variant: 'success'};
  if (s.mode === 'Suspended') return {label: 'Stopped', variant: 'neutral'};
  return {label: s.mode || s.status || 'Unknown', variant: 'neutral'};
}

export const clock = (unix: number) => new Date(unix * 1000).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});

export function slugFromRepository(repository: string) {
  const leaf = (repository || '').split('/').pop() || '';
  return leaf.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/-$/, '');
}
