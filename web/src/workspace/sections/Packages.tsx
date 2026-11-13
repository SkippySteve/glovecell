import {useCallback, useEffect, useState} from 'react';
import {sandboxAction} from '../../api';
import {GrantTable} from '../../access/GrantTable';
import {useData} from '../../data';
import {classicHref, timeOf} from '../../sandbox/Sandbox';
import {Button} from '../../ui';
import {NotAvailable, SectionIntro} from '../bits';
import {useConfirm} from '../Confirm';
import {decideRequest, loadRequests, requestUrl, type Requests} from '../sources/internet';
import {ecosystemLabel, packageRequests, type PackageRequest} from '../sources/packages';
import {useNow} from '../time';
import type {SandboxState} from '../useSandbox';

const columns = [
  {key: 'package', label: 'Package', cell: (p: PackageRequest) => <>{ecosystemLabel(p.ecosystem)} <strong>{p.name}</strong> {p.version}</>},
  {key: 'request', label: 'Request', cell: (p: PackageRequest) => <code>{p.request.method} {requestUrl(p.request)}</code>},
  {key: 'seen', label: 'Last seen', cell: (p: PackageRequest) => p.request.last ? timeOf(p.request.last) : ''},
];
const ecosystems = (rows: PackageRequest[]) => [...new Set(rows.map(p => p.ecosystem))].map(e => ({key: e, label: ecosystemLabel(e), test: (p: PackageRequest) => p.ecosystem === e}));

export function Packages({ws}: {ws: SandboxState}) {
  const detail = ws.detail!;
  const {toast} = useData();
  const ask = useConfirm();
  const now = useNow(30000);
  const [requests, setRequests] = useState<Requests | null>(null);
  const load = useCallback(async () => { if (detail.learningEgress) try { setRequests(await loadRequests(detail.id, '', 1, 100)); } catch { setRequests(null); } }, [detail.id, detail.learningEgress]);
  useEffect(() => { load(); }, [load, detail.egressCounts?.pending, detail.egressCounts?.approved]);
  const {pending, approved} = packageRequests(requests);
  const decide = async (rows: PackageRequest[], approve: boolean) => {
    if (!approve && !await ask(rows.length === 1 ? `Revoke ${rows[0].name}?` : `Revoke ${rows.length} package downloads?`, <p>The sandbox can no longer download {rows.length === 1 ? 'it' : 'them'} until approved again.</p>, 'Revoke')) return;
    let done = 0;
    for (const p of rows) {
      const f = decideRequest(p.request, approve);
      try { await sandboxAction(f.path, detail.id, f.fields); done++; } catch (e) { toast(`${detail.id}: ${done} of ${rows.length} done, then: ${(e as Error).message}`, true); break; }
    }
    if (done === rows.length) toast(`${detail.id}: ${done} package downloads ${approve ? 'approved' : 'revoked'}`);
    await Promise.all([ws.reload(), load()]);
  };
  return (
    <div className="stack">
      <SectionIntro title="Packages">Only approved registry downloads can be fetched. A name without a version allows all its versions; dependencies need their own approvals. The Standard level allows the common registries as a whole.</SectionIntro>
      {detail.learningEgress ? <>
        <GrantTable<PackageRequest> title={`Awaiting approval (${pending.length})`} label="Package downloads awaiting approval" testId="package-requests" now={now} loading={!requests}
          rows={pending} rowKey={p => p.request.observation} rowLabel={p => p.name + ' ' + p.version}
          intro={<p>Web requests the proxy recognised as package downloads. Approving allows that request, as on the Internet page.</p>}
          search={{placeholder: 'left-pad', label: 'Search packages', text: p => p.name + ' ' + p.version + ' ' + p.ecosystem}}
          filters={ecosystems(pending)}
          bulk={[{key: 'approve', label: 'Approve selected', run: rows => decide(rows, true)}]}
          columns={columns}
          actions={p => <Button size="small" variant="primary" onClick={() => decide([p], true)}>Approve</Button>}
          empty={{title: 'No blocked package downloads.', hint: 'Downloads the sandbox tries and the proxy blocks appear here.'}}
        />
        <GrantTable<PackageRequest> title={`Approved (${approved.length})`} label="Approved package downloads" now={now} loading={!requests}
          rows={approved} rowKey={p => p.request.observation} rowLabel={p => p.name + ' ' + p.version}
          search={{placeholder: 'left-pad', label: 'Search packages', text: p => p.name + ' ' + p.version + ' ' + p.ecosystem}}
          filters={ecosystems(approved)}
          bulk={[{key: 'revoke', label: 'Revoke selected', danger: true, applies: () => detail.learning?.phase !== 'Learning', run: rows => decide(rows, false)}]}
          columns={columns}
          expiry={() => ({note: 'Until revoked'})}
          empty={{title: 'No package downloads approved yet.', hint: 'Approve one above, or choose the Standard level on the Internet page.'}}
          footer={approved.length >= 100 ? <span className="subtle">The newest 100; all approved requests are on the Internet page.</span> : null}
        />
      </> : null}
      <NotAvailable what="Allowed package lists and package-policy blocks" needs="The console has no read API for them yet (sandbox-control would need GET /api/packages).">
        <a href={classicHref(detail.id, 'access')}>Edit package lists or import a package-lock.json on the classic page</a>
      </NotAvailable>
    </div>
  );
}
