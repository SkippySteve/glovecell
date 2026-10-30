// Local preview of the built console against invented fixtures. No Kubernetes,
// Forgejo or sandbox-control is contacted, and no real data is involved.
//
//   npm run build && node scripts/preview.mjs [port] [host]
//
// It serves internal/web/dist with the console's CSP and answers the console
// API (/api/v1) and the sandbox-control mount (/sandboxes) from memory, so
// actions (open, lock, approve, LAN open/extend/restrict, dismiss) change the
// fixture state until the process restarts. Authentication is not simulated.
import {createServer} from 'node:http';
import {readFileSync, existsSync} from 'node:fs';
import {extname, join, normalize} from 'node:path';
import {fileURLToPath} from 'node:url';

const port = Number(process.argv[2] || 8790), host = process.argv[3] || '127.0.0.1';
const dist = fileURLToPath(new URL('../../internal/web/dist/', import.meta.url));
const CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'sha256-vai9tBS1dbqDlLt2vYYQwJl16j1hMfOCO5KhCK8LRBg='; frame-ancestors 'none'; base-uri 'none'; form-action 'self'";
const TYPES = {'.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2'};

const MIN = 60, HOUR = 3600, DAY = 86400;
const unix = () => Math.floor(Date.now() / 1000);
const iso = t => t ? new Date(t * 1000).toISOString().replace(/\.\d+Z$/, 'Z') : '';
const t0 = unix();

function observations(prefix, n, seed, approved) {
  const hosts = ['registry.npmjs.org', 'proxy.golang.org', 'pypi.org', 'files.pythonhosted.org', 'github.com', 'objects.githubusercontent.com', 'crates.io', 'api.github.com', 'nodejs.org', 'deb.debian.org'];
  const out = [];
  for (let i = 0; i < n; i++) {
    const k = i * 7919 + seed, h = hosts[k % hosts.length];
    const path = ['/pkg/', '/v2/', '/simple/', '/releases/download/', '/dl/'][k % 5] + (k % 99991).toString(36);
    const pkg = h === 'registry.npmjs.org' ? {ecosystem: 'npm', name: 'example-' + (k % 97).toString(36), version: '1.' + (k % 9) + '.0'} : undefined;
    out.push({host: h, method: i % 11 ? 'GET' : 'HEAD', path, scheme: 'https', port: 443, queryHash: '', package: pkg, fingerprints: 1 + (k % 3 === 0 ? 2 : 0), requests: 1 + (k % 47), first: t0 - (k % 5000) * MIN - HOUR, last: t0 - (k % 5000) * MIN, observation: `${prefix}-${seed}-${i}`, approved});
  }
  return out;
}

function workspace(id, extra) {
  return {
    id, namespace: 'agent-workspace-' + id, sandbox: 'workspace', mode: 'Running', learningEgress: true,
    learning: {phase: 'Locked', minutes: 60, started: t0 - HOUR, expires: 0},
    pending: [], approved: [], reports: [], devlinks: [], previews: [], providerHosts: ['api.anthropic.com', 'api.openai.com'],
    revisions: [{id: 12, status: 'Active', actor: 'operator', at: t0 - 2 * DAY}, {id: 11, status: 'Active', actor: 'operator', at: t0 - 4 * DAY}],
    lan: null, pod: 'a41c9e2f-0000-4000-8000-' + id.padEnd(12, '0').slice(0, 12), ...extra,
  };
}

const lanHost = (address, all, ports, learned = []) => ({address, listed: true, all, ports, grant: null, learned, learnedGrant: all ? learned.filter(l => l.verdict === 'used' || l.verdict === 'refused').map(l => l.port) : [], suggestedAllowLine: ''});
const W = {
  'coding-a': workspace('coding-a', {
    learning: {phase: 'Learning', minutes: 60, started: t0 - 48 * MIN, expires: t0 + 12 * MIN},
    pending: [...observations('p', 3, 1, false), {host: 'registry.npmjs.org', method: 'GET', path: '/left-pad-example/-/left-pad-example-2.1.0.tgz', scheme: 'https', port: 443, queryHash: '', package: {ecosystem: 'npm', name: 'left-pad-example', version: '2.1.0'}, fingerprints: 1, requests: 2, first: t0 - 35 * MIN, last: t0 - 9 * MIN, observation: 'p-pkg'}, {host: 'telemetry.example.com', method: 'POST', path: '/v1/events', scheme: 'https', port: 443, queryHash: '', fingerprints: 5, requests: 17, first: t0 - 2 * HOUR, last: t0 - 52 * MIN, observation: 'p-telemetry'}],
    approved: observations('a', 212, 3, true),
    reports: [{id: 15, kind: 'other', resource: '192.168.1.50:1337', reason: 'TCP connect to 192.168.1.50:1337 times out', count: 3, first: iso(t0 - 41 * MIN), last: iso(t0 - 6 * MIN)},
      {id: 16, kind: 'web', resource: 'https://telemetry.example.com/v1/events', reason: 'The test suite posts usage events here and fails when the request is refused', count: 1, first: iso(t0 - 52 * MIN), last: iso(t0 - 52 * MIN), requests: {filter: 'telemetry.example.com', waiting: 1, approved: 0, note: ''}}],
    ruleHistory: [{at: iso(t0 - 26 * HOUR), action: 'added', rule: 'r1', description: 'GET, HEAD on api.github.com/repos/example/**', by: 'operator', expires: iso(t0 + 6 * DAY), detail: ''},
      {at: iso(t0 - 3 * DAY), action: 'removed', rule: 'r0', description: 'Any method on uploads.example.com', by: 'operator', expires: '', detail: 'no longer needed'}],
    devlinks: [{Name: 'docs-preview', State: 'Pending', Port: 5173, Cwd: '/workspace/example/web', Note: 'Vite preview of the docs site', Commit: '9f3c2ab41e07aa00', Branch: 'task/example/docs', med: 2, low: 5, requested: t0 - 20 * MIN},
      {Name: 'chart-demo', State: 'Scan failed', Port: 8080, Cwd: '/workspace/example/charts', Commit: '0c4d18e2b97f3a55', Branch: 'task/example/charts', requested: t0 - 3 * HOUR, scan: 'Failed', scanError: 'the scanner timed out after 10 minutes'}],
    lan: {hosts: [lanHost('192.168.1.50', true, [], [{port: 1337, verdict: 'used', firstSeen: iso(t0 - 3 * DAY), lastSeen: iso(t0 - 2 * DAY), count: 14}, {port: 50000, verdict: 'used', firstSeen: iso(t0 - 2 * DAY), lastSeen: iso(t0 - 2 * DAY), count: 6}, {port: 50013, verdict: 'refused', firstSeen: iso(t0 - 2 * DAY), lastSeen: iso(t0 - 2 * DAY), count: 3}]), lanHost('192.168.1.60', false, [22, 2122])],
      history: [{at: iso(t0 - 2 * DAY), event: 'expired', host: '192.168.1.50', ports: 'all', by: 'sandbox-control', deadline: '', detail: 'rule removed; the workspace pod restarts'}, {at: iso(t0 - 2 * DAY - 50 * MIN), event: 'opened', host: '192.168.1.50', ports: 'all', by: 'operator', deadline: iso(t0 - 2 * DAY + 10 * MIN), detail: ''}]},
  }),
  'orca-runtime': workspace('orca-runtime', {
    learning: {phase: 'Open', minutes: 240, started: t0 - 50 * MIN, expires: t0 + 3 * HOUR + 10 * MIN},
    pending: observations('q', 2, 5, false), approved: observations('b', 96, 5, true),
    devlinks: [{Name: 'example-docs', State: 'Approved', Live: true, Port: 4173, Cwd: '/workspace/example/docs', Commit: '51d0e9a7c2b4ee11', Branch: 'main', low: 1, requested: t0 - DAY, expires: t0 + 6 * DAY + 3 * HOUR},
      {Name: 'storybook', State: 'Scanning', Port: 6006, Cwd: '/workspace/example/ui', Note: 'Component gallery', Commit: 'a7e2c90d13f4b866', Branch: 'task/example/ui', requested: t0 - 2 * MIN, scan: 'Running'},
      {Name: 'admin-console', State: 'Offline', Port: 3000, Cwd: '/workspace/example/admin', Commit: '3b8f0e6a2d91c7f4', Branch: 'main', requested: t0 - 3 * DAY, expires: t0 + 4 * DAY, approvedDigest: '3b8f0e6a2d91c7f4'},
      {Name: 'api-mock', State: 'Expired', Port: 4010, Cwd: '/workspace/example/api', Commit: '6d2a9b0f4e81c3a7', Branch: 'main', requested: t0 - 9 * DAY, expires: t0 - DAY}],
    previews: [{id: 'v1', state: 'Pending', proposedName: 'vite', proposedRulesText: 'GET,HEAD /\nGET prefix:/assets/', rulesText: '', lan: false, tailscale: false, gapAcknowledged: false, seen: t0 - 2 * MIN, first: t0 - HOUR, count: 4, listener: {project: 'example', worktree: 'docs', port: 5173, pid: 48211, address: '0.0.0.0', loopback: false}, urls: {lan: 'https://preview-v1.example.com', tailnet: 'https://preview-v1-ts.example.com'}},
      {id: 'v2', state: 'Approved', live: true, proposedName: 'admin-ui', proposedRulesText: 'GET,HEAD prefix:/', rulesText: 'GET,HEAD prefix:/', lan: true, tailscale: true, gapAcknowledged: true, seen: t0, first: t0 - 5 * HOUR, count: 120, listener: {project: 'example', worktree: 'admin', port: 8000, pid: 50112, address: '0.0.0.0', loopback: false}, urls: {lan: 'https://preview-v2.example.com', tailnet: 'https://preview-v2-ts.example.com'}},
      {id: 'v3', state: 'Needs network binding', proposedName: 'notebook', proposedRulesText: 'GET,HEAD prefix:/', rulesText: '', lan: false, tailscale: false, gapAcknowledged: false, seen: t0 - MIN, first: t0 - 20 * MIN, count: 7, listener: {project: 'example', worktree: 'research', port: 8888, pid: 51200, address: '127.0.0.1', loopback: true}, urls: {lan: 'https://preview-v3.example.com', tailnet: 'https://preview-v3-ts.example.com'}},
      {id: 'v0', state: 'Expired', proposedName: 'old-api', proposedRulesText: 'GET prefix:/', rulesText: 'GET prefix:/', lan: true, tailscale: false, gapAcknowledged: true, seen: t0 - 2 * DAY, first: t0 - 3 * DAY, count: 300, listener: {project: 'example', worktree: 'api', port: 9000, pid: 40001, address: '0.0.0.0', loopback: false}, urls: {lan: 'https://preview-v0.example.com', tailnet: 'https://preview-v0-ts.example.com'}}],
    lan: {hosts: [{...lanHost('192.168.1.50', true, []), grant: {mode: 'ports', ports: [22], openedBy: 'operator', openedAt: iso(t0 - 52 * MIN), deadline: iso(t0 + 8 * MIN), state: 'Open', reason: ''}}], history: [{at: iso(t0 - 52 * MIN), event: 'opened', host: '192.168.1.50', ports: '22', by: 'operator', deadline: iso(t0 + 8 * MIN), detail: ''}]},
  }),
  'build-box': workspace('build-box', {status: 'Failed', learningEgress: false, learning: null,
    revisions: [{id: 4, status: 'Rejected', error: 'policy rejected: profile "builder" names unknown service "artifact-cache"', actor: 'operator', at: t0 - 3 * HOUR}, {id: 3, status: 'Active', actor: 'operator', at: t0 - 6 * DAY}],
    lan: {hosts: [], history: []}}),
  'research': workspace('research', {learningEgress: false, learning: null, mode: 'Suspended', lan: 'research'}),
};

function apiWorkspace(w) {
  return {id: w.id, namespace: w.namespace, sandbox: w.sandbox, mode: w.mode, revision: w.revisions[0], learningEgress: w.learningEgress, tailnetReady: true, serviceOwner: w.id === 'coding-a',
    learning: w.learning ? {...w.learning} : undefined, providerHosts: w.providerHosts, previewPending: w.previews.filter(p => p.state === 'Pending').length, devLinkPending: w.devlinks.filter(d => d.State === 'Pending').length,
    egressCounts: w.learningEgress ? {pending: w.pending.length, approved: w.approved.length} : undefined, links: {}, services: w.id === 'coding-a' ? [{name: 'telemetry', host: 'telemetry.example.com', editable: true, lanAvailable: true, tailscaleAvailable: true, grant: {lan: true, tailscale: false, rulesText: 'GET,HEAD prefix:/'}}] : [], revisions: w.revisions};
}
function summary(w) {
  const phase = w.learning?.phase;
  return {id: w.id, namespace: w.namespace, sandbox: w.sandbox, mode: w.mode, revision: w.revisions[0].id, status: w.status || 'Active', learning: phase || '', learningExpires: w.learning?.expires || 0, egress: phase || '', egressExpires: w.learning?.expires || 0, egressControl: w.learningEgress, pending: w.pending.length, approvals: w.previews.filter(p => p.state === 'Pending').length + w.devlinks.filter(d => d.State === 'Pending').length};
}
function apiLan(w) {
  if (w.lan === 'research') return {workspace: w.id, enabled: false, reason: 'LAN host grants are not available for the research workspace', error: '', running: false, podUid: '', windows: [900, 3600, 14400], limits: {grantsPerWorkspace: 8, portsPerGrant: 32}, hosts: [], history: []};
  const running = w.mode === 'Running';
  return {workspace: w.id, enabled: true, reason: '', error: '', running, podUid: running ? w.pod : '', windows: [900, 3600, 14400], limits: {grantsPerWorkspace: 8, portsPerGrant: 32},
    hosts: w.lan.hosts.map(h => ({...h, grant: h.grant ? {...h.grant} : null})), history: [...w.lan.history]};
}
function apiDevLinks(w) {
  return w.devlinks.map(d => ({Name: d.Name, Workspace: w.id, State: d.State, URL: `https://${d.Name}.dev.example.com/`, Host: `${d.Name}.dev.example.com`, Port: d.Port, Cwd: d.Cwd, Project: 'example', Worktree: 'main', Note: d.Note || '', RulesText: 'GET,HEAD prefix:/',
    Scan: {ID: 's-' + d.Name, Status: d.scan || 'Complete', Error: d.scanError || '', Started: d.requested, Finished: d.scan === 'Running' ? 0 : d.requested + 90, Root: d.Cwd, Commit: d.Commit, Branch: d.Branch, Remote: '', Worktree: 'clean', Modified: null, Packages: null, Vulns: null, Secrets: null, Scanner: 'Trivy', Truncated: false, Digest: d.Commit},
    Approved: Boolean(d.Live), ApprovedDigest: d.Live ? d.Commit : d.approvedDigest || '', Expires: d.expires || 0, Requested: d.requested, Decided: 0, Actor: 'operator', Live: Boolean(d.Live),
    Counts: {packages: 61, unapproved: 0, critical: 0, high: 0, medium: d.med || 0, low: d.low || 0, secrets: 0, modified: 0},
    Events: [{Action: d.Live ? 'approved for 7 days' : 'published (scan complete)', Actor: d.Live ? 'operator' : 'agent', Detail: 'build ' + d.Commit.slice(0, 12), At: d.requested}], AccessTotal: d.Live ? 11 : 0, Egress: '', Viewers: 'operator via the identity provider'}));
}
const page = (list, q) => { const items = list.filter(o => !q || (o.host + o.path).includes(q)); return {total: items.length, items: items.slice(0, 500)}; };

const project = {metadata: {name: 'demo-project', uid: 'u1', generation: 3, resourceVersion: '42'}, spec: {repository: 'example/demo-project', desiredState: 'Running', https: [{host: 'api.github.com', methods: ['GET'], paths: ['/repos/example/demo-project']}, {host: 'registry.npmjs.org', methods: ['GET', 'HEAD'], paths: ['/']}], lanHosts: [{address: '192.168.1.31', ports: [22], note: 'Desktop SSH', addedBy: 'operator', addedAt: new Date((t0 - 3 * DAY) * 1000).toISOString()}]}, status: {phase: 'Ready', namespace: 'apc-demo-project', observedGeneration: 3, conditions: [{type: 'Ready', status: 'True', reason: 'Ready', message: 'Ready', observedGeneration: 3}]}};

// A second, paused project so Home's filters and sort orders have something to sort.
const pausedProject = {metadata: {name: 'docs-site', uid: 'u2', generation: 2, resourceVersion: '17'}, spec: {repository: 'example/docs-site', desiredState: 'Paused', https: []}, status: {phase: 'Paused', namespace: 'apc-docs-site', observedGeneration: 2, conditions: [{type: 'Ready', status: 'False', reason: 'Paused', message: 'Paused by operator', observedGeneration: 2}]}};
const blockedProject = {metadata: {name: 'handbook', uid: 'u3', generation: 2, resourceVersion: '23'}, spec: {repository: 'example/handbook', desiredState: 'Running', https: []}, status: {phase: 'Preparing', namespace: 'apc-handbook', observedGeneration: 2, conditions: [{type: 'Ready', status: 'False', reason: 'PreparationPending', message: 'Forgejo branch rule for main on example/handbook falls short: require at least 1 approval (currently 0). Fix it in Settings > Branches.', observedGeneration: 2}]}};

// Run records (invented), with times relative to now so the inbox shows them.
const at = s => iso(t0 - s);
const attempt = (agent, extra) => ({dispatchId: 'ctx_' + agent + '_' + Math.abs(extra.startedAt ? Date.parse(extra.startedAt) % 9973 : 1), agent, state: 'running', ...extra});
const task = (id, title, status, attempts, extra = {}) => ({id, title, spec: title + '.', status, deps: [], attempts, checks: [], ...extra});
const RUNS = [
  {version: 1, run: {id: 'run_csv_export', objective: 'Add CSV export to the reports page', host: 'agent-workspace-coding-a/workspace', createdAt: at(2 * HOUR), collectedAt: at(12 * MIN)},
    tasks: [task('task_csv_api', 'CSV endpoint', 'completed', [attempt('codex', {state: 'settled', outcome: 'succeeded', startedAt: at(2 * HOUR - 2 * MIN), endedAt: at(HOUR + 20 * MIN)})], {checks: [{name: 'unit tests', status: 'passed', source: 'ci'}]}),
      task('task_csv_ui', 'Export button and download', 'failed', [attempt('claude', {state: 'failed', outcome: 'failed', failedStage: 'checks', lastError: 'npm test exited 1: 3 failing tests in export.test.ts (expected header row)', startedAt: at(HOUR + 15 * MIN), endedAt: at(25 * MIN)})], {checks: [{name: 'unit tests', status: 'failed', source: 'ci', detail: 'FAIL export.test.ts > writes a header row'}]})],
    gates: [], agentReports: []},
  {version: 1, run: {id: 'run_dep_upgrade', objective: 'Upgrade the build to Node 24 and refresh the lockfile', host: 'agent-workspace-coding-a/workspace', createdAt: at(6 * HOUR), collectedAt: at(8 * MIN)},
    tasks: [task('task_node24', 'Bump Node and fix build', 'running', [attempt('codex', {state: 'running', startedAt: at(5 * HOUR + 30 * MIN)})])], gates: [], agentReports: []},
  {version: 1, run: {id: 'run_textkit_cli', objective: 'textkit helpers and a CLI', host: 'agent-workspace-orca-runtime/workspace', createdAt: at(90 * MIN), collectedAt: at(4 * MIN)},
    tasks: [task('task_slugify', 'slugify helper', 'completed', [attempt('codex', {state: 'settled', outcome: 'succeeded', startedAt: at(88 * MIN), endedAt: at(70 * MIN)})], {checks: [{name: 'unit tests', status: 'passed', source: 'ci'}]}),
      task('task_cli', 'Integrate helpers into a CLI', 'blocked', [attempt('claude', {state: 'running', startedAt: at(40 * MIN)})])],
    gates: [{id: 'gate_cli_output', taskId: 'task_cli', question: 'Should `textkit top` print a trailing newline after the last line?', options: ['Keep the newline', 'Drop the newline'], status: 'pending'}], agentReports: []},
  {version: 1, run: {id: 'run_docs_links', objective: 'Fix broken links in the docs site', host: 'agent-workspace-orca-runtime/workspace', createdAt: at(DAY + 2 * HOUR), collectedAt: at(DAY)},
    tasks: [task('task_links', 'Link checker and fixes', 'completed', [attempt('claude', {state: 'settled', outcome: 'succeeded', startedAt: at(DAY + 2 * HOUR), endedAt: at(DAY + HOUR + 12 * MIN)})], {checks: [{name: 'link check', status: 'passed', source: 'collector'}]})],
    gates: [], agentReports: []},
  {version: 1, run: {id: 'run_old_trial', objective: 'Orchestration trial: parallel helpers on separate worktrees', host: 'agent-workspace-coding-a/workspace', createdAt: at(6 * DAY), collectedAt: at(5 * DAY)},
    tasks: [task('task_old', 'slugify helper', 'failed', [attempt('codex', {state: 'failed', failedStage: 'agent_readiness', lastError: 'Agent startup blocked: agent-update-prompt', startedAt: at(6 * DAY), endedAt: at(6 * DAY - 4 * MIN)})])],
    gates: [], agentReports: []},
];
// Mirrors summarizeRun in internal/web/runs.go.
function runSummary(rec) {
  const s = {id: rec.run.id, objective: rec.run.objective, host: rec.run.host, createdAt: rec.run.createdAt, collectedAt: rec.run.collectedAt, tasks: rec.tasks.length, failed: 0, succeeded: 0, running: 0, pendingGates: rec.gates.filter(g => g.status === 'pending').length};
  const starts = [], ends = [];
  for (const t of rec.tasks) {
    const last = t.attempts.at(-1);
    for (const a of t.attempts) { if (a.startedAt) starts.push(a.startedAt); if (a.endedAt) ends.push(a.endedAt); }
    if (last?.outcome === 'succeeded') s.succeeded++;
    else if (last?.outcome === 'failed' || last?.state === 'failed' || t.status === 'failed') { s.failed++; s.failure ??= {task: t.id, title: t.title, stage: last?.failedStage, error: last?.lastError}; }
    else if (last && !last.endedAt) s.running++;
  }
  s.status = s.failed ? 'failed' : s.pendingGates ? 'waiting' : s.running ? 'running' : s.tasks && s.succeeded === s.tasks ? 'succeeded' : 'pending';
  starts.sort(); ends.sort();
  if (starts.length) s.startedAt = starts[0];
  if (ends.length && !s.running) s.endedAt = ends.at(-1);
  const all = [...starts, ...ends].sort();
  if (all.length) s.lastActivity = all.at(-1);
  return s;
}

function act(w, path, f) {
  const action = f.get('action') || '';
  const seconds = Number(f.get('duration') || 0), minutes = Number(f.get('minutes') || 60);
  const lanHostOf = a => w.lan?.hosts?.find(h => h.address === a);
  const lanEvent = (event, h, ports, detail = '') => w.lan.history.unshift({at: iso(unix()), event, host: h, ports, by: 'operator', deadline: '', detail});
  if (path === '/egress' && action.startsWith('lan-')) {
    if (w.lan === 'research') throw new Error('LAN host grants are not available for this workspace');
    const h = lanHostOf(f.get('lan.address'));
    if (!h) throw new Error('choose a LAN host');
    if (action === 'lan-forget') { h.learned = []; h.learnedGrant = []; lanEvent('ports-forgotten', h.address, ''); return; }
    if (action === 'lan-restrict') {
      if (!h.grant) throw new Error('This LAN host is not open');
      w.lan.hosts.forEach(x => { if (x.grant) x.grant.state = 'Restricting'; });
      lanEvent('restricted', h.address, '', 'restricted now; the workspace pod restarts');
      setTimeout(() => { w.lan.hosts.forEach(x => { x.grant = null; }); w.pod = w.pod.slice(0, -4) + String(Math.floor(Math.random() * 9000) + 1000); lanEvent('revoked', h.address, '', 'rule removed; workspace pod restarted'); }, 4000);
      return;
    }
    if (w.mode !== 'Running') throw new Error('Start the workspace before opening LAN access');
    if (f.get('pod_uid') !== w.pod) throw new Error('Worker changed; reload before changing LAN access');
    if (![900, 3600, 14400].includes(seconds)) throw new Error('Choose 15 minutes, one hour or four hours');
    if (action === 'lan-extend') { if (!h.grant) throw new Error('This LAN host is not open; open it instead'); h.grant.deadline = iso(unix() + seconds); lanEvent('extended', h.address, h.grant.mode === 'all' ? 'all' : h.grant.ports.join(', '), 'window restarts now'); return; }
    if (h.grant) throw new Error('This LAN host is already open or closing; extend or restrict it');
    let mode = 'ports', ports = f.getAll('lan.port').map(Number);
    if (action === 'lan-open-all') { if (!h.all) throw new Error('not allowlisted for all ports'); mode = 'all'; ports = []; }
    if (action === 'lan-open-learned') ports = [...h.learnedGrant];
    if (mode === 'ports' && (!ports.length || (!h.all && ports.some(p => !h.ports.includes(p))))) throw new Error('Choose allowlisted ports');
    h.grant = {mode, ports, openedBy: 'operator', openedAt: iso(unix()), deadline: iso(unix() + seconds), state: 'Opening', reason: ''};
    lanEvent('opened', h.address, mode === 'all' ? 'all' : ports.join(', '));
    const g = h.grant;
    setTimeout(() => { if (h.grant === g) { g.state = 'Open'; lanEvent('applied', h.address, '', 'lan-hosts policy applied'); } }, 2500);
    return;
  }
  if (path === '/egress') {
    const find = (list, id) => list.findIndex(o => o.observation === id);
    switch (action) {
      case 'learning-open': case 'learning-resume': case 'learning-start':
        if (action === 'learning-start') { w.pending = []; w.approved = []; }
        w.learning = {phase: action === 'learning-open' ? 'Open' : 'Learning', minutes, started: unix(), expires: unix() + minutes * MIN}; return;
      case 'learning-lock': w.learning = {phase: 'Locked', minutes, started: unix(), expires: 0}; return;
      case 'approve-endpoint': case 'approve-learning': { const i = find(w.pending, f.get('observation')); if (i < 0) throw new Error('unknown request'); w.approved.unshift(...w.pending.splice(i, 1)); return; }
      case 'revoke-endpoint': case 'revoke-learning': { const i = find(w.approved, f.get('observation')); if (i < 0) throw new Error('unknown request'); w.approved.splice(i, 1); return; }
      case 'clear-unapproved': w.pending = []; return;
    }
    throw new Error('unknown action');
  }
  if (path === '/lifecycle') { w.mode = f.get('mode'); if (w.mode !== 'Running' && w.lan?.hosts) w.lan.hosts.forEach(h => { h.grant = null; }); w.revisions.unshift({id: w.revisions[0].id + 1, status: 'Active', actor: 'operator', at: unix()}); return; }
  if (path === '/dismiss-report') { w.reports = w.reports.filter(r => String(r.id) !== f.get('report')); return; }
  if (path === '/devlink-decision') { const d = w.devlinks.find(x => x.Name === f.get('name')); if (!d) throw new Error('unknown dev link'); const state = f.get('state'); if (state === 'Deleted') w.devlinks = w.devlinks.filter(x => x !== d); else { d.State = state; d.Live = state === 'Approved'; d.expires = d.Live ? unix() + 7 * DAY : 0; } return; }
  if (path === '/decision') { const p = w.previews.find(x => x.id === f.get('id')); if (!p) throw new Error('unknown preview'); p.state = f.get('state'); return; }
  if (['/services', '/profile', '/rollback', '/allow-request'].includes(path)) return;
  throw new Error('not supported in the preview');
}

// "Download into sandbox" fixtures: invented servers and redirect chains.
//   github.com/.../releases/download/...  302 -> objects.githubusercontent.com with a fresh signature per request
//   ...loop...  a redirect loop · ...downgrade...  a redirect to http · ...missing...  404 · anything else  200
const checks = {}, jobs = {}, downloadAudit = [];
const rid = () => Math.random().toString(36).slice(2, 8).padEnd(6, '0') + Math.random().toString(36).slice(2, 8).padEnd(6, '0');
const sha = () => Array.from({length: 64}, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('');
const redact = u => { try { const x = new URL(u); return x.origin + x.pathname + (x.search ? '?<query sha256:' + sha().slice(0, 16) + '>' : ''); } catch { return u; } };
function fixtureAnswer(u) {
  const x = new URL(u);
  if (x.hostname === 'github.com' && x.pathname.includes('/releases/download/')) return {status: 302, location: `https://objects.githubusercontent.com/github-production-release-asset/${x.pathname.split('/').pop()}?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=${sha()}&X-Amz-Date=${Date.now()}`};
  if (x.hostname === 'objects.githubusercontent.com') return {status: 200, length: 48 * 1024 * 1024 + 4242, type: 'application/gzip', probe: 'GET'};
  if (x.pathname.includes('loop')) return {status: 302, location: x.pathname.endsWith('/b') ? x.origin + x.pathname.slice(0, -2) : x.origin + x.pathname + '/b'};
  if (x.pathname.includes('downgrade')) return {status: 301, location: 'http://' + x.host + x.pathname};
  if (x.pathname.includes('missing')) return {status: 404};
  return {status: 200, length: 1258291, type: 'application/octet-stream', probe: 'HEAD'};
}
function hop(u, prev) { const x = new URL(u); return {url: u, redacted: redact(u), host: x.hostname, status: 0, crossHost: Boolean(prev && prev !== x.hostname), queryHash: x.search ? sha().slice(0, 16) : '', contentLength: -1, contentType: '', probe: 'HEAD', refused: ''}; }
function startCheck(name, raw) {
  let u;
  try { u = new URL(raw); } catch { throw new Error('URL does not parse'); }
  if (u.protocol !== 'https:') throw new Error('only https URLs are downloaded; http is refused');
  const id = rid(), c = {id, workspace: name, input: raw, state: 'checking', hops: [], digest: '', error: '', defaultDestination: '~/downloads/' + (u.pathname.split('/').pop() || 'download'), requestedBy: 'operator (preview)', started: new Date().toISOString(), expires: new Date(Date.now() + 15 * 60000).toISOString()};
  checks[id] = c;
  const seen = new Set();
  let cur = raw, prev = '';
  const step = () => {
    const h = hop(cur, prev);
    if (seen.has(cur)) { h.refused = 'redirect loop: this URL appeared earlier in the chain'; c.hops.push(h); c.state = 'failed'; c.error = 'the redirect chain loops'; return finishCheck(c); }
    seen.add(cur);
    const a = fixtureAnswer(cur);
    h.status = a.status; h.probe = a.probe || 'HEAD';
    if (a.status === 200) { h.contentLength = a.length; h.contentType = a.type; c.hops.push(h); c.state = 'ready'; c.digest = sha(); return finishCheck(c); }
    if (a.status >= 300 && a.status < 400) {
      if (a.location.startsWith('http:')) { h.refused = 'redirect downgrades https to http; refused'; c.hops.push(h); c.state = 'failed'; c.error = 'a redirect was refused: ' + h.refused; return finishCheck(c); }
      c.hops.push(h); prev = h.host; cur = a.location; return setTimeout(step, 700);
    }
    c.hops.push(h); c.state = 'failed'; c.error = `the server answered ${a.status}`; finishCheck(c);
  };
  setTimeout(step, 600);
  return c;
}
function finishCheck(c) { downloadAudit.unshift({time: new Date().toISOString(), action: 'download-chain-checked', message: `Download redirect chain checked by operator: ${c.hops.map(h => h.redacted + ' [' + h.status + ']').join(' -> ')} (${c.state})`}); }
function startJob(name, b) {
  const c = checks[b.checkId];
  if (!c || c.state !== 'ready' || c.digest !== b.digest) throw new Error('that redirect chain is no longer approved (changed, expired or not checked); check the URL again');
  delete checks[b.checkId];
  const id = rid(), last = c.hops[c.hops.length - 1];
  const j = {id, workspace: name, checkId: c.id, input: c.input, state: 'starting', hops: c.hops, hop: 0, followed: [], destination: b.destination, overwrite: Boolean(b.overwrite), expectedSha256: b.sha256 || '', acceptQueryChanges: Boolean(b.acceptQueryChanges), maxBytes: 2 * 1024 ** 3, bytes: 0, total: -1, sha256: '', path: '', code: '', error: '', change: null, note: '', requestedBy: 'operator (preview)', started: new Date().toISOString()};
  jobs[id] = j;
  downloadAudit.unshift({time: new Date().toISOString(), action: 'download-requested', message: `Download into sandbox requested by operator: ${c.hops[0].redacted} to ${b.destination} (approved chain of ${c.hops.length})`});
  const end = state => { j.state = state; j.finished = new Date().toISOString(); downloadAudit.unshift({time: j.finished, action: 'download-finished', message: `Download ${state}: ${c.hops[0].redacted} to ${j.destination}${state === 'done' ? ` (${j.bytes} bytes, sha256 ${j.sha256})` : j.error ? ' (' + j.error + ')' : ''}`}); };
  j.followed.push(c.hops[0].redacted);
  setTimeout(() => {
    if (j.state === 'cancelled') return;
    const signed = c.hops.length > 1 && c.hops[1].host === 'objects.githubusercontent.com';
    if (signed && !j.acceptQueryChanges) {
      j.hop = 1; j.error = 'the redirect chain changed since it was checked; review the new chain';
      j.change = {hop: 1, expected: c.hops[1].redacted, observed: redact(fixtureAnswer(c.hops[0].url).location), queryOnly: true, reason: 'same host and path, different query (for example a fresh signature)'};
      return end('changed');
    }
    if (c.hops.length > 1) { j.hop = c.hops.length - 1; j.followed.push(...c.hops.slice(1).map(h => h.redacted)); }
    j.state = 'downloading'; j.total = last.contentLength;
    const t = setInterval(() => {
      if (j.state !== 'downloading') return clearInterval(t);
      j.bytes = Math.min(j.total, j.bytes + Math.ceil(j.total / 6));
      if (j.bytes >= j.total) {
        clearInterval(t);
        j.sha256 = j.expectedSha256 || sha(); j.path = j.destination;
        if (j.expectedSha256 && j.expectedSha256.startsWith('0000')) { j.sha256 = ''; j.code = 'checksum'; j.error = `SHA-256 mismatch: got ${sha()}, expected ${j.expectedSha256}; the file was deleted`; j.bytes = 0; return end('failed'); }
        end('done');
      }
    }, 700);
  }, 1200);
  return j;
}
function downloads(name, rest, method, b) {
  if (rest === 'download-checks' && method === 'POST') return [202, startCheck(name, b.url || '')];
  if (rest.startsWith('download-checks/')) { const c = checks[rest.split('/')[1]]; return c ? [200, c] : [404, {error: 'no such check'}]; }
  if (rest === 'downloads' && method === 'POST') return [202, startJob(name, b)];
  if (rest === 'downloads') return [200, Object.values(jobs).filter(j => j.workspace === name).sort((a, b2) => b2.started.localeCompare(a.started))];
  const [, id, cancel] = rest.split('/');
  const j = jobs[id];
  if (!j) return [404, {error: 'no such download'}];
  if (cancel === 'cancel' && method === 'POST') { if (!j.finished) { j.state = 'cancelled'; j.code = 'cancelled'; j.error = 'download cancelled'; j.finished = new Date().toISOString(); } return [202, {id}]; }
  return [200, j];
}

function tick() {
  const now = unix();
  for (const w of Object.values(W)) {
    if (w.learning && w.learning.phase !== 'Locked' && w.learning.expires <= now) w.learning = {phase: 'Locked', minutes: w.learning.minutes, started: now, expires: 0};
    for (const h of (w.lan?.hosts || [])) if (h.grant && h.grant.state === 'Open' && Date.parse(h.grant.deadline) / 1000 <= now) { h.grant.state = 'Restricting'; h.grant.reason = 'expired'; setTimeout(() => { h.grant = null; }, 3000); }
  }
}

const json = (res, status, body, headers = {}) => { res.writeHead(status, {'Content-Type': 'application/json', 'Cache-Control': 'no-store', ...headers}); res.end(JSON.stringify(body)); };
async function body(req) { let s = ''; for await (const c of req) s += c; return s; }

createServer(async (req, res) => {
  tick();
  const url = new URL(req.url, 'http://preview');
  const p = url.pathname, q = url.searchParams;
  try {
    if (p.startsWith('/api/v1/')) {
      const r = p.slice(8);
      if (r === 'session') return json(res, 200, {actor: 'operator (preview)', sandboxControl: true, runRecords: true, orcaProjects: true, orcaSandbox: 'orca-runtime', lanHosts: true, lanAllowed: ['192.168.1.0/24'], downloads: true, downloadMaxBytes: 2 * 1024 ** 3});
      const dl = r.match(/^workspaces\/([^/]+)\/(download-checks.*|downloads.*)$/);
      if (dl) {
        const b = req.method === 'POST' ? JSON.parse((await body(req)) || '{}') : {};
        try { const [status, out] = downloads(decodeURIComponent(dl[1]), dl[2], req.method, b); return json(res, status, out); } catch (e) { return json(res, 409, {error: e.message}); }
      }
      if (r === 'workspaces') return json(res, 200, [project, pausedProject, blockedProject]);
      if (r === 'repositories') return json(res, 200, [{full_name: 'example/demo-project'}]);
      if (r === 'orca-projects') return json(res, 200, [{name: 'demo-project', repo: 'example/demo-project', status: 'registered', clone: '/workspace/demo-project'}, {name: 'docs', repo: 'example/docs', status: 'published', actor: 'operator', requestedAt: new Date().toISOString()}]);
      if (r === 'runs') return json(res, 200, RUNS.map(runSummary).sort((a, b) => b.collectedAt.localeCompare(a.collectedAt)));
      if (r.startsWith('runs/')) { const rec = RUNS.find(x => x.run.id === decodeURIComponent(r.slice(5))); return rec ? json(res, 200, rec) : json(res, 404, {error: 'run record unavailable'}); }
      if (r.endsWith('/audit')) return json(res, 200, [...downloadAudit, {time: new Date((t0 - 3 * DAY) * 1000).toISOString(), action: 'lan-host-granted', message: '192.168.1.31 TCP 22 by operator'}, {time: new Date((t0 - 4 * DAY) * 1000).toISOString(), action: 'created', message: 'example/demo-project'}]);
      if (r.endsWith('/clients')) return json(res, 200, {clients: []});
      if (req.method === 'GET') return json(res, 404, {error: 'not served by the preview'});
      return json(res, 403, {error: 'the preview does not change console projects'});
    }
    if (p.startsWith('/sandboxes/')) {
      const r = p.slice(10);
      const headers = {'X-Sandbox-CSRF': 'preview'};
      if (req.method === 'POST') {
        const f = new URLSearchParams(await body(req));
        const w = W[f.get('workspace')];
        if (!w) return json(res, 404, {error: 'workspace not enrolled'});
        try { act(w, r, f); } catch (e) { return json(res, 400, {error: e.message}); }
        return json(res, 200, summary(w));
      }
      if (r === '/workspaces') return json(res, 200, Object.values(W).map(summary), headers);
      const w = W[q.get('workspace')];
      if (r.startsWith('/api/') && !w) return json(res, 404, {error: 'workspace not enrolled'});
      switch (r) {
        case '/api/workspace': return json(res, 200, apiWorkspace(w), headers);
        case '/api/egress': return w.learningEgress ? json(res, 200, {filter: q.get('q') || '', pending: page(w.pending, q.get('q')), approved: page(w.approved, q.get('q')), ...(w.ruleHistory ? {rules: [], ruleHistory: w.ruleHistory} : {})}, headers) : json(res, 409, {error: 'learning is not enrolled for this workspace'});
        case '/api/lan': return json(res, 200, apiLan(w), headers);
        case '/api/devlinks': return json(res, 200, apiDevLinks(w), headers);
        case '/api/previews': {
          // Approved fixtures marked live are "observed" on every request, as the host collector would.
          const now = unix(), all = w.previews.map(p => p.live && p.state === 'Approved' ? {...p, seen: now - 3} : p);
          const inactive = p => ['Expired', 'Revoked', 'Denied'].includes(p.state); // as sandbox-control apiPreviews
          return json(res, 200, {expiredCount: all.filter(inactive).length, requests: q.get('expired') ? all : all.filter(p => !inactive(p))}, headers);
        }
        case '/api/reports': return json(res, 200, {reports: w.reports}, headers);
        case '/api/cluster': return json(res, 200, {Identity: {Namespace: w.namespace, Name: 'agent'}, Grants: w.id === 'coding-a' ? [{Namespace: 'example-app', Name: 'grant-debug', Capability: 'debug', Subject: w.namespace + '/agent', Reason: 'inspect a crash loop', GrantedBy: 'operator', Expires: new Date((t0 + 40 * MIN) * 1000).toISOString(), Left: '', ThisSandbox: true}] : [], Namespaces: ['example-app'], Capabilities: [], Durations: []}, headers);
        case '/api/activity': return json(res, 200, {window: {start: '', end: '', minutes: Number(q.get('minutes') || 15)}, links: {}, sources: [{source: 'Web inspection', scope: w.namespace, at: unix(), items: [{title: 'GET telemetry.example.com', target: 'https://telemetry.example.com/v1/events', time: 'just now', outcome: 'Blocked', count: 4}]}], health: {cards: [{title: 'worker', target: '212 MiB memory in use'}]}}, headers);
      }
      res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8', 'Content-Security-Policy': CSP});
      return res.end('<!doctype html><title>Classic page</title><h1>Classic sandbox-control page</h1><p>Not part of the preview. <a href="/">Back to the console</a></p>');
    }
    const file = normalize(join(dist, p === '/' ? 'index.html' : p));
    if (!file.startsWith(dist) || !existsSync(file)) { res.writeHead(404); return res.end('not found'); }
    res.writeHead(200, {'Content-Type': TYPES[extname(file)] || 'application/octet-stream', 'Content-Security-Policy': CSP, 'X-Content-Type-Options': 'nosniff'});
    res.end(readFileSync(file));
  } catch (e) { json(res, 500, {error: String(e && e.message || e)}); }
}).listen(port, host, () => console.log(`console preview on http://${host}:${port}/ (fixtures only)`));
