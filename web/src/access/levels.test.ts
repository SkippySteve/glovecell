import {describe, expect, it} from 'vitest';
import type {AccessProfileView, ExistingSandbox, HTTPSRule, Observation, Project, WebRule} from '../api';
import {accessLevel, presetOf, projectLevelOf, sandboxLevel, standingLevel, STANDARD_WEB_RULES, type LevelState} from './levels';
import {MAX_REVOKES, projectPlan, rollbackTargets, runPlan, sandboxPlan, stepForm, widens, type Step} from './plan';

const rule = (over: Partial<WebRule>): WebRule => ({
  id: Math.random().toString(36).slice(2), kind: 'domain', host: 'example.com', path: '', methods: ['GET', 'HEAD'], anyMethod: false, apex: false, owner: '', repo: '', write: false,
  passthrough: false, expires: '', active: true, description: 'rule', risks: [], rules: [], requests: 0, lastUsed: '', actor: 'operator', created: '', ...over,
});
const standard = (): WebRule[] => STANDARD_WEB_RULES.map(p => rule({kind: p.kind, host: p.host, path: p.path, methods: [...p.methods].reverse(), description: p.host}));
const obs = (i: number): Observation => ({host: 'registry.npmjs.org', method: 'GET', path: '/pkg/' + i, scheme: 'https', port: 443, observation: 'a-' + i});
const locked = (over: Partial<LevelState> = {}): LevelState => ({level: 'locked', standing: 'locked', expires: 0, learning: false, supported: true, label: 'Locked', ...over});

describe('sandbox level from rules', () => {
  it('reads Locked, Ask, Standard and Custom from the standing rules', () => {
    expect(standingLevel({rules: [], approved: 0})).toBe('locked');
    expect(standingLevel({rules: [], approved: 12})).toBe('ask');
    expect(standingLevel({rules: standard(), approved: 0})).toBe('standard');
    expect(standingLevel({rules: standard(), approved: 40})).toBe('standard');
    expect(standingLevel({rules: [...standard(), rule({kind: 'wildcard', host: '*.example.com'})], approved: 0})).toBe('custom');
    expect(standingLevel({rules: standard().slice(1), approved: 0})).toBe('custom');
    expect(standingLevel({rules: [rule({host: 'api.example.com'})], approved: 0})).toBe('custom');
  });

  it('ignores rules from Git and expired rules', () => {
    expect(standingLevel({rules: [rule({fromGit: true, source: 'git:ref#x'}), rule({active: false})], approved: 0})).toBe('locked');
  });

  it('never counts a broader variant of a preset rule as Standard', () => {
    const broad = standard().map((r, i) => i === 0 ? {...r, anyMethod: true} : r);
    expect(standingLevel({rules: broad, approved: 0})).toBe('custom');
    expect(presetOf(rule({host: 'registry.npmjs.org', passthrough: true}))).toBeUndefined();
    expect(presetOf(rule({host: 'registry.npmjs.org', methods: ['GET', 'HEAD', 'POST']}))).toBeUndefined();
  });

  it('is unknown until rules and approvals are loaded', () => {
    expect(standingLevel({rules: undefined, approved: 3})).toBeNull();
    expect(standingLevel({rules: [], approved: undefined})).toBeNull();
    // A release without web rules can still be Locked or Ask.
    expect(standingLevel({rules: null, approved: 0})).toBe('locked');
  });

  it('reports an open window as Open with its expiry and standing level', () => {
    const s = sandboxLevel({learningEgress: true, phase: 'Learning', expires: 1_000_000}, {rules: standard(), approved: 1});
    expect(s).toMatchObject({level: 'open', standing: 'standard', learning: true, expires: 1_000_000, supported: true});
    expect(sandboxLevel({learningEgress: true, phase: 'Open', expires: 5}, {rules: [], approved: 0})).toMatchObject({level: 'open', standing: 'locked', learning: false});
    expect(sandboxLevel({learningEgress: false, phase: undefined, expires: 0}, {rules: [], approved: 0})).toMatchObject({level: null, supported: false});
  });
});

const npm: HTTPSRule = {host: 'registry.npmjs.org', methods: ['GET', 'HEAD'], paths: ['/'], source: 'profile:standard'};
const profiles: AccessProfileView[] = [
  {name: 'locked', level: 'locked', rules: [], allowedGrants: [], projectSupported: true, default: true},
  {name: 'ask', level: 'ask', rules: [], allowedGrants: ['lan'], projectSupported: false, reason: 'project sandboxes do not record blocked requests yet'},
  {name: 'standard', level: 'standard', rules: [npm], allowedGrants: ['lan'], projectSupported: true},
  {name: 'open', level: 'open', rules: [], allowedGrants: ['internet'], projectSupported: false, reason: 'the project proxy cannot open time-limited internet windows yet'},
  {name: 'custom', level: 'custom', rules: [], allowedGrants: ['lan'], projectSupported: true},
  {name: 'team-docs', level: 'standard', rules: [npm], allowedGrants: [], projectSupported: true},
];
const project = (https: HTTPSRule[], profile?: string, desiredState: Project['spec']['desiredState'] = 'Paused'): Project => ({metadata: {name: 'p', resourceVersion: '1'}, spec: {repository: 'o/r', desiredState, https, accessProfileName: profile}});

describe('accessLevel', () => {
  const summary = (over: Partial<ExistingSandbox>): ExistingSandbox => ({id: 'a', namespace: 'n', sandbox: 's', mode: 'Running', revision: 1, status: 'Active', pending: 0, approvals: 0, egressControl: true, ...over});

  it('names a project level from its profile, and Custom from its own rules', () => {
    expect(accessLevel(project([], 'standard'), {profiles}).level).toBe('standard');
    expect(accessLevel(project([], 'team-docs'), {profiles}).level).toBe('standard');
    expect(accessLevel(project([], 'locked'), {profiles}).level).toBe('locked');
    expect(accessLevel(project([{host: 'api.example.com', methods: ['GET'], paths: ['/']}], 'standard'), {profiles}).level).toBe('custom');
    // Before access profiles: no profile and no rules is Locked, rules are Custom.
    expect(accessLevel(project([]), {profiles}).level).toBe('locked');
    expect(accessLevel(project([npm])).level).toBe('custom');
    // Built-in names work without the profile list; unknown ones say so.
    expect(accessLevel(project([], 'standard')).level).toBe('standard');
    expect(accessLevel(project([], 'missing'), {profiles})).toMatchObject({level: null});
  });

  it('knows an open window from the sandbox summary alone', () => {
    const now = Date.now() / 1000;
    expect(accessLevel(summary({egress: 'Open', egressExpires: now + 600}))).toMatchObject({level: 'open', learning: false});
    expect(accessLevel(summary({egress: 'Locked'}))).toMatchObject({level: null, label: 'Closed'});
    expect(accessLevel(summary({egress: 'Locked'}), {rules: {rules: [], approved: 4}}).level).toBe('ask');
    expect(accessLevel(summary({egressControl: false, learning: ''})).supported).toBe(false);
  });
});

describe('sandbox plans', () => {
  it('Standard adds only the missing preset rules and removes custom ones first', () => {
    const rules = [...standard().slice(2), rule({kind: 'wildcard', host: '*.example.com', description: 'every subdomain of example.com'})];
    const plan = sandboxPlan('standard', {state: locked({level: 'custom', standing: 'custom'}), rules, approvedTotal: 3});
    expect(plan.available).toBe(true);
    expect(plan.steps.map(s => s.op)).toEqual(['remove-rule', 'add-rule', 'add-rule']);
    expect(plan.widens).toBe(true);
    expect(plan.changes.some(c => c.kind === 'keep' && c.text.includes('3 approved'))).toBe(true);
  });

  it('narrows before it widens and locks an open window first', () => {
    const plan = sandboxPlan('standard', {state: locked({level: 'open', standing: 'custom', expires: 9}), rules: [rule({host: 'x.example.com'})], approvedTotal: 0});
    expect(plan.steps[0].op).toBe('lock');
    const firstWiden = plan.steps.findIndex(widens);
    expect(plan.steps.slice(firstWiden).every(widens)).toBe(true);
  });

  it('Locked revokes every approval and removes every rule added here, never rules from Git', () => {
    const rules = [rule({host: 'a.example.com'}), rule({fromGit: true, host: 'git.example.com'})];
    const approved = [obs(1), obs(2)];
    const plan = sandboxPlan('locked', {state: locked({level: 'ask', standing: 'ask'}), rules, approvedTotal: 2, approved});
    expect(plan.steps.map(s => s.op)).toEqual(['remove-rule', 'revoke', 'revoke']);
    expect(plan.widens).toBe(false);
    expect(plan.steps.some(s => s.op === 'remove-rule' && s.rule.fromGit)).toBe(false);
  });

  it('refuses Locked when the approvals are too many to revoke one by one', () => {
    const plan = sandboxPlan('locked', {state: locked({level: 'ask'}), rules: [], approvedTotal: MAX_REVOKES + 1});
    expect(plan.available).toBe(false);
    expect(plan.reason).toMatch(/one by one/);
    expect(sandboxPlan('locked', {state: locked({level: 'ask'}), rules: [], approvedTotal: 3, approved: [obs(1)]}).available).toBe(false);
  });

  it('Ask keeps approvals, removes web rules, and is refused when nothing is approved', () => {
    const plan = sandboxPlan('ask', {state: locked({level: 'standard'}), rules: standard(), approvedTotal: 5});
    expect(plan.steps.every(s => s.op === 'remove-rule')).toBe(true);
    expect(plan.steps).toHaveLength(STANDARD_WEB_RULES.length);
    expect(sandboxPlan('ask', {state: locked(), rules: [], approvedTotal: 0}).available).toBe(false);
  });

  it('Open is one time-boxed window and changes no rules', () => {
    const plan = sandboxPlan('open', {state: locked({level: 'ask', standing: 'ask'}), rules: [rule({})], approvedTotal: 2, open: {minutes: 240, learn: true}});
    expect(plan.steps).toEqual([{op: 'open', minutes: 240, learn: true}]);
    expect(stepForm(plan.steps[0]).fields).toMatchObject({action: 'learning-resume', minutes: '240'});
    expect(stepForm({op: 'open', minutes: 15, learn: false}).fields).toMatchObject({action: 'learning-open', minutes: '15'});
  });

  it('says when nothing would change, and refuses Custom and fixed-policy sandboxes', () => {
    expect(sandboxPlan('standard', {state: locked({level: 'standard'}), rules: standard(), approvedTotal: 0}).available).toBe(false);
    expect(sandboxPlan('custom', {state: locked(), rules: [], approvedTotal: 0}).available).toBe(false);
    expect(sandboxPlan('locked', {state: locked({supported: false, level: null, note: 'fixed'}), rules: [], approvedTotal: 0}).reason).toBe('fixed');
    expect(sandboxPlan('standard', {state: locked(), rules: null, approvedTotal: 0}).available).toBe(false);
  });

  it('adds Standard rules as permanent, method-limited web rules', () => {
    const f = stepForm({op: 'add-rule', preset: STANDARD_WEB_RULES.find(p => p.kind === 'path')!}).fields;
    expect(f).toMatchObject({action: 'web-rule-add', 'rule.kind': 'path', 'rule.host': 'github.com', 'rule.path': '/*/*/git-upload-pack', 'rule.method': ['POST'], 'rule.expires': 'permanent'});
    expect(f['rule.any_method']).toBeUndefined();
    expect(f['rule.passthrough']).toBeUndefined();
  });
});

describe('runPlan', () => {
  const steps: Step[] = [{op: 'lock'}, {op: 'add-rule', preset: STANDARD_WEB_RULES[0]}, {op: 'add-rule', preset: STANDARD_WEB_RULES[1]}];

  it('runs every step in order', async () => {
    const seen: string[] = [];
    const r = await runPlan(steps, async s => { seen.push(s.op); }, async () => 0);
    expect(r).toEqual({done: 3, total: 3, rolledBack: 0});
    expect(seen).toEqual(['lock', 'add-rule', 'add-rule']);
  });

  it('stops at the first failure and removes the rules this run added', async () => {
    let rolledBack: unknown[] = [];
    const r = await runPlan(steps, async s => { if (s.op === 'add-rule' && s.preset === STANDARD_WEB_RULES[1]) throw new Error('refused'); }, async added => { rolledBack = added; return added.length; });
    expect(r).toMatchObject({done: 2, error: 'refused', rolledBack: 1});
    expect(rolledBack).toEqual([STANDARD_WEB_RULES[0]]);
  });

  it('does not roll back when only narrowing steps ran', async () => {
    let called = false;
    const r = await runPlan([{op: 'lock'}, {op: 'revoke', request: obs(1)}], async s => { if (s.op === 'revoke') throw new Error('gone'); }, async () => { called = true; return 0; });
    expect(r).toMatchObject({done: 1, error: 'gone', rolledBack: 0});
    expect(called).toBe(false);
  });

  it('reports a failed rollback', async () => {
    const r = await runPlan(steps, async s => { if (s.op === 'add-rule' && s.preset === STANDARD_WEB_RULES[1]) throw new Error('refused'); }, async () => { throw new Error('unreachable'); });
    expect(r.rollbackError).toBe('unreachable');
  });

  it('rolls back only the preset rules it added, never others', () => {
    const rules = [...standard(), rule({host: 'own.example.com'}), rule({fromGit: true, host: STANDARD_WEB_RULES[0].host})];
    const targets = rollbackTargets(rules, [STANDARD_WEB_RULES[0]]);
    expect(targets).toHaveLength(1);
    expect(targets[0].host).toBe(STANDARD_WEB_RULES[0].host);
    expect(targets[0].fromGit).toBeFalsy();
  });
});

describe('project plans', () => {
  it('shows the rules a level adds and removes, and the profile it sets', () => {
    const own: HTTPSRule = {host: 'api.example.com', methods: ['GET'], paths: ['/v1']};
    const plan = projectPlan('standard', project([own], 'custom'), profiles, true);
    expect(plan).toMatchObject({available: true, widens: true, profile: 'standard'});
    expect(plan.changes.map(c => c.kind)).toEqual(['remove', 'add']);
    expect(projectPlan('locked', project([], 'standard', 'Running'), profiles, false)).toMatchObject({available: true, widens: false, profile: 'locked', after: []});
  });

  it('keeps own rules at Custom, which applies while running', () => {
    const own: HTTPSRule = {host: 'api.example.com', methods: ['GET'], paths: ['/v1']};
    const plan = projectPlan('custom', project([own], 'standard', 'Running'), profiles, false);
    expect(plan.available).toBe(true);
    expect(plan.after).toEqual([own]);
    expect(plan.changes.some(c => c.kind === 'remove' && c.text.includes('registry.npmjs.org'))).toBe(true);
  });

  it('refuses levels the project proxy cannot enforce, and removing own rules while running', () => {
    expect(projectPlan('ask', project([]), profiles, true).reason).toMatch(/record blocked requests/);
    expect(projectPlan('open', project([]), profiles, true).reason).toMatch(/internet windows/);
    expect(projectPlan('standard', project([{host: 'a.example.com', methods: ['GET'], paths: ['/']}], 'custom', 'Running'), profiles, false).reason).toMatch(/paused/);
    expect(projectPlan('standard', project([]), null, true).reason).toMatch(/no access profiles/);
    expect(projectPlan('locked', project([], 'locked'), profiles, true).reason).toMatch(/Already/);
    expect(projectPlan('standard', project([], undefined, 'Removed'), profiles, true).available).toBe(false);
  });
});
