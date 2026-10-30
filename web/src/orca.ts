// Display rules for Orca project records and agent reports, kept free of React so they are testable.
import type {ExistingSandbox, OrcaProject, Session} from './api';
import type {TagVariant} from './ui';

export const REPO = /^[A-Za-z0-9_][A-Za-z0-9_.-]*\/[A-Za-z0-9_][A-Za-z0-9_.-]*$/;

// Poll while a publish is in progress, but give up after a bounded time.
export const POLL_MS = 3000;
export const POLL_LIMIT = 100;
export const preparing = (list: OrcaProject[]) => list.some(p => p.status === 'preparing');

// How many records are in each state, for the list heading.
export function projectCounts(list: OrcaProject[]): {total: number; published: number; registered: number; preparing: number; failed: number} {
  const out = {total: list.length, published: 0, registered: 0, preparing: 0, failed: 0};
  for (const p of list) {
    if (p.status === 'published') out.published++;
    else if (p.status === 'registered') out.registered++;
    else if (p.status === 'failed') out.failed++;
    else out.preparing++;
  }
  return out;
}

export function projectStatus(p: OrcaProject): {label: string; variant: TagVariant; busy?: boolean} {
  if (p.status === 'published') return {label: 'Published', variant: 'success'};
  if (p.status === 'registered') return {label: 'In Orca', variant: 'neutral'};
  if (p.status === 'failed') return {label: 'Failed' + (p.step ? ' at ' + p.step : ''), variant: 'danger'};
  return {label: 'Preparing' + (p.step ? ': ' + p.step : ''), variant: 'info', busy: true};
}

// The existing sandbox that runs the Orca intake: the configured id, else the
// only sandbox in the intake service's namespace. Orca projects are
// repositories inside that one sandbox, so their names differ from sandbox names.
export function orcaRuntimeId(session: Session, existing: ExistingSandbox[]): string | undefined {
  if (!session.orcaProjects) return undefined;
  if (session.orcaSandbox) return existing.some(s => s.id === session.orcaSandbox) ? session.orcaSandbox : undefined;
  const matches = session.orcaNamespace ? existing.filter(s => s.namespace === session.orcaNamespace) : [];
  return matches.length === 1 ? matches[0].id : undefined;
}

export type SandboxKind = 'Orca runtime' | 'Coding sandbox' | 'Project sandbox';
export const existingKind = (s: ExistingSandbox, runtime: string | undefined): SandboxKind => s.id === runtime ? 'Orca runtime' : 'Coding sandbox';

// A compact list of the projects Orca holds: published or registered, by name,
// the first few and how many more.
export const PROJECT_PREVIEW = 6;
export function projectPreview(records: OrcaProject[], limit = PROJECT_PREVIEW): {names: string[]; more: number; total: number} {
  const names = [...new Set(records.filter(p => p.status === 'published' || p.status === 'registered').map(p => p.name))].sort((a, b) => a.localeCompare(b));
  return {names: names.slice(0, limit), more: Math.max(0, names.length - limit), total: names.length};
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
