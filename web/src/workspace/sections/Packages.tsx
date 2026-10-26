import {useCallback, useEffect, useState, type ReactNode} from 'react';
import {classicHref, timeOf} from '../../sandbox/Sandbox';
import {Button, Card, CardHeader} from '../../ui';
import {NotAvailable, SectionIntro} from '../bits';
import {decideRequest, loadRequests, requestUrl, type Requests} from '../sources/internet';
import {ecosystemLabel, packageRequests, type PackageRequest} from '../sources/packages';
import type {SandboxState} from '../useSandbox';

function Row({p, action}: {p: PackageRequest; action?: ReactNode}) {
  return (
    <li>
      <div className="item-main"><span>{ecosystemLabel(p.ecosystem)} <strong>{p.name}</strong> {p.version}</span>
        <span className="subtle-text"><code>{p.request.method} {requestUrl(p.request)}</code>{p.request.last ? ' · ' + timeOf(p.request.last) : ''}</span></div>
      {action}
    </li>
  );
}

export function Packages({ws}: {ws: SandboxState}) {
  const detail = ws.detail!;
  const [requests, setRequests] = useState<Requests | null>(null);
  const [busy, setBusy] = useState('');
  const load = useCallback(async () => { if (detail.learningEgress) try { setRequests(await loadRequests(detail.id, '')); } catch { setRequests(null); } }, [detail.id, detail.learningEgress]);
  useEffect(() => { load(); }, [load, detail.egressCounts?.pending, detail.egressCounts?.approved]);
  const {pending, approved} = packageRequests(requests);
  const approve = async (p: PackageRequest) => {
    setBusy(p.request.observation);
    const f = decideRequest(p.request, true);
    if (await ws.act(f.path, f.fields, `${p.name} download approved`)) await load();
    setBusy('');
  };
  return (
    <div className="stack">
      <SectionIntro title="Packages">Only approved registry downloads can be fetched. A name without a version allows all its versions; dependencies need their own approvals.</SectionIntro>
      {detail.learningEgress ? <>
        <Card variant="sunk" width="full" data-testid="package-requests">
          <CardHeader title={`Registry downloads awaiting approval (${pending.length})`} subtitle="Web requests the proxy recognised as package downloads. Approving allows that request, as on the Internet page." />
          <ul className="items">
            {pending.map(p => <Row key={p.request.observation} p={p} action={<Button size="small" variant="primary" disabled={busy === p.request.observation} onClick={() => approve(p)}>Approve download</Button>} />)}
            {!pending.length ? <li className="empty-item">{requests ? 'No blocked package downloads.' : 'Loading…'}</li> : null}
          </ul>
        </Card>
        {approved.length ? <Card variant="sunk" width="full"><CardHeader title={`Approved downloads (${approved.length})`} /><ul className="items">{approved.map(p => <Row key={p.request.observation} p={p} />)}</ul></Card> : null}
      </> : null}
      <NotAvailable what="Allowed package lists and package-policy blocks" needs="The console has no read API for them yet (sandbox-control would need GET /api/packages).">
        <a href={classicHref(detail.id, 'access')}>Edit package lists or import a package-lock.json on the classic page</a>
      </NotAvailable>
    </div>
  );
}
