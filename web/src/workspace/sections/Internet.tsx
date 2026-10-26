import {useCallback, useEffect, useState} from 'react';
import type {Observation} from '../../api';
import {useData} from '../../data';
import {classicHref, timeOf} from '../../sandbox/Sandbox';
import {Button, Card, CardHeader, Input, Tabs, TabsList, TabsTrigger, Tag, ToggleGroup, ToggleItem} from '../../ui';
import {Countdown, plural, SectionIntro, WindowBar} from '../bits';
import {clearUnapproved, decideRequest, loadRequests, paginate, requestUrl, WEB_WINDOWS, webAccess, webLearn, webLock, webOpen, webStartFresh, type Requests} from '../sources/internet';
import {clockAt} from '../time';
import type {SandboxState} from '../useSandbox';

const PHASES: [string, string][] = [['Locked', 'Approved requests only'], ['Learning', 'Allowed and recorded as approvals'], ['Open', 'Allowed, nothing learned']];

function WebAccessCard({ws, now}: {ws: SandboxState; now: number}) {
  const detail = ws.detail!, web = webAccess(detail)!;
  const [minutes, setMinutes] = useState(String(WEB_WINDOWS.some(([m]) => m === web.minutes) ? web.minutes : 60));
  const [busy, setBusy] = useState(false);
  const run = async (form: {path: string; fields: Record<string, string>}, done: string) => { setBusy(true); await ws.act(form.path, form.fields, done); setBusy(false); };
  const m = Number(minutes), phase = web.phase, open = phase !== 'Locked';
  const description = phase === 'Open' ? `Open to the public web until ${clockAt(web.expires)}. Nothing is learned; locking removes this access.`
    : phase === 'Learning' ? `Learning until ${clockAt(web.expires)}. Every request made now becomes an approval.`
    : 'Locked to approved requests and provider hosts.';
  return (
    <section className={'card web-card' + (open ? ' is-open' : '')} aria-labelledby="web-access-title">
      <div className="card-hd"><h2 id="web-access-title">Web access</h2>{open ? <span className="big-countdown" aria-label="Time left"><Countdown until={web.expires} now={now} /></span> : null}</div>
      <div className="card-bd">
        <ol className="phases" aria-label="Web access states">
          {PHASES.map(([k, d]) => <li key={k} className={phase === k ? 'on ' + k : undefined} aria-current={phase === k ? 'step' : undefined}><strong>{k}</strong>{d}</li>)}
        </ol>
        {open ? <WindowBar until={web.expires} total={web.expires - web.started} now={now} tone={phase === 'Open' ? 'bad' : 'warn'} label="Share of the window left" /> : null}
        <p><strong data-testid="sandbox-egress">{description}</strong> <span className="muted">Private networks, cluster services and cloud metadata stay blocked in every state. Changes apply on the next request.</span></p>
        <div className="field"><span className="field-label" id="web-window">Window</span>
          <ToggleGroup type="single" value={minutes} onValueChange={v => v && setMinutes(v)} aria-labelledby="web-window" className="durations">
            {WEB_WINDOWS.map(([v, l]) => <ToggleItem key={v} value={String(v)} aria-label={'Window ' + l}>{l}</ToggleItem>)}
          </ToggleGroup></div>
      </div>
      <div className="card-ft">
        <Button variant="primary" disabled={busy} onClick={() => run(webOpen(m), 'web access opened')}>{phase === 'Open' ? 'Restart open window' : 'Open'}</Button>
        <Button variant="secondary" disabled={busy} onClick={() => run(webLearn(m), 'learning window started')}>{phase === 'Learning' ? 'Restart learning' : 'Learn for this window'}</Button>
        <Button variant="secondary" buttonType="destructive" disabled={busy || !open} onClick={() => run(webLock(m), 'web access locked')}>Lock</Button>
        <span className="end"><Button variant="tertiary" size="small" buttonType="destructive" disabled={busy}
          onClick={() => confirm('Start a fresh learning window? Every recorded request, approved or not, is deleted first.') && run(webStartFresh(m), 'fresh learning window started')}>Start fresh…</Button></span>
      </div>
    </section>
  );
}

function RequestRow({o}: {o: Observation}) {
  const fingerprints = o.fingerprints ?? 1;
  const details = [
    o.package ? `${o.package.ecosystem} ${o.package.name} ${o.package.version || ''}`.trim() : '',
    o.requests ? plural(o.requests, 'request') : '',
    fingerprints > 1 ? plural(fingerprints, 'request fingerprint') : '',
    o.first && o.last && o.first !== o.last ? `first seen ${timeOf(o.first)}` : '',
    o.last ? `last seen ${timeOf(o.last)}` : '',
  ].filter(Boolean);
  return (
    <div className="item-main">
      <span className="request"><span className="method">{o.method}</span><code>{requestUrl(o)}</code></span>
      {details.length ? <span className="subtle-text">{details.join(' · ')}</span> : null}
    </div>
  );
}

function RequestLists({ws}: {ws: SandboxState}) {
  const {toast} = useData();
  const detail = ws.detail!;
  const [tab, setTab] = useState<'pending' | 'approved'>('pending');
  const [query, setQuery] = useState(''), [filter, setFilter] = useState('');
  const [page, setPage] = useState(0);
  const [data, setData] = useState<Requests | null>(null);
  const [busy, setBusy] = useState('');
  const load = useCallback(async () => { try { setData(await loadRequests(detail.id, filter)); } catch (e) { toast((e as Error).message, true); } }, [detail.id, filter, toast]);
  useEffect(() => { load(); }, [load, detail.egressCounts?.pending, detail.egressCounts?.approved, detail.learning?.phase]);
  useEffect(() => { const t = window.setInterval(load, 30000); return () => window.clearInterval(t); }, [load]);
  useEffect(() => { const t = window.setTimeout(() => { setFilter(query.trim()); setPage(0); }, 300); return () => window.clearTimeout(t); }, [query]);
  const learning = detail.learning?.phase === 'Learning';
  const decide = async (o: Observation, approve: boolean) => {
    setBusy(o.observation);
    const f = decideRequest(o, approve);
    if (await ws.act(f.path, f.fields, approve ? 'request approved' : 'request revoked')) await load();
    setBusy('');
  };
  const clear = async () => {
    if (!data || !confirm(`Clear all ${data.pending.total} unapproved requests? They stay blocked; new ones are listed again when seen.`)) return;
    setBusy('clear');
    const f = clearUnapproved();
    if (await ws.act(f.path, f.fields, 'unapproved requests cleared')) await load();
    setBusy('');
  };
  const current = data ? data[tab] : null;
  const shown = current ? paginate(current.items, page) : null;
  return (
    <section className="card" aria-label="Web requests" data-testid={tab === 'pending' ? 'pending-requests' : 'approved-requests'}>
      <Tabs value={tab} onValueChange={v => { setTab(v as 'pending' | 'approved'); setPage(0); }} className="inner-tabs">
        <TabsList aria-label="Requests">
          <TabsTrigger value="pending" text={`Awaiting approval (${data?.pending.total ?? detail.egressCounts?.pending ?? 0})`} />
          <TabsTrigger value="approved" text={`Approved (${data?.approved.total ?? detail.egressCounts?.approved ?? 0})`} />
        </TabsList>
      </Tabs>
      <div className="card-hd">
        <div className="field grow"><label className="field-label" htmlFor="observation-filter">Filter by host or path</label>
          <Input id="observation-filter" value={query} placeholder="api.example.com" autoCapitalize="off" spellCheck={false} onChange={e => setQuery(e.target.value)} /></div>
        {tab === 'pending' && data?.pending.total && !filter ? <Button variant="secondary" buttonType="destructive" size="small" disabled={busy === 'clear'} onClick={clear}>Clear unapproved requests</Button> : null}
      </div>
      {!shown || !current ? <p className="empty pad">Loading requests…</p> : (
        <ul className="items rows" aria-label={tab === 'pending' ? 'Requests awaiting approval' : 'Approved requests'}>
          {shown.rows.map(o => (
            <li key={o.observation}>
              <RequestRow o={o} />
              {tab === 'pending'
                ? <Button size="small" variant="primary" disabled={busy === o.observation} onClick={() => decide(o, true)}>{(o.fingerprints ?? 1) > 1 ? `Approve all ${o.fingerprints!.toLocaleString()}` : 'Approve'}</Button>
                : <Button size="small" variant="secondary" buttonType="destructive" disabled={learning || busy === o.observation} title={learning ? 'Revoking is available once the learning window ends' : undefined} onClick={() => decide(o, false)}>Revoke</Button>}
            </li>
          ))}
          {!shown.rows.length ? <li className="empty-item">{filter ? `Nothing matches “${filter}”.` : tab === 'pending' ? 'No requests awaiting approval.' : 'No approved requests yet.'}</li> : null}
        </ul>
      )}
      {shown && current ? (
        <div className="pager">
          <Button size="small" variant="secondary" disabled={shown.page === 0} onClick={() => setPage(shown.page - 1)}>Previous</Button>
          <span className="subtle-text" aria-live="polite">{current.items.length ? `${shown.from}–${shown.to} of ${current.items.length}` : '0'}{current.truncated ? ` (newest ${current.items.length} of ${current.total.toLocaleString()})` : ''}</span>
          <Button size="small" variant="secondary" disabled={shown.page >= shown.pages - 1} onClick={() => setPage(shown.page + 1)}>Next</Button>
          <span className="end subtle-text">{tab === 'approved' && learning ? 'Revoking is available once the learning window ends.' : 'Grouped by method and URL; a new query or body needs approval again.'}</span>
        </div>
      ) : null}
    </section>
  );
}

export function Internet({ws, now}: {ws: SandboxState; now: number}) {
  const detail = ws.detail!;
  if (!webAccess(detail)) return (
    <div className="stack">
      <SectionIntro title="Internet">This sandbox uses fixed policy rules. Instant open and lock are available for sandboxes with learning egress.</SectionIntro>
      <Card variant="sunk" width="full"><CardHeader title="Fixed rules" subtitle="The console has no read API for this sandbox's rules yet." /><p><a href={classicHref(detail.id, 'access')}>View and edit the rules on the classic page</a></p></Card>
    </div>
  );
  return (
    <div className="stack">
      <WebAccessCard key={detail.id} ws={ws} now={now} />
      <RequestLists key={'r' + detail.id} ws={ws} />
      {detail.providerHosts.length ? <p className="subtle-text">Provider hosts are always reachable: {detail.providerHosts.map(h => <Tag key={h} size="sm" text={h} className="inline-tag" />)}</p> : null}
    </div>
  );
}
