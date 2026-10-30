// A workspace's access in one phrase, shared by Home's rows, the sandbox
// Overview and anything else that names "how far can this reach". One place, so
// every page uses the same words and the same order.
//
// TODO(control): when the access-control work lands, accessSummary() takes its
// accessLevel() as the source of the named level; callers stay unchanged.
import {egressExpires, egressState, type EgressState, type ExistingSandbox, type Project, type SandboxDetail} from './api';
import type {TagVariant} from './ui';

export type AccessTone = 'danger' | 'warn' | 'ok' | 'info' | 'neutral';

// rank orders the widest first: 0 open to the public web, 1 learning, 2 a named
// level or fixed rules, 3 locked. expires is unix seconds (as the API reports it).
export interface Access { label: string; tone: AccessTone; variant: TagVariant; rank: number; expires?: number; level?: string }

// The facts each source reports. rules is set only for project sandboxes, which
// have fixed outbound rules instead of a web window.
export interface AccessFacts { web?: EgressState; expires?: number; level?: string; rules?: number }

const VARIANT: Record<AccessTone, TagVariant> = {danger: 'danger', warn: 'warning', ok: 'success', info: 'info', neutral: 'neutral'};

// A named access level, when the API reports one. Older releases have none;
// then the web access state (Open, Learning, Locked) stands in. Anything that is
// not a plain short name is ignored rather than shown.
type Leveled = {accessLevel?: unknown; level?: unknown; status?: unknown};
export function namedLevel(item: unknown): string | undefined {
  const it = (item && typeof item === 'object' ? item : {}) as Leveled;
  const status = (it.status && typeof it.status === 'object' ? it.status : {}) as Leveled;
  const value = it.accessLevel ?? it.level ?? status.accessLevel ?? status.level;
  return typeof value === 'string' && /^[a-z][a-z0-9-]{0,39}$/i.test(value) ? value : undefined;
}
const levelLabel = (level: string) => level.charAt(0).toUpperCase() + level.slice(1).replace(/-/g, ' ');

const make = (label: string, tone: AccessTone, rank: number, extra: Partial<Access> = {}): Access => ({label, tone, variant: VARIANT[tone], rank, ...extra});

// An open or learning window wins over a named level: it is the widest access in force.
export function accessSummary(f: AccessFacts): Access {
  const {web, level} = f, expires = f.expires || undefined;
  if (web === 'Open') return make('Open to the web', 'danger', 0, {expires, level});
  if (web === 'Learning') return make('Learning', 'warn', 1, {expires, level});
  if (level) { const locked = /locked/i.test(level); return make(levelLabel(level), locked ? 'ok' : 'info', locked ? 3 : 2, {level}); }
  if (web === 'Locked') return make('Locked', 'ok', 3);
  if (f.rules !== undefined) return f.rules ? make(`${f.rules} fixed rule${f.rules === 1 ? '' : 's'}`, 'neutral', 2) : make('Repositories only', 'ok', 3);
  return make('Fixed policy rules', 'neutral', 2);
}

// From the sandbox list (polled), the sandbox detail, or a project.
export const sandboxFacts = (s: ExistingSandbox): AccessFacts => ({web: egressState(s), expires: egressExpires(s), level: namedLevel(s)});
export const detailFacts = (d: SandboxDetail): AccessFacts => {
  const l = d.learningEgress ? d.learning : undefined;
  return {web: l?.phase, expires: l && l.phase !== 'Locked' ? l.expires : undefined, level: namedLevel(d)};
};
export const projectFacts = (p: Project): AccessFacts => ({level: namedLevel(p), rules: (p.spec.https || []).length});

export const sandboxAccess = (s: ExistingSandbox) => accessSummary(sandboxFacts(s));
export const detailAccess = (d: SandboxDetail) => accessSummary(detailFacts(d));
export const projectAccess = (p: Project) => accessSummary(projectFacts(p));
