// Access-level fixtures for scripts/preview.mjs: sandbox-control web rules,
// one sandbox per level, AccessProfiles, project sandboxes at each project
// level, and their grants. Everything is invented.
const MIN = 60, HOUR = 3600, DAY = 86400;
const unix = () => Math.floor(Date.now() / 1000);
const iso = t => new Date(t * 1000).toISOString().replace(/\.\d+Z$/, 'Z');

let ruleSeq = 1;
// One sandbox-control web rule, shaped like GET /api/egress `rules`.
export function webRule(f, actor = 'operator') {
  const kind = f.kind, host = f.host || (f.wildcard || '').replace(/^\*\./, ''), methods = f.anyMethod ? [] : f.methods || ['GET', 'HEAD'];
  const what = kind === 'domain' ? `every path on ${host}` : kind === 'path' ? `${host}${f.path}` : kind === 'wildcard' ? `every subdomain of ${host}` : kind === 'github-repo' ? `GitHub project ${f.github}` : `every GitHub repository of ${f.github}`;
  const expires = f.expires === 'permanent' || !f.expires ? '' : iso(unix() + {'1d': DAY, '7d': 7 * DAY, '30d': 30 * DAY}[f.expires]);
  const risks = [];
  if (kind === 'wildcard') risks.push(`Every current and future subdomain of ${host} is allowed.`);
  if (f.passthrough) risks.push('With inspection off, History shows only the domain and connection.');
  if (methods.some(m => ['POST', 'PUT', 'PATCH', 'DELETE'].includes(m)) && !(kind === 'path' && f.path?.endsWith('git-upload-pack'))) risks.push('POST, PUT, PATCH or DELETE let the agent send data there.');
  return {id: 'r' + ruleSeq++, kind, host, path: f.path || '', methods, anyMethod: Boolean(f.anyMethod), apex: false, owner: (f.github || '').split('/')[0], repo: (f.github || '').split('/')[1] || '', write: Boolean(f.write),
    passthrough: Boolean(f.passthrough), expires, active: true, description: `${f.anyMethod ? 'Any method' : methods.join(', ')} on ${what}`, risks,
    rules: [{host, path: f.path || '/', methods, purpose: kind}], requests: f.requests || 0, lastUsed: f.requests ? iso(unix() - 2 * HOUR) : '', actor, created: iso(unix() - 3 * DAY), fromGit: Boolean(f.fromGit), source: f.fromGit ? 'git:reference-sources.yaml#' + f.fromGit : undefined};
}

const STANDARD = [
  ...['proxy.golang.org', 'sum.golang.org', 'registry.npmjs.org', 'pypi.org', 'files.pythonhosted.org', 'index.crates.io', 'static.crates.io', 'github.com', 'codeload.github.com', 'raw.githubusercontent.com', 'objects.githubusercontent.com', 'release-assets.githubusercontent.com']
    .map(host => ({kind: 'domain', host, methods: ['GET', 'HEAD'], expires: 'permanent'})),
  {kind: 'path', host: 'github.com', path: '/*/*/git-upload-pack', methods: ['POST'], expires: 'permanent'},
];
const gitRef = () => webRule({kind: 'github-repo', github: 'cilium/cilium', fromGit: 'cilium', requests: 4});

// Extra sandbox-control workspaces, one per level, plus web rules on the existing ones.
export function levelWorkspaces(make, observations, t0) {
  const workspace = (id, extra) => make(id, {lan: {hosts: [], history: []}, ...extra});
  return {
    scratch: workspace('scratch', {rules: [gitRef()]}),
    builder: workspace('builder', {rules: [...STANDARD.map((r, i) => webRule({...r, requests: i * 7})), gitRef()], approved: observations('s', 18, 9, true), pending: observations('t', 2, 11, false)}),
    'docs-bot': workspace('docs-bot', {rules: [webRule({kind: 'wildcard', wildcard: '*.readthedocs.io', methods: ['GET', 'HEAD'], expires: '7d', requests: 31}), webRule({kind: 'path', host: 'api.example.com', path: '/v1/*/models', methods: ['GET'], expires: '1d', requests: 3}), webRule({kind: 'github-repo', github: 'example/docs', expires: '30d'})], approved: observations('d', 7, 13, true)}),
    spike: workspace('spike', {learning: {phase: 'Open', minutes: 240, started: t0 - 3 * HOUR, expires: t0 + HOUR - 5 * MIN}, approved: observations('k', 4, 17, true)}),
  };
}

// POST /egress web-rule-add and web-rule-remove; true when handled.
export function ruleAction(w, action, f) {
  if (action === 'web-rule-add') {
    w.rules = w.rules || [];
    const r = webRule({kind: f.get('rule.kind'), host: f.get('rule.host') || '', path: f.get('rule.path') || '', wildcard: f.get('rule.wildcard') || '', github: f.get('rule.github') || '',
      methods: f.getAll('rule.method'), anyMethod: f.get('rule.any_method') === 'on', passthrough: f.get('rule.passthrough') === 'on', write: f.get('rule.write') === 'on', expires: f.get('rule.expires')});
    if (w.rules.some(x => x.description === r.description && !x.fromGit)) throw new Error('that rule already exists');
    w.rules.push(r);
    return true;
  }
  if (action === 'web-rule-remove') {
    const i = (w.rules || []).findIndex(r => r.id === f.get('rule.id'));
    if (i < 0) throw new Error('unknown rule');
    if (w.rules[i].fromGit) throw new Error('rules from Git change only in Git');
    w.rules.splice(i, 1);
    return true;
  }
  return false;
}

// --- Project sandboxes ---------------------------------------------------------

const rule = (host, methods = ['GET', 'HEAD'], paths = ['/']) => ({host, methods, paths});
const profileRules = name => name === 'standard' ? [...['proxy.golang.org', 'sum.golang.org', 'registry.npmjs.org', 'pypi.org', 'files.pythonhosted.org', 'index.crates.io', 'static.crates.io'], 'github.com', 'codeload.github.com', 'raw.githubusercontent.com', 'objects.githubusercontent.com', 'release-assets.githubusercontent.com'].map(h => ({...rule(h), source: 'profile:standard'})) : [];
export const PROFILES = [
  {name: 'ask', level: 'ask', description: 'Locked, plus each exact request an operator approved. New requests wait for approval.', allowedGrants: ['lan', 'package'], maxGrantMinutes: {lan: 1440, package: 43200}, approveRequests: true, projectSupported: false, reason: 'project sandboxes do not record blocked requests yet, so there is nothing to approve one by one'},
  {name: 'custom', level: 'custom', description: "The sandbox's own HTTPS rules and explicit LAN and package grants.", allowedGrants: ['lan', 'package'], maxGrantMinutes: {lan: 1440, package: 43200}, approveRequests: true, projectSupported: true},
  {name: 'locked', level: 'locked', description: "Nothing beyond the sandbox's own repository, model providers and the deployment's read-only reference sources.", allowedGrants: [], default: true, projectSupported: true},
  {name: 'open', level: 'open', description: 'Any public website while an internet grant is live (at most 8 hours).', allowedGrants: ['internet', 'lan', 'package'], maxGrantMinutes: {internet: 480, lan: 1440, package: 43200}, approveRequests: true, projectSupported: false, reason: 'the project proxy cannot open time-limited internet windows yet'},
  {name: 'standard', level: 'standard', description: 'Package downloads from npm, PyPI, Go modules and crates.io, and GitHub pages, raw files, archives and release assets. Read only.', allowedGrants: ['lan', 'package'], maxGrantMinutes: {lan: 1440, package: 43200}, approveRequests: true, projectSupported: true},
].map(p => ({...p, rules: profileRules(p.name)}));

const ready = (gen, phase = 'Ready') => ({phase, namespace: 'apc-x', observedGeneration: gen, conditions: [{type: 'Ready', status: phase === 'Ready' ? 'True' : 'False', reason: phase, message: phase, observedGeneration: gen}]});
export function levelProjects(t0) {
  return [
    {metadata: {name: 'quiet-project', uid: 'u2', generation: 2, resourceVersion: '10'}, spec: {repository: 'example/quiet-project', desiredState: 'Paused', accessProfileName: 'locked'}, status: ready(2, 'Paused')},
    {metadata: {name: 'std-project', uid: 'u3', generation: 4, resourceVersion: '20'}, spec: {repository: 'example/std-project', desiredState: 'Running', accessProfileName: 'standard', lanHosts: []}, status: ready(4)},
  ];
}
export const levelGrants = t0 => ({
  'std-project': [{metadata: {name: 'std-project-lan-1a2b3c4d', creationTimestamp: iso(t0 - 20 * MIN)}, spec: {cell: 'std-project', type: 'lan', lan: {address: '192.168.1.40', ports: [5432], note: 'Postgres on the dev box'}, expiresAt: iso(t0 + 40 * MIN), requestedBy: 'operator'}, status: {state: 'Active', message: 'applied to the lan-hosts policy'}}],
});

// Console API routes for levels; returns [status, body] or null when not a level route.
export function projectApi(projects, grants, r, method, body) {
  const m = r.match(/^workspaces\/([^/]+)\/(access-level|lock|grants|lan-hosts|access)(?:\/(.+))?$/) || r.match(/^workspaces\/([^/]+)()$/);
  if (r === 'access-profiles') return [200, PROFILES];
  if (!m) return null;
  const p = projects.find(x => x.metadata.name === decodeURIComponent(m[1]));
  if (!p) return [404, {error: 'project unavailable'}];
  const bump = () => { p.metadata.generation++; p.metadata.resourceVersion = String(Number(p.metadata.resourceVersion) + 1); p.status.observedGeneration = p.metadata.generation; p.status.conditions[0].observedGeneration = p.metadata.generation; };
  const fresh = () => body.resourceVersion === undefined || body.resourceVersion === p.metadata.resourceVersion;
  const paused = p.spec.desiredState === 'Paused' && p.status.phase === 'Paused';
  const list = grants[p.metadata.name] = grants[p.metadata.name] || [];
  switch (m[2]) {
    case '': {
      if (method !== 'PATCH') return null;
      if (!fresh()) return [409, {error: 'project changed; refresh before retrying'}];
      p.spec.desiredState = body.desiredState; p.status.phase = body.desiredState === 'Paused' ? 'Paused' : 'Ready'; p.status.conditions[0].status = p.status.phase === 'Ready' ? 'True' : 'False'; bump();
      return [200, p];
    }
    case 'access-level': {
      if (!fresh()) return [409, {error: 'project changed; refresh before retrying'}];
      const profile = PROFILES.find(x => x.name === body.profile);
      if (!profile) return [400, {error: `access profile ${body.profile} does not exist`}];
      if (!profile.projectSupported) return [400, {error: profile.reason}];
      if (profile.level !== 'custom' && (p.spec.https || []).length) { if (!paused) return [409, {error: 'pause it and wait for Paused to replace its own rules with a level'}]; p.spec.https = []; }
      p.spec.accessProfileName = profile.name; bump();
      return [200, p];
    }
    case 'lock': {
      const n = list.length; list.length = 0;
      p.spec.accessProfileName = 'locked'; bump();
      return [200, {project: p, grantsRemoved: n, ownRules: (p.spec.https || []).length, lanHosts: (p.spec.lanHosts || []).length}];
    }
    case 'grants': {
      if (method === 'GET') return [200, list];
      if (method === 'DELETE') { const i = list.findIndex(g => g.metadata.name === m[3]); if (i >= 0) list.splice(i, 1); return [200, {removed: m[3]}]; }
      const profile = PROFILES.find(x => x.name === p.spec.accessProfileName);
      if (body.type !== 'lan') return [400, {error: 'the project proxy cannot enforce this grant yet; nothing was allowed'}];
      if (!profile?.allowedGrants.includes('lan')) return [409, {error: `the ${profile?.level || 'current'} level does not allow lan grants; choose Custom or Standard first`}];
      if (body.minutes > profile.maxGrantMinutes.lan) return [400, {error: 'lan grants last at most 24h at this level'}];
      const g = {metadata: {name: `${p.metadata.name}-lan-${Math.random().toString(16).slice(2, 10)}`, creationTimestamp: iso(unix())}, spec: {cell: p.metadata.name, type: 'lan', lan: body.lan, expiresAt: iso(unix() + body.minutes * MIN), requestedBy: 'operator (preview)'}, status: {state: 'Active', message: 'applied to the lan-hosts policy'}};
      list.push(g);
      return [201, g];
    }
    case 'lan-hosts': {
      if (!fresh()) return [409, {error: 'project changed; refresh before retrying'}];
      p.spec.lanHosts = p.spec.lanHosts || [];
      if (method === 'POST') p.spec.lanHosts.push({address: body.address, ports: body.ports, note: body.note, addedBy: 'operator (preview)', addedAt: new Date().toISOString()});
      else p.spec.lanHosts = p.spec.lanHosts.filter(h => h.address !== decodeURIComponent(m[3]));
      bump();
      return [method === 'POST' ? 201 : 200, p];
    }
    case 'access': {
      if (!fresh()) return [409, {error: 'project changed; refresh before retrying'}];
      if (!paused) return [409, {error: 'pause the project and wait for Paused before editing access'}];
      p.spec.https = body.https; bump();
      return [200, p];
    }
  }
  return null;
}
