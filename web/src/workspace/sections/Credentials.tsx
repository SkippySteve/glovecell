import {useEffect, useState} from 'react';
import {GrantTable} from '../../access/GrantTable';
import {classicHref} from '../../sandbox/Sandbox';
import {NotAvailable, SectionIntro} from '../bits';
import {loadCluster, type ClusterGrant, type ClusterView} from '../sources/credentials';
import type {SandboxState} from '../useSandbox';

export function Credentials({ws, now}: {ws: SandboxState; now: number}) {
  const detail = ws.detail!;
  const [cluster, setCluster] = useState<ClusterView | null | undefined>(undefined);
  const [error, setError] = useState('');
  useEffect(() => {
    let live = true;
    loadCluster(detail.id).then(v => { if (live) setCluster(v); }, (e: Error) => { if (live) { setCluster(null); setError(e.message); } });
    return () => { live = false; };
  }, [detail.id]);
  const mine = cluster?.grants.filter(g => g.thisSandbox) || [];
  const hosts = detail.providerHosts.map(host => ({host}));
  return (
    <div className="stack">
      <SectionIntro title="Credentials">What this sandbox can authenticate as. Secret values are never shown or exported.</SectionIntro>
      <GrantTable<{host: string}> title={`Model providers (${hosts.length})`} label="Model provider hosts" now={now} rows={hosts} rowKey={h => h.host}
        intro={<p>Provider hosts are reachable at every access level. Which subscription tokens are mounted has no read API yet.</p>}
        columns={[{key: 'host', label: 'Host', cell: h => <code>{h.host}</code>}]}
        expiry={() => ({note: 'At every level'})}
        empty={{title: 'No provider hosts are configured.', hint: 'Providers come from the sandbox profile in Git.'}}
      />
      <GrantTable<ClusterGrant> title={`Cluster grants (${mine.length})`} label="Cluster grants" testId="cluster-identity" now={now} rows={mine} rowKey={g => g.namespace + g.capability + g.expires} rowLabel={g => g.capability + ' in ' + g.namespace}
        loading={cluster === undefined}
        intro={cluster ? <p>{cluster.identity ? <>Agents use ServiceAccount <code>{cluster.identity.namespace}/{cluster.identity.name}</code>. </> : 'This sandbox\'s pod mounts no Kubernetes token. '}Grants are time-limited and changed on the classic page.</p>
          : cluster === null ? (error ? <p className="error-text">{error}</p> : <NotAvailable what="Cluster identity" needs="This console release does not proxy GET /api/cluster yet." />) : null}
        search={{placeholder: 'namespace', label: 'Search grants', text: g => g.namespace + ' ' + g.capability + ' ' + g.reason}}
        columns={[
          {key: 'what', label: 'Grant', cell: g => <><strong>{g.capability}</strong> in <code>{g.namespace}</code></>},
          {key: 'why', label: 'Reason', cell: g => <>{g.reason}{g.grantedBy ? <span className="cell-sub">granted by {g.grantedBy}</span> : null}</>},
        ]}
        expiry={g => g.expires ? {at: g.expires} : 'never'}
        empty={{title: 'No time-limited cluster grants are active.', hint: 'Grant one on the classic page when a task needs to read or debug a namespace.', action: <a href={classicHref(detail.id, 'cluster')}>Manage cluster grants on the classic page</a>}}
        footer={mine.length ? <a href={classicHref(detail.id, 'cluster')}>Manage cluster grants on the classic page</a> : null}
      />
      <NotAvailable what="Repository grants of shared workspaces" needs="sandbox-control has no read API for them yet. Project sandboxes list theirs on their Credentials page." />
    </div>
  );
}
