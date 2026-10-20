// Display rules for Orca project records and agent reports, kept free of React so they are testable.
import type {OrcaProject} from './api';
import type {TagVariant} from './ui';

export const REPO = /^[A-Za-z0-9_][A-Za-z0-9_.-]*\/[A-Za-z0-9_][A-Za-z0-9_.-]*$/;

// Poll while a publish is in progress, but give up after a bounded time.
export const POLL_MS = 3000;
export const POLL_LIMIT = 100;
export const preparing = (list: OrcaProject[]) => list.some(p => p.status === 'preparing');

export function projectStatus(p: OrcaProject): {label: string; variant: TagVariant; busy?: boolean} {
  if (p.status === 'published') return {label: 'Published', variant: 'success'};
  if (p.status === 'failed') return {label: 'Failed' + (p.step ? ' at ' + p.step : ''), variant: 'danger'};
  return {label: 'Preparing' + (p.step ? ': ' + p.step : ''), variant: 'info', busy: true};
}

// Agent text is untrusted: render it as plain text only, without control
// characters, collapsed and clamped so one report cannot dominate the page.
export const REASON_LIMIT = 500;
export const RESOURCE_LIMIT = 200;
export function plainText(value: unknown, limit: number): string {
  const text = String(value ?? '').replace(/[\u0000-\u001f\u007f-\u009f​-‏‪-‮⁦-⁩]/g, ' ').replace(/\s+/g, ' ').trim();
  const chars = Array.from(text);
  return chars.length > limit ? chars.slice(0, limit - 1).join('') + '…' : text;
}

const KINDS: Record<string, string> = {web: 'Web request', package: 'Package', repository: 'Repository', cluster: 'Cluster access', credential: 'Credential', other: 'Other'};
export const reportKind = (kind: string) => KINDS[kind] || 'Other';
