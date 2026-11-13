// Access levels: one named control per sandbox, from Locked to Open, plus
// Custom for anything the fine-grained editors make. A level is never stored
// on its own; it is read from the rules and the web window each backend
// actually enforces, and changing it is a list of ordinary rule changes the
// operator sees and confirms first (plan.ts).
//
// Two backends, two rule models:
// - sandbox-control sandboxes (learning egress): a web window (Locked,
//   Learning, Open, 15 minutes to 24 hours), exact approved requests and
//   web rules (path, domain, wildcard, GitHub project) with optional expiry.
// - project sandboxes (ProjectWorkspace): an AccessProfile named in
//   spec.accessProfileName (read-only rules, applied while running), the
//   sandbox's own exact HTTPS rules (changed only while paused) and
//   AccessGrants (LAN today). GET /api/v1/access-profiles lists the profiles.
import {egressControllable, egressExpires, egressState, type AccessProfileView, type EgressState, type ExistingSandbox, type HTTPSRule, type Project, type WebRule} from '../api';

export type AccessLevel = 'locked' | 'ask' | 'standard' | 'open' | 'custom';
export const LEVELS: AccessLevel[] = ['locked', 'ask', 'standard', 'open', 'custom'];
export type BackendKind = 'sandbox' | 'project';

export const LEVEL_NAMES: Record<AccessLevel, string> = {locked: 'Locked', ask: 'Ask', standard: 'Standard', open: 'Open', custom: 'Custom'};

export interface LevelText { summary: string; allows: string[]; blocks: string[] }

const REGISTRIES = 'Package downloads from npm, PyPI, Go modules and crates.io (read only)';

// What each level allows and blocks, in plain words, per backend.
export function levelText(level: AccessLevel, kind: BackendKind): LevelText {
  const base = kind === 'sandbox'
    ? ['Model provider hosts', 'Read-only reference sources from Git']
    : ['Its own repository (clone, push task branches, pull requests)', 'Read-only reference sources from Git'];
  switch (level) {
    case 'locked': return {
      summary: 'Nothing beyond the basics.',
      allows: base,
      blocks: kind === 'sandbox' ? ['Every other web request. Blocked requests are listed for you to review.'] : ['Every other web request.'],
    };
    case 'ask': return {
      summary: 'Only requests you approved, one by one.',
      allows: [...base, 'Each exact request you approved (method and URL)'],
      blocks: ['New requests, until you approve them in Awaiting approval.'],
    };
    case 'standard': return {
      summary: 'Common package registries and GitHub, read only.',
      allows: [...base, ...(kind === 'sandbox' ? ['Each exact request you approved'] : []), REGISTRIES,
        kind === 'sandbox' ? 'GitHub pages, raw files, archives, release assets and git clone of public repositories' : 'GitHub pages, raw files, archives and release assets (no git clone)'],
      blocks: ['Uploads, pushes and POST requests to these hosts', 'Every other host'],
    };
    case 'open': return {
      summary: 'Any public website, for a limited time.',
      allows: [...base, 'Every public website, any method, until the window ends'],
      blocks: ['Private networks, cluster services and cloud metadata, always', 'Everything again when the window ends'],
    };
    case 'custom': return {
      summary: 'Your own mix of rules.',
      allows: ['Whatever the fine-grained editors list'],
      blocks: ['Everything they do not list'],
    };
  }
}

// --- Standard preset ---------------------------------------------------------

// Hosts both backends allow at Standard, GET and HEAD on every path. The
// project controller serves its own copy (proxy.StandardRules); this list is
// the sandbox-control one, applied as web rules.
export const STANDARD_HOSTS = [
  'proxy.golang.org', 'sum.golang.org', 'registry.npmjs.org', 'pypi.org', 'files.pythonhosted.org', 'index.crates.io', 'static.crates.io',
  'github.com', 'codeload.github.com', 'raw.githubusercontent.com', 'objects.githubusercontent.com', 'release-assets.githubusercontent.com',
];

// One web rule of the sandbox-control Standard preset.
export interface PresetWebRule { kind: 'domain' | 'path'; host: string; path: string; methods: string[] }
export const STANDARD_WEB_RULES: PresetWebRule[] = [
  ...STANDARD_HOSTS.map((host): PresetWebRule => ({kind: 'domain', host, path: '', methods: ['GET', 'HEAD']})),
  // git clone and fetch over HTTPS (smart HTTP posts its wants to git-upload-pack; pushes use git-receive-pack, which stays blocked).
  {kind: 'path', host: 'github.com', path: '/*/*/git-upload-pack', methods: ['POST']},
];

const sameSet = (a: readonly string[], b: readonly string[]) => a.length === b.length && [...a].sort().join(',') === [...b].sort().join(',');

export const presetLabel = (p: PresetWebRule) => `${p.methods.join(', ')} ${p.host}${p.kind === 'path' ? p.path : ' (every path)'}`;

// The preset rule a web rule is, if any. Broader variants (any method,
// pass-through) never count as the preset.
export function presetOf(r: WebRule): PresetWebRule | undefined {
  if (r.anyMethod || r.passthrough || (r.kind !== 'domain' && r.kind !== 'path')) return undefined;
  return STANDARD_WEB_RULES.find(p => p.kind === r.kind && p.host === r.host && (p.kind === 'domain' || p.path === r.path) && sameSet(p.methods, r.methods));
}

// Web rules this console manages: added here (not synced from Git) and active.
export const ownRules = (rules: WebRule[]) => rules.filter(r => !r.fromGit && r.active);

// --- Current level -----------------------------------------------------------

export interface LevelState {
  // null: no level applies (fixed-policy sandbox) or the standing rules are not loaded.
  level: AccessLevel | null;
  // The level the sandbox returns to when an open window ends (null when unknown).
  standing: AccessLevel | null;
  // Open window end in epoch milliseconds, 0 when closed.
  expires: number;
  // An open window that records every request as an approval.
  learning: boolean;
  supported: boolean;
  label: string;
  note?: string;
}

export interface SandboxRules {
  // undefined: not loaded yet; null: this sandbox-control release has no web rules.
  rules: WebRule[] | null | undefined;
  approved: number | undefined;
}

// The standing level of a sandbox-control sandbox from its own rules and approvals.
export function standingLevel({rules, approved}: SandboxRules): AccessLevel | null {
  if (rules === undefined || approved === undefined) return null;
  const own = ownRules(rules || []);
  const preset = own.filter(r => presetOf(r));
  if (own.length > preset.length) return 'custom';
  if (preset.length) return STANDARD_WEB_RULES.every(p => own.some(r => presetOf(r) === p)) ? 'standard' : 'custom';
  return approved > 0 ? 'ask' : 'locked';
}

const clockAt = (ms: number) => new Date(ms).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});

export function sandboxLevel(window: {learningEgress: boolean; phase?: EgressState; expires: number}, rules: SandboxRules): LevelState {
  if (!window.learningEgress) return {level: null, standing: null, expires: 0, learning: false, supported: false, label: 'Fixed policy', note: 'This sandbox uses fixed policy rules from Git; levels apply to sandboxes with learning egress.'};
  const standing = standingLevel(rules);
  const open = window.phase === 'Open' || window.phase === 'Learning';
  if (open) {
    const learning = window.phase === 'Learning';
    return {level: 'open', standing, expires: window.expires, learning, supported: true, label: (learning ? 'Open, learning' : 'Open') + (window.expires ? ' until ' + clockAt(window.expires) : '')};
  }
  return {level: standing, standing, expires: 0, learning: false, supported: true, label: standing ? LEVEL_NAMES[standing] : 'Closed', note: standing ? undefined : 'No open window. Open the sandbox to see its level.'};
}

const ruleKey = (r: HTTPSRule) => `${r.host} ${[...r.methods].sort().join(',')} ${[...r.paths].sort().join(' ')}`;
export const sameRules = (a: HTTPSRule[], b: HTTPSRule[]) => sameSet(a.map(ruleKey), b.map(ruleKey));

// The profiles the controller serves; null on controllers without access profiles.
export type Profiles = AccessProfileView[] | null | undefined;
export const profileFor = (profiles: Profiles, level: AccessLevel) =>
  profiles?.find(p => p.name === level && p.level === level) || profiles?.find(p => p.level === level);

// A project's level: its own HTTPS rules make it Custom; otherwise its
// profile's level. A project without a profile and without rules is Locked
// (nothing beyond the basics); with an unknown profile the level is unknown.
export function projectLevelOf(p: Project, profiles: Profiles): AccessLevel | null {
  const own = p.spec.https || [];
  if (own.length) return 'custom';
  const name = p.spec.accessProfileName;
  if (!name) return 'locked';
  const profile = profiles?.find(x => x.name === name);
  return profile ? profile.level : null;
}

export function projectLevel(p: Project, profiles: Profiles): LevelState {
  const level = projectLevelOf(p, profiles);
  return {level, standing: level, expires: 0, learning: false, supported: true, label: level ? LEVEL_NAMES[level] : 'Unknown profile ' + p.spec.accessProfileName,
    note: level ? undefined : `Profile ${p.spec.accessProfileName} is not installed; the controller blocks this sandbox until it is.`};
}

const isProject = (x: Project | ExistingSandbox): x is Project => 'metadata' in x && 'spec' in x;

// The current access level of a sandbox, for lists and dashboards.
// A project sandbox needs the profile list (GET /api/v1/access-profiles) to
// name custom profile names; the built-in names work without it. A
// sandbox-control summary knows only its web window: an open window is exact
// ("Open until 9:25"); otherwise pass its rules and approval count, or the
// level is null ("Closed").
export function accessLevel(target: Project | ExistingSandbox, context: {profiles?: Profiles; rules?: SandboxRules} = {}): LevelState {
  if (isProject(target)) {
    const builtIn = (LEVELS as string[]).includes(target.spec.accessProfileName || '') ? [{name: target.spec.accessProfileName!, level: target.spec.accessProfileName as AccessLevel, rules: [], allowedGrants: [], projectSupported: true}] : [];
    return projectLevel(target, context.profiles || builtIn);
  }
  const phase = egressState(target);
  return sandboxLevel({learningEgress: egressControllable(target), phase, expires: egressExpires(target) * 1000}, context.rules || {rules: undefined, approved: undefined});
}
