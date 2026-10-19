import {useCallback, useEffect, useState} from 'react';
import {sandboxObservations, type Observation, type ObservationPage} from '../api';
import {useData} from '../data';
import {Button, Card, CardHeader, Input, Tag} from '../ui';
import {timeOf, type SandboxContext} from './Sandbox';

const plural = (n: number, word: string) => `${n.toLocaleString()} ${word}${n === 1 ? '' : 's'}`;
const grouped = (o: Observation) => o.fingerprints !== undefined;

function Request({o}: {o: Observation}) {
  const fingerprints = o.fingerprints ?? 1;
  const span = !!o.first && !!o.last && o.first !== o.last;
  const details = [
    o.package ? `${o.package.ecosystem} ${o.package.name} ${o.package.version || ''}`.trim() : '',
    fingerprints > 1 ? plural(fingerprints, 'request fingerprint') : o.queryHash ? 'Request fingerprint ' + o.queryHash.slice(0, 12) : '',
    span ? `First seen ${timeOf(o.first!)}` : '',
    o.last ? `${span ? 'last seen' : 'Seen'} ${timeOf(o.last)}` : '',
  ].filter(Boolean);
  return (
    <div className="item-main">
      <span className="request"><span className="method">{o.method}</span><code>{o.scheme}://{o.host}{o.port === 443 || o.port === 80 ? '' : ':' + o.port}{o.path}</code>{o.requests ? <Tag size="sm" text={plural(o.requests, 'request')} className="inline-tag" /> : null}</span>
      {details.length ? <span className="subtle">{details.join(' · ')}</span> : null}
    </div>
  );
}

export function AccessTab({detail, act}: SandboxContext) {
  const {toast} = useData();
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState('');
  const [page, setPage] = useState<ObservationPage | null>(null);
  const [busy, setBusy] = useState<string>('');
  const load = useCallback(async () => {
    try { setPage(await sandboxObservations(detail.id, filter)); } catch (e) { toast((e as Error).message, true); }
  }, [detail.id, filter, toast]);
  // Reload when the filter changes or the sandbox's counts move.
  useEffect(() => { load(); }, [load, detail.egressCounts?.pending, detail.egressCounts?.approved, detail.learning?.phase]);
  // Pending counts move only when an endpoint first appears; refresh its request counts too.
  useEffect(() => { const t = window.setInterval(load, 30000); return () => window.clearInterval(t); }, [load]);
  useEffect(() => { const t = window.setTimeout(() => setFilter(query.trim()), 300); return () => window.clearTimeout(t); }, [query]);
  // A grouped item decides every fingerprint seen for its method and URL.
  const decide = async (o: Observation, approve: boolean) => {
    setBusy(o.observation);
    const action = (approve ? 'approve-' : 'revoke-') + (grouped(o) ? 'endpoint' : 'learning');
    if (await act('/egress', {action, observation: o.observation}, approve ? 'request approved' : 'request revoked')) await load();
    setBusy('');
  };
  const clear = async () => {
    if (!page || !confirm(`Clear all ${page.pending.total} unapproved requests? They stay blocked; new ones are listed again when seen.`)) return;
    setBusy('clear');
    if (await act('/egress', {action: 'clear-unapproved'}, 'unapproved requests cleared')) await load();
    setBusy('');
  };
  const learning = detail.learning?.phase === 'Learning';
  const shown = (list: ObservationPage['pending']) => list.total > list.items.length ? ` · showing the newest ${list.items.length} of ${list.total}` : '';
  return (
    <div className="stack">
      <div className="observation-filter">
        <div className="field"><label className="field-label" htmlFor="observation-filter">Filter by host or path</label>
          <Input id="observation-filter" value={query} placeholder="api.example.com" autoCapitalize="off" spellCheck={false} onChange={e => setQuery(e.target.value)} /></div>
        {page && page.pending.total && !filter ? <Button variant="secondary" buttonType="destructive" disabled={busy === 'clear'} onClick={clear}>Clear unapproved requests</Button> : null}
      </div>
      {!page ? <p className="empty">Loading requests…</p> : <>
        <Card variant="sunk" width="full" data-testid="pending-requests">
          <CardHeader title={`Awaiting approval (${page.pending.total})`} subtitle={([...page.pending.items, ...page.approved.items].some(grouped) ? 'Requests are grouped by method and URL. Approving allows the request fingerprints seen so far; a new query or body needs approval again' : 'Approving allows this exact method, path and request fingerprint') + shown(page.pending) + '.'} />
          <ul className="items">
            {page.pending.items.map(o => <li key={o.observation}><Request o={o} /><Button size="small" variant="primary" disabled={busy === o.observation} onClick={() => decide(o, true)}>{(o.fingerprints ?? 1) > 1 ? `Approve all ${o.fingerprints!.toLocaleString()}` : 'Approve'}</Button></li>)}
            {!page.pending.items.length ? <li className="empty-item">{filter ? `No requests awaiting approval match “${filter}”.` : 'No requests awaiting approval.'}</li> : null}
          </ul>
        </Card>
        <Card variant="sunk" width="full" data-testid="approved-requests">
          <CardHeader title={`Approved (${page.approved.total})`} subtitle={(learning ? 'Revoking is available once the learning window ends' : 'Revoke anything the sandbox should no longer reach') + shown(page.approved) + '.'} />
          <ul className="items">
            {page.approved.items.map(o => <li key={o.observation}><Request o={o} /><Button size="small" variant="secondary" buttonType="destructive" disabled={learning || busy === o.observation} onClick={() => decide(o, false)}>Revoke</Button></li>)}
            {!page.approved.items.length ? <li className="empty-item">{filter ? `No approved requests match “${filter}”.` : 'No approved requests yet.'}</li> : null}
          </ul>
        </Card>
        {detail.providerHosts.length ? <p className="subtle">Provider hosts are always reachable: {detail.providerHosts.map(h => <Tag key={h} size="sm" text={h} className="inline-tag" />)}</p> : null}
      </>}
    </div>
  );
}
