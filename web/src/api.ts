// Typed clients for the console API (/api/v1) and the mounted sandbox-control service (/sandboxes).

export type Method = 'GET' | 'HEAD' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'OPTIONS';
// source is set only on the deployment's reference rules (reference.ts).
export interface HTTPSRule { host: string; methods: Method[]; paths: string[]; source?: string }
export interface ProjectRepository { name: string; host: string; repository: string; public?: boolean }
// Direct outbound TCP from the project worker to one IPv4 host on the operator's LAN.
export interface LANHost { address: string; ports: number[]; note?: string; addedBy?: string; addedAt?: string }
export interface ProviderGrant { name: string; addedBy?: string; addedAt?: string }
export interface Condition { type: string; status: string; reason: string; message: string; observedGeneration?: number }
export interface Project {
  metadata: { name: string; uid?: string; generation?: number; resourceVersion?: string };
  spec: { repository: string; desiredState: 'Running' | 'Paused' | 'Removed'; https?: HTTPSRule[]; accessProfileName?: string; repositories?: ProjectRepository[]; lanHosts?: LANHost[]; providers?: ProviderGrant[] };
  status?: { phase?: string; namespace?: string; observedGeneration?: number; conditions?: Condition[] };
}
// Access levels (docs/access-levels.md): GET /api/v1/access-profiles and a cell's AccessGrants.
export type ProfileLevel = 'locked' | 'ask' | 'standard' | 'open' | 'custom';
export interface AccessProfileView {
  name: string; level: ProfileLevel; description?: string; rules: HTTPSRule[]; allowedGrants: string[];
  maxGrantMinutes?: Record<string, number>; approveRequests?: boolean; default?: boolean; projectSupported: boolean; reason?: string;
}
export interface AccessGrant {
  metadata: {name: string; creationTimestamp?: string};
  spec: {cell: string; type: 'internet' | 'lan' | 'package'; expiresAt: string; reason?: string; requestedBy?: string;
    lan?: {address: string; ports: number[]; note?: string}; package?: {ecosystem: string; name: string; version?: string}; internet?: {learn?: boolean}};
  status?: {state?: 'Active' | 'Refused' | 'Expired'; message?: string};
}
// null when this controller has no access profiles (older release or CRD not installed).
export async function accessProfiles(): Promise<AccessProfileView[] | null> {
  const response = await fetch('/api/v1/access-profiles', {credentials: 'same-origin'});
  if (response.status === 404 || response.status === 405) return null;
  if (!response.ok) throw await failure(response);
  return response.json() as Promise<AccessProfileView[]>;
}
export interface AuditRecord { time: string; action: string; message?: string }
export interface Session { actor?: string; oidc?: boolean; sandboxControl?: boolean; t3Connect?: boolean; githubOwner?: string; runRecords?: boolean; handoffs?: boolean; orcaProjects?: boolean; orcaSandbox?: string; orcaNamespace?: string; lanHosts?: boolean; lanAllowed?: string[]; downloads?: boolean; downloadMaxBytes?: number; uploads?: boolean; uploadChunkBytes?: number; providers?: string[] }
export interface Repository { full_name: string }

export type EgressState = 'Open' | 'Learning' | 'Locked';
// Summary of a sandbox-control workspace. `egress` fields come from sandbox-control
// releases with instant egress; older releases only report `learning`.
export interface ExistingSandbox {
  id: string; namespace: string; sandbox: string; mode: string; revision: number; status: string;
  learning?: string; learningExpires?: number; pending: number; approvals: number;
  egress?: EgressState | ''; egressExpires?: number; egressControl?: boolean;
}

export async function failure(response: Response): Promise<Error> {
  const text = (await response.text()).trim();
  try {
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed.error === 'string') return new Error(parsed.error);
  } catch { /* plain-text error */ }
  return new Error(text || `${response.status} ${response.statusText}`);
}

export async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch('/api/v1/' + path, {method, credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  if (!response.ok) throw await failure(response);
  return response.json() as Promise<T>;
}

// sandbox-control uses double-submit CSRF: the HttpOnly cookie travels with the
// request and the same token, read from the list response header, goes in the form.
let csrf = '';

export async function existingSandboxes(): Promise<ExistingSandbox[]> {
  const response = await fetch('/sandboxes/workspaces', {credentials: 'same-origin'});
  if (!response.ok) throw await failure(response);
  csrf = response.headers.get('X-Sandbox-CSRF') || csrf;
  return response.json() as Promise<ExistingSandbox[]>;
}

export const egressState = (s: ExistingSandbox): EgressState | undefined => {
  const state = s.egress || s.learning;
  return state === 'Open' || state === 'Learning' || state === 'Locked' ? state : undefined;
};
export const egressExpires = (s: ExistingSandbox) => s.egressExpires || s.learningExpires || 0;
export const egressControllable = (s: ExistingSandbox) => s.egressControl ?? Boolean(s.learning);

export type EgressAction = 'open' | 'lock';

export async function setEgress(id: string, action: EgressAction, minutes: number): Promise<ExistingSandbox> {
  if (!csrf) await existingSandboxes();
  const form = new URLSearchParams({workspace: id, tab: 'access', action: 'learning-' + action, minutes: String(minutes), csrf});
  const response = await fetch('/sandboxes/egress', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/x-www-form-urlencoded', Accept: 'application/json'}, body: form, redirect: 'manual'});
  // A redirect means the backend predates the JSON API and handled the form; refresh to confirm.
  if (response.type === 'opaqueredirect') {
    const list = await existingSandboxes();
    const found = list.find(item => item.id === id);
    if (!found) throw new Error(id + ': sandbox is no longer listed');
    return found;
  }
  if (!response.ok) throw await failure(response);
  if (!response.headers.get('Content-Type')?.startsWith('application/json')) throw new Error(id + ': this sandbox-control release does not support instant egress; open its Access page');
  return response.json() as Promise<ExistingSandbox>;
}

// Native sandbox pages: JSON views of a sandbox-control workspace.
export interface Revision { id: number; status: string; error?: string; actor?: string; at: number }
export interface LearningWindow { phase: EgressState; minutes: number; started: number; expires: number }
export interface ServiceAccess { name: string; host: string; editable: boolean; lanAvailable: boolean; tailscaleAvailable: boolean; grant: {lan: boolean; tailscale: boolean; rulesText: string} }
export interface SandboxDetail {
  id: string; namespace: string; sandbox: string; mode: 'Running' | 'Suspended' | string;
  revision: Revision; learningEgress: boolean; tailnetReady: boolean; serviceOwner: boolean;
  learning?: LearningWindow; providerHosts: string[]; previewPending: number; devLinkPending?: number;
  egressCounts?: {pending: number; approved: number};
  links: {grafana?: string; openlit?: string};
  services: ServiceAccess[]; revisions: Revision[];
}
// One method and URL. fingerprints counts its distinct queries and bodies; older
// sandbox-control releases list each fingerprint on its own and omit the counts.
export interface Observation { host: string; method: string; path: string; scheme: string; port: number; queryHash?: string; package?: {ecosystem: string; name: string; version?: string}; fingerprints?: number; requests?: number; first?: number; last?: number; observation: string; allow?: AllowOptions }
// One-click web rule options for a waiting or blocked request (sandbox-control
// README "Broad web rules"); absent on releases without web rules.
export interface AllowOptions { host: string; method: string; path: string; pattern: string; wildcard: string; wildcardReason: string; githubOwner: string; githubRepo: string; sharedHost: boolean; passthrough: boolean }
// Server-side page of a list (positions from 1); absent on older releases.
export interface Paging { page: number; size: number; pages: number; first: number; last: number }
export interface ObservationList { total: number; items: Observation[]; paging?: Paging }
export interface WebRule {
  id: string; kind: 'path' | 'domain' | 'wildcard' | 'github-repo' | 'github-owner'; host: string; path: string; methods: string[]; anyMethod: boolean; apex: boolean;
  owner: string; repo: string; write: boolean; passthrough: boolean; expires: string; active: boolean; description: string; risks: string[];
  rules: {host: string; path: string; methods: string[]; purpose: string}[]; requests: number; lastUsed: string; actor: string; created: string;
  // Set on rules a sandbox-control sync keeps in step with Git (reference
  // sources); the console cannot remove them.
  source?: string; fromGit?: boolean;
}
export interface RuleEvent { at: string; action: string; rule: string; description: string; by: string; expires: string; detail: string }
export interface PassthroughTool { name: string; problem: string; evidence: string; source: string; alternative: string }
export interface EgressCapacity { waiting: number; approved: number; waitingLimit: number; approvalLimit: number; warnWaiting: boolean; warnApprovals: boolean; rolledOff: number; lastRolledOff: string; oldestWaiting: string; approvalCapHits: number; lastCapHit: string }
export interface ObservationPage {
  filter: string; pending: ObservationList; approved: ObservationList;
  rules?: WebRule[]; ruleHistory?: RuleEvent[]; passthroughTools?: {note: string; tools: PassthroughTool[]}; capacity?: EgressCapacity;
}
export interface Preview {
  id: string; state: string; proposedName: string; proposedRulesText: string; rulesText: string;
  lan: boolean; tailscale: boolean; gapAcknowledged: boolean; seen: number; first: number; count: number;
  listener: {project: string; worktree: string; port: number; pid: number; address: string; loopback: boolean};
  urls: {lan: string; tailnet: string};
}
export interface DevScan {
  ID: string; Status: string; Error: string; Started: number; Finished: number; Root: string;
  Commit: string; Branch: string; Remote: string; Worktree: string; Modified: string[] | null;
  Packages: {Ecosystem: string; Name: string; Version: string; License: string; Approved: boolean}[] | null;
  Vulns: {ID: string; Package: string; Version: string; Fixed: string; Severity: string; Title: string}[] | null;
  Secrets: {File: string; Rule: string; Title: string; Severity: string; Line: number}[] | null;
  Scanner: string; Truncated: boolean; Digest: string;
}
export interface DevLink {
  Name: string; Workspace: string; State: string; URL: string; Host: string; Port: number;
  Cwd: string; Project: string; Worktree: string; Note: string; RulesText: string;
  Scan: DevScan; Approved: boolean; ApprovedDigest: string; Expires: number; Requested: number; Decided: number; Actor: string; Live: boolean;
  Counts: Record<'packages' | 'unapproved' | 'critical' | 'high' | 'medium' | 'low' | 'secrets' | 'modified', number>;
  Events: {Action: string; Actor: string; Detail: string; At: number}[] | null;
  AccessTotal: number; Egress: string; Viewers: string;
}
export interface ActivityItem { title: string; target: string; time: string; outcome?: string; details?: {label: string; value: string}[]; count?: number; first?: string; blocked?: {host: string; method: string; path: string; timestamp: string; reason?: string; token?: string} }
export interface ActivitySource { source: string; scope: string; at: number; error?: string; items: ActivityItem[] }
export interface Activity { window: {start: string; end: string; minutes: number}; links: {grafana?: string; openlit?: string}; sources: ActivitySource[]; health: {error?: string; cards: {title: string; target: string}[]} }

export async function sandboxGet<T>(path: string, params: Record<string, string>): Promise<T> {
  const response = await fetch('/sandboxes' + path + '?' + new URLSearchParams(params), {credentials: 'same-origin', headers: {Accept: 'application/json'}});
  if (!response.ok) throw await failure(response);
  csrf = response.headers.get('X-Sandbox-CSRF') || csrf;
  if (!response.headers.get('Content-Type')?.startsWith('application/json')) throw new Error('This sandbox-control release has no JSON view; open the classic page');
  return response.json() as Promise<T>;
}
// Like sandboxGet, but null when this console or sandbox-control release does not
// serve the view (404 or 405), so a section can say "not available yet".
export async function sandboxGetOptional<T>(path: string, params: Record<string, string>): Promise<T | null> {
  const response = await fetch('/sandboxes' + path + '?' + new URLSearchParams(params), {credentials: 'same-origin', headers: {Accept: 'application/json'}});
  if (response.status === 404 || response.status === 405) return null;
  if (!response.ok) throw await failure(response);
  csrf = response.headers.get('X-Sandbox-CSRF') || csrf;
  if (!response.headers.get('Content-Type')?.startsWith('application/json')) return null;
  return response.json() as Promise<T>;
}
export const sandboxDetail = (id: string) => sandboxGet<SandboxDetail>('/api/workspace', {workspace: id});
// With page and size, releases that page server-side return `paging` per list;
// older ones ignore them and return the newest `limit` rows.
export const sandboxObservations = (id: string, q: string, limit?: number, page?: number, size?: number) => sandboxGet<ObservationPage>('/api/egress', {
  workspace: id, q, ...(limit ? {limit: String(limit)} : {}), ...(page ? {page: String(page)} : {}), ...(size ? {size: String(size)} : {}),
});
export const sandboxPreviews = (id: string, expired: boolean) => sandboxGet<{expiredCount: number; requests: Preview[]}>('/api/previews', expired ? {workspace: id, expired: '1'} : {workspace: id});
export const sandboxDevLinks = (id: string) => sandboxGet<DevLink[]>('/api/devlinks', {workspace: id});
export const sandboxActivity = (id: string, minutes: number) => sandboxGet<Activity>('/api/activity', {workspace: id, minutes: String(minutes)});

// An agent's own report that a denial stops its task. The agent supplies kind,
// resource and reason; the operator reviews and dismisses. Agents approve nothing.
export interface AgentReport { id: number; kind: string; resource: string; reason: string; count: number; first: string; last: string; requests?: ReportRequests }
// A web report's link to the request list: the Internet filter that finds it,
// matching waiting and approved counts, and a note when nothing matches.
export interface ReportRequests { filter: string; waiting: number; approved: number; note: string }
// Null when this sandbox-control release has no agent reports.
export async function sandboxReports(id: string): Promise<AgentReport[] | null> {
  const response = await fetch('/sandboxes/api/reports?' + new URLSearchParams({workspace: id}), {credentials: 'same-origin', headers: {Accept: 'application/json'}});
  if (response.status === 404 || response.status === 405) return null;
  if (!response.ok) throw await failure(response);
  csrf = response.headers.get('X-Sandbox-CSRF') || csrf;
  if (!response.headers.get('Content-Type')?.startsWith('application/json')) return null;
  const body = await response.json() as {reports?: AgentReport[]};
  return body.reports || [];
}

// Projects of the Orca sandbox, through its handoff intake: repositories the
// console published, and 'registered' ones Orca holds without a console record
// (those have no actor or request time, and repo is empty when Orca's remote
// does not name OWNER/NAME).
export type OrcaProjectStatus = 'preparing' | 'published' | 'failed' | 'registered';
export interface OrcaProject { name: string; repo: string; actor?: string; requestedAt?: string; status: OrcaProjectStatus; step?: string; error?: string; clone?: string; repoId?: string; publishedAt?: string }
export const orcaProjects = () => api<OrcaProject[]>('orca-projects').then(list => list || []);
export const orcaProject = (name: string) => api<OrcaProject>('orca-projects/' + encodeURIComponent(name));
export const publishOrcaProject = (repo: string) => api<OrcaProject>('orca-projects', 'POST', {repo});

// Posts a sandbox-control form action that answers with its own JSON body.
export async function sandboxPostJSON<T>(path: string, id: string, fields: Record<string, string>): Promise<T> {
  if (!csrf) await existingSandboxes();
  const form = new URLSearchParams({workspace: id, csrf, ...fields});
  const response = await fetch('/sandboxes' + path, {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/x-www-form-urlencoded', Accept: 'application/json'}, body: form, redirect: 'manual'});
  if (response.type === 'opaqueredirect' || response.status === 404) throw new Error('This sandbox-control release does not support downloads yet');
  if (!response.ok) throw await failure(response);
  return response.json() as Promise<T>;
}

// Sends one raw upload part to sandbox-control. The body is file bytes, so the
// CSRF token travels in X-Sandbox-CSRF (the cookie half still comes along).
export async function sandboxPostBytes<T>(path: string, params: Record<string, string>, body: Uint8Array): Promise<T> {
  if (!csrf) await existingSandboxes();
  const response = await fetch('/sandboxes' + path + '?' + new URLSearchParams(params), {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/octet-stream', Accept: 'application/json', 'X-Sandbox-CSRF': csrf}, body: body as BodyInit, redirect: 'manual'});
  if (response.type === 'opaqueredirect' || response.status === 404) throw new Error('This sandbox-control release does not support uploads yet');
  if (!response.ok) throw await failure(response);
  return response.json() as Promise<T>;
}

// Posts a sandbox-control form action and returns the refreshed summary.
export async function sandboxAction(path: string, id: string, fields: Record<string, string | string[]>): Promise<ExistingSandbox> {
  if (!csrf) await existingSandboxes();
  const form = new URLSearchParams({workspace: id, csrf});
  for (const [key, value] of Object.entries(fields)) for (const v of [value].flat()) form.append(key, v);
  const response = await fetch('/sandboxes' + path, {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/x-www-form-urlencoded', Accept: 'application/json'}, body: form, redirect: 'manual'});
  if (response.type === 'opaqueredirect') throw new Error('This sandbox-control release does not answer in JSON; refresh to confirm the change');
  if (!response.ok) throw await failure(response);
  return response.json() as Promise<ExistingSandbox>;
}
