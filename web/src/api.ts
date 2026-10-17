// Typed clients for the console API (/api/v1) and the mounted sandbox-control service (/sandboxes).

export type Method = 'GET' | 'HEAD' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'OPTIONS';
export interface HTTPSRule { host: string; methods: Method[]; paths: string[] }
export interface ProjectRepository { name: string; host: string; repository: string; public?: boolean }
export interface Condition { type: string; status: string; reason: string; message: string; observedGeneration?: number }
export interface Project {
  metadata: { name: string; uid?: string; generation?: number; resourceVersion?: string };
  spec: { repository: string; desiredState: 'Running' | 'Paused' | 'Removed'; https?: HTTPSRule[]; repositories?: ProjectRepository[] };
  status?: { phase?: string; namespace?: string; observedGeneration?: number; conditions?: Condition[] };
}
export interface AuditRecord { time: string; action: string; message?: string }
export interface Session { actor?: string; oidc?: boolean; sandboxControl?: boolean; t3Connect?: boolean; githubOwner?: string }
export interface Repository { full_name: string }

export type EgressState = 'Open' | 'Learning' | 'Locked';
// Summary of a sandbox-control workspace. `egress` fields come from sandbox-control
// releases with instant egress; older releases only report `learning`.
export interface ExistingSandbox {
  id: string; namespace: string; sandbox: string; mode: string; revision: number; status: string;
  learning?: string; learningExpires?: number; pending: number; approvals: number;
  egress?: EgressState | ''; egressExpires?: number; egressControl?: boolean;
}

async function failure(response: Response): Promise<Error> {
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
