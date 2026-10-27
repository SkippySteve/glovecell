import {useCallback, useEffect, useState} from 'react';
import type {AllowOptions, EgressCapacity, Observation, WebRule} from '../../api';
import {useData} from '../../data';
import {classicHref, timeOf} from '../../sandbox/Sandbox';
import {Button, Callout, Card, CardHeader, Input, Tabs, TabsList, TabsTrigger, Tag, ToggleGroup, ToggleItem} from '../../ui';
import {Countdown, plural, SectionIntro, WindowBar} from '../bits';
import {addRule, clearUnapproved, decideRequest, DEFAULT_PAGE_SIZE, dismissOlder, draftFor, draftRisks, EXPIRIES, loadRequests, METHODS, needsConfirm, PAGE_SIZES, pageKeeping, pageOf, pageSize, passthroughAllowed, removeRule, requestUrl, WEB_WINDOWS, webAccess, webLearn, webLock, webOpen, webStartFresh, type Requests, type RuleDraft, type RuleKind} from '../sources/internet';
import {reportTime} from '../sources/reports';
import {hashQuery, replaceHashQuery} from '../../router';
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

// Per-page choice: the URL query first (shared links), then this browser.
const SIZE_KEY = 'console.internet.pageSize';
function initialSize(): number {
  const fromUrl = hashQuery().get('size');
  if (fromUrl) return pageSize(fromUrl);
  try { return pageSize(localStorage.getItem(SIZE_KEY)); } catch { return DEFAULT_PAGE_SIZE; }
}
const initialPage = () => Math.max(1, Number(hashQuery().get('page')) || 1);

// "More ways to allow": one web rule instead of one exact request. Options
// come from sandbox-control (`allow`); the server validates every field again.
function AllowPanel({ws, allow, free, onDone}: {ws: SandboxState; allow?: AllowOptions; free?: boolean; onDone: () => void}) {
  const [d, setD] = useState<RuleDraft>(() => draftFor(allow));
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<RuleDraft>) => setD(old => ({...old, ...patch}));
  const isGitHub = d.kind === 'github-repo' || d.kind === 'github-owner';
  const base = d.wildcard.replace(/^\*\./, '');
  const kinds: [RuleKind, string, boolean][] = [
    ['path', free ? 'Path pattern' : `This path pattern on ${d.host}`, true],
    ['domain', free ? 'Whole domain' : `The whole domain ${d.host}`, true],
    ['wildcard', free ? 'Every subdomain (*.example.com)' : `Every subdomain: ${d.wildcard}`, Boolean(free || allow?.wildcard)],
    ['github-repo', free ? 'GitHub project OWNER/REPO' : `GitHub project ${d.github}`, Boolean(free || allow?.githubOwner)],
    ['github-owner', free ? 'Every repository of a GitHub owner' : `Every GitHub repository of ${allow?.githubOwner}`, Boolean(free || allow?.githubOwner)],
  ];
  const submit = async () => {
    setBusy(true);
    const f = addRule(d);
    if (await ws.act(f.path, f.fields, 'web rule added')) onDone();
    setBusy(false);
  };
  const risks = draftRisks(d);
  return (
    <div className="allow-panel" role="group" aria-label="More ways to allow">
      <fieldset><legend>Allow</legend>
        {kinds.filter(([, , shown]) => shown).map(([k, label]) => (
          <label key={k} className="check"><input type="radio" name="rule-kind" checked={d.kind === k} onChange={() => set({kind: k, passthrough: false, write: false})} /> {label}</label>
        ))}
        {allow && !allow.wildcard && allow.wildcardReason ? <small className="subtle-text">No wildcard option: {allow.wildcardReason}</small> : null}
        {(free && (d.kind === 'path' || d.kind === 'domain')) ? <label className="field"><span className="field-label">Host</span><Input value={d.host} placeholder="api.example.com" autoCapitalize="off" spellCheck={false} onChange={e => set({host: e.target.value})} /></label> : null}
        {d.kind === 'path' ? <label className="field"><span className="field-label">Pattern: * is one path segment, ** any number; no * means exactly that path</span><Input value={d.path} autoCapitalize="off" spellCheck={false} onChange={e => set({path: e.target.value})} /></label> : null}
        {d.kind === 'wildcard' ? <>
          {free ? <label className="field"><span className="field-label">Wildcard</span><Input value={d.wildcard} placeholder="*.example.com" autoCapitalize="off" spellCheck={false} onChange={e => set({wildcard: e.target.value})} /></label> : null}
          <label className="check"><input type="checkbox" checked={d.apex} onChange={e => set({apex: e.target.checked})} /> and {base || 'the base domain'} itself (otherwise subdomains only)</label>
          <small className="subtle-text">Public suffixes (*.com) and shared hosting (*.github.io, *.vercel.app, *.amazonaws.com, ngrok, …) are refused.</small>
        </> : null}
        {isGitHub ? <>
          {free ? <label className="field"><span className="field-label">GitHub project</span><Input value={d.github} placeholder="OWNER/REPO, or OWNER" autoCapitalize="off" spellCheck={false} onChange={e => set({github: e.target.value})} /></label> : null}
          <small className="subtle-text">Read covers clone and fetch, the repository's API, raw files, archives and release downloads, for that repository only.</small>
          <label className="check"><input type="checkbox" checked={d.write} onChange={e => set({write: e.target.checked})} /> Write: git push and API changes</label>
        </> : null}
      </fieldset>
      {!isGitHub ? <fieldset><legend>Methods</legend>
        <div className="check-row">
          {METHODS.map(m => <label key={m} className="check"><input type="checkbox" disabled={d.anyMethod || d.passthrough} checked={d.methods.includes(m)} onChange={e => set({methods: e.target.checked ? [...d.methods, m] : d.methods.filter(x => x !== m)})} /> {m}</label>)}
          <label className="check"><input type="checkbox" disabled={d.passthrough} checked={d.anyMethod || d.passthrough} onChange={e => set({anyMethod: e.target.checked})} /> Any method</label>
        </div>
      </fieldset> : null}
      {passthroughAllowed(d) && (free || allow?.passthrough) ? (
        <label className="check"><input type="checkbox" checked={d.passthrough} onChange={e => set({passthrough: e.target.checked})} /> Pass through without inspection (the proxy sees only the domain, not paths, methods or content)</label>
      ) : null}
      {d.passthrough ? <small className="subtle-text">Last resort: first make the client trust the sandbox CA. See “Clients that may need pass-through” under Web rules.</small> : null}
      {risks.length ? <ul className="risks">{risks.map(r => <li key={r}>{r}</li>)}</ul> : null}
      <div className="actions">
        <label className="field inline"><span className="field-label">Expires</span>
          <select className="select" value={d.expires} onChange={e => set({expires: e.target.value as RuleDraft['expires']})}>{EXPIRIES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select></label>
        {needsConfirm(d) ? <label className="check"><input type="checkbox" checked={d.confirm} onChange={e => set({confirm: e.target.checked})} /> I understand what this allows</label> : null}
        <Button size="small" variant="primary" disabled={busy || (needsConfirm(d) && !d.confirm)} onClick={submit}>Allow</Button>
        <Button size="small" variant="tertiary" onClick={onDone}>Cancel</Button>
      </div>
    </div>
  );
}

function CapacityBanner({c, filter, busy, clear, dismiss}: {c: EgressCapacity; filter: string; busy: boolean; clear: () => void; dismiss: (days: number) => void}) {
  const [days, setDays] = useState(7);
  if (!c.warnWaiting && !c.warnApprovals && !c.rolledOff && !c.approvalCapHits) return null;
  const parts = [
    c.rolledOff ? `${c.rolledOff.toLocaleString()} older waiting requests rolled off (most recently ${reportTime(c.lastRolledOff)}); the oldest still kept was last seen ${reportTime(c.oldestWaiting)}.` : '',
    c.warnWaiting ? `Waiting: ${c.waiting.toLocaleString()} of ${c.waitingLimit.toLocaleString()}. New requests are always listed; the ones seen longest ago roll off.` : '',
    c.warnApprovals ? `Approved: ${c.approved.toLocaleString()} of ${c.approvalLimit.toLocaleString()}. At the cap new requests are only listed as waiting; revoke unused approvals or replace many with one web rule.` : '',
  ].filter(Boolean);
  return (
    <div className="capacity" role="status">
      <Callout variant="warning" title="The request list is nearly full" description={parts.join(' ')} />
      {!filter ? <div className="actions">
        <Button size="small" variant="secondary" buttonType="destructive" disabled={busy} onClick={clear}>Clear unapproved requests</Button>
        <label className="field inline"><span className="field-label">Dismiss requests not seen for</span>
          <select className="select" value={days} onChange={e => setDays(Number(e.target.value))}>{[1, 7, 30].map(n => <option key={n} value={n}>{plural(n, 'day')}</option>)}</select></label>
        <Button size="small" variant="secondary" disabled={busy} onClick={() => dismiss(days)}>Dismiss old requests</Button>
      </div> : null}
    </div>
  );
}

function WebRules({ws, data}: {ws: SandboxState; data: Requests}) {
  const [busy, setBusy] = useState('');
  const [adding, setAdding] = useState(false);
  const rules = data.rules || [];
  const remove = async (r: WebRule) => {
    if (!confirm(`Remove the rule: ${r.description}?`)) return;
    setBusy(r.id);
    const f = removeRule(r);
    await ws.act(f.path, f.fields, 'web rule removed');
    setBusy('');
  };
  return (
    <section className="card" aria-labelledby="web-rules-title" data-testid="web-rules">
      <div className="card-hd"><h2 id="web-rules-title">Web rules ({rules.length})</h2><span className="end"><Button size="small" variant="secondary" onClick={() => setAdding(!adding)}>{adding ? 'Close' : 'Add a web rule'}</Button></span></div>
      <div className="card-bd">
        <p className="subtle-text">Broad approvals: a path pattern, a whole domain, every subdomain, or a GitHub project. Each is one entry; removing it removes everything it allows. Private networks and cluster services stay blocked whatever a rule says. Every change is a policy revision.</p>
        {adding ? <AllowPanel ws={ws} free onDone={() => setAdding(false)} /> : null}
      </div>
      <ul className="items rows" aria-label="Web rules">
        {rules.map(r => (
          <li key={r.id}>
            <div className="item-main">
              <span className="request">{r.passthrough ? <Tag size="sm" variant="warning" text="uninspected" /> : null}{!r.active ? <Tag size="sm" text="expired" /> : null} <strong>{r.description}</strong></span>
              <span className="subtle-text">{r.expires ? `${r.active ? 'until' : 'expired'} ${reportTime(r.expires)}` : 'permanent'} · {plural(r.requests, 'request')} allowed{r.lastUsed ? `, last ${reportTime(r.lastUsed)}` : ''} · added by {r.actor}</span>
              {r.risks.length ? <ul className="risks">{r.risks.map(x => <li key={x}>{x}</li>)}</ul> : null}
              <details><summary>{plural(r.rules.length, 'rule')} it allows</summary><ul className="rule-lines">{r.rules.map((l, i) => <li key={i}><code>{l.methods.length ? l.methods.join(', ') : 'any method'} {l.host}{l.path}</code> <span className="subtle-text">{l.purpose}</span></li>)}</ul></details>
            </div>
            <Button size="small" variant="secondary" buttonType="destructive" disabled={busy === r.id} onClick={() => remove(r)}>Remove</Button>
          </li>
        ))}
        {!rules.length ? <li className="empty-item">No web rules yet. Use “More ways to allow” on a waiting request.</li> : null}
      </ul>
      {data.ruleHistory?.length ? <details className="pad"><summary>Rule history</summary><ul className="rule-lines">{data.ruleHistory.map((e, i) => <li key={i}>{reportTime(e.at)} · {e.action} · {e.description} · {e.by}{e.detail ? ' · ' + e.detail : ''}</li>)}</ul></details> : null}
      {data.tools ? <details className="pad" id="passthrough-tools"><summary>Clients that may need pass-through without inspection</summary><p className="subtle-text">{data.tools.note}</p>
        <ul className="rule-lines">{data.tools.tools.map(t => <li key={t.name}><strong>{t.name}</strong> <Tag size="sm" text={t.evidence} /><br />{t.problem}{t.alternative ? <><br />Instead: {t.alternative}</> : null}{t.source ? <><br /><span className="subtle-text">Source: {t.source}</span></> : null}</li>)}</ul></details> : null}
    </section>
  );
}

function RequestLists({ws, data, setData}: {ws: SandboxState; data: Requests | null; setData: (r: Requests) => void}) {
  const {toast} = useData();
  const detail = ws.detail!;
  const [tab, setTab] = useState<'pending' | 'approved'>('pending');
  const initialFilter = hashQuery().get('q') || '';
  const [query, setQuery] = useState(initialFilter), [filter, setFilter] = useState(initialFilter);
  const [page, setPage] = useState(initialPage), [size, setSize] = useState(initialSize);
  const [jump, setJump] = useState('');
  const [busy, setBusy] = useState('');
  const [open, setOpen] = useState('');
  const load = useCallback(async () => { try { setData(await loadRequests(detail.id, filter, page, size)); } catch (e) { toast((e as Error).message, true); } }, [detail.id, filter, page, size, toast, setData]);
  useEffect(() => { load(); }, [load, detail.egressCounts?.pending, detail.egressCounts?.approved, detail.learning?.phase, ws.detail?.revision.id]);
  useEffect(() => { const t = window.setInterval(load, 30000); return () => window.clearInterval(t); }, [load]);
  useEffect(() => { const t = window.setTimeout(() => { if (query.trim() !== filter) { setFilter(query.trim()); setPage(1); } }, 300); return () => window.clearTimeout(t); }, [query, filter]);
  // Filter, page and size live in the URL, so a reload or a shared link keeps them.
  useEffect(() => { replaceHashQuery({q: filter, page: page > 1 ? page : undefined, size: size !== DEFAULT_PAGE_SIZE ? size : undefined}); }, [filter, page, size]);
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
  const dismiss = async (days: number) => {
    if (!confirm(`Dismiss waiting requests not seen for ${plural(days, 'day')}? Nothing is approved.`)) return;
    setBusy('clear');
    const f = dismissOlder(days);
    if (await ws.act(f.path, f.fields, 'old waiting requests dismissed')) await load();
    setBusy('');
  };
  const current = data ? data[tab] : null;
  const shown = current ? pageOf(current, page, size) : null;
  const go = (p: number) => { if (shown) setPage(Math.min(Math.max(1, p), shown.pages)); };
  const resize = (next: number) => {
    try { localStorage.setItem(SIZE_KEY, String(next)); } catch { /* private mode */ }
    // Stay near the same items: the page that holds the first one shown.
    setPage(pageKeeping(shown?.from || 1, next));
    setSize(next);
  };
  return (
    <section className="card" aria-label="Web requests" data-testid={tab === 'pending' ? 'pending-requests' : 'approved-requests'}>
      <Tabs value={tab} onValueChange={v => { setTab(v as 'pending' | 'approved'); setPage(1); }} className="inner-tabs">
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
      {tab === 'pending' && data?.capacity ? <CapacityBanner c={data.capacity} filter={filter} busy={busy === 'clear'} clear={clear} dismiss={dismiss} /> : null}
      {!shown || !current ? <p className="empty pad">Loading requests…</p> : (
        <ul className="items rows" aria-label={tab === 'pending' ? 'Requests awaiting approval' : 'Approved requests'}>
          {shown.rows.map(o => (
            <li key={o.observation} className={open === o.observation ? 'expanded' : undefined}>
              <RequestRow o={o} />
              {tab === 'pending' ? <span className="row-actions">
                <Button size="small" variant="primary" disabled={busy === o.observation} onClick={() => decide(o, true)}>{(o.fingerprints ?? 1) > 1 ? `Approve all ${o.fingerprints!.toLocaleString()}` : 'Approve'}</Button>
                {o.allow ? <Button size="small" variant="secondary" aria-expanded={open === o.observation} onClick={() => setOpen(open === o.observation ? '' : o.observation)}>More ways to allow…</Button> : null}
              </span>
                : <Button size="small" variant="secondary" buttonType="destructive" disabled={learning || busy === o.observation} title={learning ? 'Revoking is available once the learning window ends' : undefined} onClick={() => decide(o, false)}>Revoke</Button>}
              {open === o.observation && o.allow ? <AllowPanel ws={ws} allow={o.allow} onDone={() => { setOpen(''); load(); }} /> : null}
            </li>
          ))}
          {!shown.rows.length ? <li className="empty-item">{filter ? `Nothing matches “${filter}”.` : tab === 'pending' ? 'No requests awaiting approval.' : 'No approved requests yet.'}</li> : null}
        </ul>
      )}
      {shown && current ? (
        <nav className="pager" aria-label={tab === 'pending' ? 'Pages of requests awaiting approval' : 'Pages of approved requests'}>
          <Button size="small" variant="secondary" disabled={shown.page <= 1} onClick={() => go(1)}>First</Button>
          <Button size="small" variant="secondary" disabled={shown.page <= 1} onClick={() => go(shown.page - 1)}>Previous</Button>
          <span className="subtle-text" aria-live="polite">{shown.total ? `${shown.from}–${shown.to} of ${shown.total.toLocaleString()} · page ${shown.page} of ${shown.pages}` : '0'}{current.truncated ? ` (newest ${current.items.length} of ${current.total.toLocaleString()})` : ''}</span>
          <Button size="small" variant="secondary" disabled={shown.page >= shown.pages} onClick={() => go(shown.page + 1)}>Next</Button>
          <Button size="small" variant="secondary" disabled={shown.page >= shown.pages} onClick={() => go(shown.pages)}>Last</Button>
          <label className="field inline"><span className="field-label">Per page</span>
            <select className="select" value={size} onChange={e => resize(pageSize(e.target.value))}>{PAGE_SIZES.map(n => <option key={n} value={n}>{n}</option>)}</select></label>
          <form className="field inline" onSubmit={e => { e.preventDefault(); const n = Number(jump); if (n) go(n); setJump(''); }}>
            <label className="field-label" htmlFor="page-jump">Go to page</label>
            <Input id="page-jump" type="number" min={1} max={shown.pages} inputMode="numeric" value={jump} placeholder={String(shown.page)} onChange={e => setJump(e.target.value)} />
            <Button size="small" variant="tertiary" type="submit">Go</Button>
          </form>
          <span className="end subtle-text">{tab === 'approved' && learning ? 'Revoking is available once the learning window ends.' : 'Grouped by method and URL; a new query or body needs approval again.'}</span>
        </nav>
      ) : null}
    </section>
  );
}

export function Internet({ws, now}: {ws: SandboxState; now: number}) {
  const detail = ws.detail!;
  const [data, setData] = useState<Requests | null>(null);
  if (!webAccess(detail)) return (
    <div className="stack">
      <SectionIntro title="Internet">This sandbox uses fixed policy rules. Instant open and lock are available for sandboxes with learning egress.</SectionIntro>
      <Card variant="sunk" width="full"><CardHeader title="Fixed rules" subtitle="The console has no read API for this sandbox's rules yet." /><p><a href={classicHref(detail.id, 'access')}>View and edit the rules on the classic page</a></p></Card>
    </div>
  );
  return (
    <div className="stack">
      <WebAccessCard key={detail.id} ws={ws} now={now} />
      <RequestLists key={'r' + detail.id} ws={ws} data={data} setData={setData} />
      {data?.rules ? <WebRules ws={ws} data={data} /> : null}
      {detail.providerHosts.length ? <p className="subtle-text">Provider hosts are always reachable: {detail.providerHosts.map(h => <Tag key={h} size="sm" text={h} className="inline-tag" />)}</p> : null}
    </div>
  );
}
