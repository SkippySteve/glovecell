import {useEffect, useState} from 'react';
import {classicHref} from '../../sandbox/Sandbox';
import {Card, CardHeader, Tag} from '../../ui';
import {Countdown, NotAvailable, SectionIntro} from '../bits';
import {loadCluster, type ClusterView} from '../sources/credentials';
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
  return (
    <div className="stack">
      <SectionIntro title="Credentials">What this sandbox can authenticate as. Secret values are never shown or exported.</SectionIntro>
      <Card variant="sunk" width="full">
        <CardHeader title="Model providers" subtitle="Provider hosts are always reachable, in every web access state." />
        {detail.providerHosts.length ? <div className="chip-list">{detail.providerHosts.map(h => <code key={h}>{h}</code>)}</div> : <p className="muted">No provider hosts are configured.</p>}
        <NotAvailable what="Mounted provider credentials" needs="Which subscription tokens are mounted has no read API yet." />
      </Card>
      <Card variant="sunk" width="full">
        <CardHeader title="Forgejo repositories" />
        <NotAvailable what="Repository grants of shared workspaces" needs="sandbox-control has no read API for them yet. Project sandboxes list theirs on their Credentials page." />
      </Card>
      <Card variant="sunk" width="full" data-testid="cluster-identity">
        <CardHeader title="Cluster identity" subtitle="Time-limited Kubernetes grants to this sandbox's ServiceAccount." />
        {cluster === undefined ? <p className="muted">Loading…</p> : cluster === null ? (
          error ? <p className="error-text">{error}</p> : <NotAvailable what="Cluster identity" needs="This console release does not proxy GET /api/cluster yet." />
        ) : <>
          <p>{cluster.identity ? <>Agents use ServiceAccount <code>{cluster.identity.namespace}/{cluster.identity.name}</code>.</> : 'This sandbox\'s pod mounts no Kubernetes token.'}</p>
          {mine.length ? <ul className="items">{mine.map(g => (
            <li key={g.namespace + g.capability + g.expires}><div className="item-main"><strong>{g.capability} in {g.namespace}</strong><span className="subtle-text">{g.reason}{g.grantedBy ? ' · granted by ' + g.grantedBy : ''}</span></div>
              {g.expires ? <Tag size="sm" variant="warning" text={<>ends in <Countdown until={g.expires} now={now} /></>} /> : null}</li>
          ))}</ul> : <p className="muted">No time-limited cluster grants are active.</p>}
        </>}
        <p><a href={classicHref(detail.id, 'cluster')}>Manage cluster grants on the classic page</a></p>
      </Card>
    </div>
  );
}
