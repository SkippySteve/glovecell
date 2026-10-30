// A workspace's access for lists and summaries: the access level from
// accessLevel() (levels.ts), plus the order Home sorts and filters by. Home's
// rows, the sandbox Overview and the command palette all name access through
// here, so every page says the same thing as the Internet page's level control.
import type {ExistingSandbox, Project, SandboxDetail} from '../api';
import {accessLevel, LEVEL_NAMES, sandboxLevel, type AccessLevel, type LevelState, type Profiles, type SandboxRules} from './levels';

// rank orders the widest first: 0 open, 1 open and learning, then custom,
// standard, ask, an unknown level (fixed policy, or rules not loaded yet), locked.
export interface Access { state: LevelState; label: string; rank: number; expires?: number }

const RANK: Record<Exclude<AccessLevel, 'open'>, number> = {custom: 2, standard: 3, ask: 4, locked: 6};
const UNKNOWN_RANK = 5;

export function summarize(state: LevelState): Access {
  const rank = state.level === 'open' ? (state.learning ? 1 : 0) : state.level ? RANK[state.level] : UNKNOWN_RANK;
  return {state, label: state.label, rank, expires: state.expires || undefined};
}

// A short word for the level, for search and the palette ("Open", "Standard", "Fixed policy").
export const levelWord = (state: LevelState) => state.level ? LEVEL_NAMES[state.level] : state.label;

// From the sandbox list (rules and approvals come from a separate read; without
// them a closed sandbox's level is unknown), a project (profiles name custom
// profile names), or the sandbox detail.
export const sandboxAccess = (s: ExistingSandbox, rules?: SandboxRules) => summarize(accessLevel(s, {rules}));
export const projectAccess = (p: Project, profiles?: Profiles) => summarize(accessLevel(p, {profiles}));
export const detailLevel = (d: SandboxDetail, rules: SandboxRules['rules']): LevelState => sandboxLevel(
  {learningEgress: d.learningEgress, phase: d.learning?.phase, expires: d.learning && d.learning.phase !== 'Locked' ? d.learning.expires * 1000 : 0},
  {rules, approved: d.egressCounts?.approved},
);
