// Credentials section: data source.
//
// Today: provider hosts from GET /api/workspace and the cluster identity and its
// time-limited grants from sandbox-control GET /api/cluster (read only; granting
// stays on the classic page because POST /cluster-access is not proxied).
// Mounted model-provider credentials and Forgejo repository grants of shared
// workspaces have no read API yet.
import {sandboxGetOptional} from '../../api';

export interface ClusterGrant { namespace: string; capability: string; reason: string; grantedBy: string; expires: number; thisSandbox: boolean }
export interface ClusterView { identity: {namespace: string; name: string} | null; grants: ClusterGrant[] }

const str = (v: unknown) => typeof v === 'string' ? v : '';

// /api/cluster is Go's default JSON encoding of ClusterAccess (field names as
// declared: Identity, Grants, ...). Only fields shown here are read.
export function toClusterView(raw: unknown): ClusterView {
  const b = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>;
  const id = b.Identity && typeof b.Identity === 'object' ? b.Identity as Record<string, unknown> : null;
  const grants = (Array.isArray(b.Grants) ? b.Grants : []).filter(g => g && typeof g === 'object').map(g => {
    const r = g as Record<string, unknown>;
    const expires = Date.parse(str(r.Expires));
    return {namespace: str(r.Namespace), capability: str(r.Capability), reason: str(r.Reason), grantedBy: str(r.GrantedBy), expires: Number.isFinite(expires) ? expires : 0, thisSandbox: r.ThisSandbox === true};
  });
  return {identity: id && str(id.Name) ? {namespace: str(id.Namespace), name: str(id.Name)} : null, grants};
}

export async function loadCluster(workspace: string): Promise<ClusterView | null> {
  const raw = await sandboxGetOptional<unknown>('/api/cluster', {workspace});
  return raw === null ? null : toClusterView(raw);
}
