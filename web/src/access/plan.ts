// Changing a level: the exact rule changes it makes, shown before anything
// is applied, then applied in order. Narrowing steps (lock the window,
// remove rules, revoke approvals) always run before widening ones (add
// rules, open a window), so an interrupted change never leaves more access
// than the operator confirmed, and rules added by a failed change are removed
// again.
import type {HTTPSRule, Observation, Project, WebRule} from '../api';
import {addRule, decideRequest, removeRule, requestUrl, webLearn, webLock, webOpen, type RuleDraft} from '../workspace/sources/internet';
import {LEVEL_NAMES, ownRules, presetLabel, presetOf, profileFor, projectLevelOf, STANDARD_WEB_RULES, type AccessLevel, type LevelState, type PresetWebRule, type Profiles} from './levels';

// sandbox-control has no bulk revoke: each approval is one request and one
// policy revision. Above this many, Locked is offered as disabled instead.
export const MAX_REVOKES = 100;
export const OPEN_WINDOWS: [number, string][] = [[15, '15 minutes'], [60, '1 hour'], [240, '4 hours'], [1440, '24 hours']];

export type Step =
  | {op: 'lock'}
  | {op: 'open'; minutes: number; learn: boolean}
  | {op: 'remove-rule'; rule: WebRule}
  | {op: 'revoke'; request: Observation}
  | {op: 'add-rule'; preset: PresetWebRule};

export const widens = (s: Step) => s.op === 'open' || s.op === 'add-rule';

export interface Change { kind: 'add' | 'remove' | 'keep'; text: string }
export interface Plan {
  target: AccessLevel;
  available: boolean;
  reason?: string;
  // Sandbox steps, in the order they run; project plans have none.
  steps: Step[];
  changes: Change[];
  widens: boolean;
  // Project plans: the effective rules before and after, and the profile to set.
  before?: HTTPSRule[]; after?: HTTPSRule[]; profile?: string;
}

const unavailable = (target: AccessLevel, reason: string): Plan => ({target, available: false, reason, steps: [], changes: [], widens: false});

export function stepText(s: Step): string {
  switch (s.op) {
    case 'lock': return 'End the open web window now';
    case 'open': return `Open every public website for ${OPEN_WINDOWS.find(([m]) => m === s.minutes)?.[1] || s.minutes + ' minutes'}${s.learn ? ', recording each request as an approval' : ''}`;
    case 'remove-rule': return 'Remove web rule: ' + s.rule.description;
    case 'revoke': return `Revoke approval: ${s.request.method} ${requestUrl(s.request)}`;
    case 'add-rule': return 'Allow ' + presetLabel(s.preset);
  }
}

// Order: lock, remove rules, revoke approvals, then add rules, then open.
const ORDER: Step['op'][] = ['lock', 'remove-rule', 'revoke', 'add-rule', 'open'];
export const ordered = (steps: Step[]) => [...steps].sort((a, b) => ORDER.indexOf(a.op) - ORDER.indexOf(b.op));

export interface SandboxPlanInput {
  state: LevelState;
  // null: this sandbox-control release has no web rules.
  rules: WebRule[] | null;
  approvedTotal: number;
  // Approved requests loaded for revocation (Locked only); may be fewer than approvedTotal.
  approved?: Observation[];
  open?: {minutes: number; learn: boolean};
}

export function sandboxPlan(target: AccessLevel, input: SandboxPlanInput): Plan {
  const {state} = input;
  if (!state.supported) return unavailable(target, state.note || 'Levels are not available for this sandbox.');
  if (target === 'custom') return unavailable(target, 'Custom is what the fine-grained editors make. Edit them below; the level follows.');
  const windowOpen = state.level === 'open';
  const own = ownRules(input.rules || []);
  const steps: Step[] = [];
  if (target === 'open') {
    const o = input.open || {minutes: 60, learn: false};
    steps.push({op: 'open', minutes: o.minutes, learn: o.learn});
    const changes: Change[] = [{kind: 'add', text: stepText(steps[0])}, {kind: 'keep', text: `Rules and approvals stay as they are; when the window ends the sandbox returns to ${state.standing ? LEVEL_NAMES[state.standing] : 'its rules'}.`}];
    if (o.learn) changes.push({kind: 'add', text: 'Every request made during the window becomes a permanent approval.'});
    return {target, available: true, steps, changes, widens: true};
  }
  if (windowOpen) steps.push({op: 'lock'});
  if (target === 'standard') {
    if (input.rules === null) return unavailable(target, 'This sandbox-control release has no web rules, so the Standard registries cannot be added as one set. Approve requests one by one instead.');
    for (const r of own) if (!presetOf(r)) steps.push({op: 'remove-rule', rule: r});
    for (const p of STANDARD_WEB_RULES) if (!own.some(r => presetOf(r) === p)) steps.push({op: 'add-rule', preset: p});
  } else {
    for (const r of own) steps.push({op: 'remove-rule', rule: r});
  }
  if (target === 'ask' && input.approvedTotal === 0) return unavailable(target, 'No requests are approved yet, so Ask is the same as Locked. Approve requests in Awaiting approval, or choose Locked.');
  if (target === 'locked' && input.approvedTotal > 0) {
    if (input.approvedTotal > MAX_REVOKES) return unavailable(target, `${input.approvedTotal.toLocaleString()} approved requests would each need revoking one by one (the console does at most ${MAX_REVOKES}); sandbox-control has no bulk revoke yet. Choose Ask to keep them, or revoke them on the Approved tab.`);
    if (!input.approved || input.approved.length < input.approvedTotal) return unavailable(target, 'Loading the approved requests to revoke…');
    for (const o of input.approved) steps.push({op: 'revoke', request: o});
  }
  const all = ordered(steps);
  const changes: Change[] = all.map(s => ({kind: widens(s) ? 'add' : 'remove', text: stepText(s)}));
  const keptPreset = target === 'standard' ? own.filter(r => presetOf(r)).length : 0;
  if (keptPreset) changes.push({kind: 'keep', text: `${keptPreset} Standard rules already in place stay`});
  if (target !== 'locked' && input.approvedTotal) changes.push({kind: 'keep', text: `${input.approvedTotal.toLocaleString()} approved requests stay approved`});
  if (!all.length) return unavailable(target, `Already ${LEVEL_NAMES[target]}: nothing would change.`);
  return {target, available: true, steps: all, changes, widens: all.some(widens)};
}

// The RuleDraft sandbox-control's web-rule-add form takes for one preset rule.
// Standard rules are permanent: the level stays until it is changed.
export const presetDraft = (p: PresetWebRule): RuleDraft => ({kind: p.kind, host: p.host, path: p.kind === 'path' ? p.path : '', wildcard: '', apex: false, github: '', write: false,
  methods: p.methods, anyMethod: false, passthrough: false, expires: 'permanent', confirm: false});

// The sandbox-control POST each step sends.
export function stepForm(s: Step): {path: string; fields: Record<string, string | string[]>} {
  switch (s.op) {
    case 'lock': return webLock(60);
    case 'open': return s.learn ? webLearn(s.minutes) : webOpen(s.minutes);
    case 'remove-rule': return removeRule(s.rule);
    case 'revoke': return decideRequest(s.request, false);
    case 'add-rule': return addRule(presetDraft(s.preset));
  }
}

export interface RunResult { done: number; total: number; error?: string; rolledBack: number; rollbackError?: string }

// Runs the steps in order and stops at the first failure. Rules this run
// added are then removed again (rollback reads the rules back to find their
// IDs), so a failed Standard never stays half-applied.
export async function runPlan(steps: Step[], run: (s: Step) => Promise<void>, rollback: (added: PresetWebRule[]) => Promise<number>): Promise<RunResult> {
  const added: PresetWebRule[] = [];
  let done = 0;
  for (const s of steps) {
    try { await run(s); } catch (e) {
      const result: RunResult = {done, total: steps.length, error: (e as Error).message, rolledBack: 0};
      if (added.length) {
        try { result.rolledBack = await rollback(added); } catch (r) { result.rollbackError = (r as Error).message; }
      }
      return result;
    }
    if (s.op === 'add-rule') added.push(s.preset);
    done++;
  }
  return {done, total: steps.length, rolledBack: 0};
}

// Web rules a rollback removes: active rules added here that are one of the
// given presets.
export const rollbackTargets = (rules: WebRule[], added: PresetWebRule[]) => ownRules(rules).filter(r => { const p = presetOf(r); return p && added.includes(p); });

// --- Project sandboxes -------------------------------------------------------

const ruleText = (r: HTTPSRule) => `${r.methods.join(', ')} ${r.host}${r.paths.length === 1 && r.paths[0] === '/' ? ' (every path)' : ' ' + r.paths.join(' ')}`;
const key = (r: HTTPSRule) => `${r.host} ${[...r.methods].sort().join(',')} ${[...r.paths].sort().join(' ')}`;

export function ruleDiff(before: HTTPSRule[], after: HTTPSRule[]): Change[] {
  const b = new Set(before.map(key)), a = new Set(after.map(key));
  return [
    ...before.filter(r => !a.has(key(r))).map((r): Change => ({kind: 'remove', text: 'Stop allowing ' + ruleText(r)})),
    ...after.filter(r => !b.has(key(r))).map((r): Change => ({kind: 'add', text: 'Allow ' + ruleText(r)})),
    ...after.filter(r => b.has(key(r))).map((r): Change => ({kind: 'keep', text: 'Keep ' + ruleText(r)})),
  ];
}

export function projectPlan(target: AccessLevel, p: Project, profiles: Profiles, paused: boolean): Plan {
  if (!profiles) return unavailable(target, 'This controller has no access profiles yet. Update it and apply the AccessProfile CRD and profiles; until then edit the rules below.');
  const profile = profileFor(profiles, target);
  if (!profile) return unavailable(target, `No ${LEVEL_NAMES[target]} profile is installed on this cluster.`);
  if (!profile.projectSupported) return unavailable(target, (profile.reason || 'Project sandboxes cannot enforce this level yet') + '.');
  if (p.spec.desiredState === 'Removed') return unavailable(target, 'This sandbox was removed.');
  const own = p.spec.https || [];
  if (projectLevelOf(p, profiles) === target && p.spec.accessProfileName === profile.name) return unavailable(target, `Already ${LEVEL_NAMES[target]}: nothing would change.`);
  if (target !== 'custom' && own.length && !paused) return unavailable(target, `This sandbox has ${own.length} rules of its own, which are removed only while it is paused. Pause it and wait for Paused, or choose Custom to keep them.`);
  const currentRules = profiles.find(x => x.name === p.spec.accessProfileName)?.rules || [];
  const before = [...own, ...currentRules];
  const after = target === 'custom' ? [...own, ...profile.rules] : profile.rules;
  const changes = ruleDiff(before, after);
  if (target === 'custom') changes.push({kind: 'keep', text: 'LAN and package grants can be added one by one'});
  return {target, available: true, steps: [], changes, widens: changes.some(c => c.kind === 'add'), before, after, profile: profile.name};
}
