// Internet section: the access level on top, then the fine-grained editors
// (requests awaiting approval, approved requests, web rules) in the shared
// grant table pattern. Editing them changes the level to Custom.
import {useCallback, useEffect, useState} from 'react';
import {sandboxAction, type AllowOptions, type EgressCapacity, type Observation, type WebRule} from '../../api';
import {useData} from '../../data';
import {SandboxAccessLevels} from '../../access/AccessLevels';
import {GrantTable, type Expiry} from '../../access/GrantTable';
import {presetOf} from '../../access/levels';
import {classicHref, timeOf} from '../../sandbox/Sandbox';
import {Button, Callout, Card, CardHeader, Input, Tabs, TabsList, TabsTrigger, Tag} from '../../ui';
import {plural, SectionIntro} from '../bits';
import {useConfirm} from '../Confirm';
import {addRule, clearUnapproved, decideRequest, DEFAULT_PAGE_SIZE, dismissOlder, draftFor, draftRisks, EXPIRIES, loadRequests, METHODS, needsConfirm, PAGE_SIZES, pageKeeping, pageOf, pageSize, passthroughAllowed, removeRule, requestUrl, rulesBySource, webAccess, webStartFresh, type Requests, type RuleDraft, type RuleKind} from '../sources/internet';
import {reportTime} from '../sources/reports';
import {hashQuery, replaceHashQuery, sandboxHref} from '../../router';
import type {SandboxState} from '../useSandbox';

const WRITES = ['POST', 'PUT', 'PATCH', 'DELETE'];

function RequestCell({o}: {o: Observation}) {
  const fingerprints = o.fingerprints ?? 1;
  const details = [
    o.package ? `${o.package.ecosystem} ${o.package.name} ${o.package.version || ''}`.trim() : '',
    o.requests ? plural(o.requests, 'request') : '',
    fingerprints > 1 ? plural(fingerprints, 'request fingerprint') : '',
  ].filter(Boolean);
  return <>
    <span><span className="method">{o.method}</span><code>{requestUrl(o)}</code></span>
    {details.length ? <span className="cell-sub">{details.join(', ')}</span> : null}
  </>;
}
const seen = (o: Observation) => <>{o.last ? timeOf(o.last) : ''}{o.first && o.last && o.first !== o.last ? <span className="cell-sub">first {timeOf(o.first)}</span> : null}</>;

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

// Runs one sandbox-control form per item, then refreshes once.
async function each<T>(ws: SandboxState, items: T[], form: (item: T) => {path: string; fields: Record<string, string | string[]>}, done: string, toast: (t: string, e?: boolean) => void) {
  let ok = 0;
  for (const item of items) {
    const f = form(item);
    try { await sandboxAction(f.path, ws.id, f.fields); ok++; } catch (e) { toast(`${ws.id}: ${ok} of ${items.length} ${done}, then: ${(e as Error).message}`, true); await ws.reload(); return; }
  }
  toast(`${ws.id}: ${ok} ${done}`);
  await ws.reload();
}

function WebRules({ws, data, now, reload}: {ws: SandboxState; data: Requests; now: number; reload: () => void}) {
  const {toast} = useData();
  const ask = useConfirm();
  const [adding, setAdding] = useState(false);
  const rules = data.rules || [];
  const {git} = rulesBySource(rules, 'all');
  const expiry = (r: WebRule): Expiry => r.expires ? {at: Date.parse(r.expires)} : 'never';
  const remove = async (list: WebRule[]) => {
    if (!await ask(list.length === 1 ? 'Remove this web rule?' : `Remove ${list.length} web rules?`, <ul>{list.map(r => <li key={r.id}>{r.description}</li>)}</ul>, 'Remove')) return;
    await each(ws, list, removeRule, 'web rules removed', toast);
    reload();
  };
  return (
    <GrantTable<WebRule> title={`Web rules (${rules.length})`} label="Web rules" testId="web-rules" now={now} rows={rules} rowKey={r => r.id} rowLabel={r => r.description}
      intro={<>
        <p>Broad approvals: a path pattern, a whole domain, every subdomain or a GitHub project. Removing one removes everything it allows. Private networks and cluster services stay blocked whatever a rule says.{git ? ' Rules from Git give read-only access to the source and docs of the projects this deployment uses; they change only in Git.' : ''}</p>
        {adding ? <AllowPanel ws={ws} free onDone={() => { setAdding(false); reload(); }} /> : null}
      </>}
      toolbar={<Button size="small" variant="secondary" onClick={() => setAdding(!adding)}>{adding ? 'Close' : 'Add a web rule'}</Button>}
      search={{placeholder: 'github.com', label: 'Search rules', text: r => r.description + ' ' + r.host + ' ' + r.path + ' ' + r.owner + '/' + r.repo}}
      filters={[
        {key: 'here', label: 'Added here', test: r => !r.fromGit},
        {key: 'git', label: 'From Git', test: r => Boolean(r.fromGit)},
        {key: 'standard', label: 'Standard level', test: r => Boolean(presetOf(r))},
        {key: 'uninspected', label: 'Uninspected', test: r => r.passthrough},
        {key: 'expired', label: 'Expired', test: r => !r.active},
      ]}
      bulk={[{key: 'remove', label: 'Remove selected', danger: true, applies: r => !r.fromGit, run: remove}]}
      columns={[
        {key: 'rule', label: 'Rule', cell: r => <>
          <span className="tags">{r.fromGit ? <Tag size="sm" variant="info" text="from Git" /> : null}{presetOf(r) ? <Tag size="sm" text="Standard" /> : null}{r.passthrough ? <Tag size="sm" variant="warning" text="uninspected" /> : null}{!r.active ? <Tag size="sm" text="expired" /> : null}</span>
          <strong>{r.description}</strong>
          {r.risks.length ? <ul className="risks">{r.risks.map(x => <li key={x}>{x}</li>)}</ul> : null}
        </>},
        {key: 'use', label: 'Used', cell: r => <>{plural(r.requests, 'request')}{r.lastUsed ? <span className="cell-sub">last {reportTime(r.lastUsed)}</span> : null}</>},
        {key: 'by', label: 'Added by', cell: r => r.fromGit ? <span className="subtle">Git: {r.source}</span> : r.actor},
      ]}
      expiry={expiry}
      actions={(r, row) => <>
        <Button size="small" variant="tertiary" aria-expanded={row.expanded} onClick={row.toggle}>{row.expanded ? 'Hide' : 'Details'}</Button>
        {r.fromGit ? null : <Button size="small" variant="secondary" buttonType="destructive" onClick={() => remove([r])}>Remove</Button>}
      </>}
      detail={r => <ul className="rule-lines">{r.rules.map((l, i) => <li key={i}><code>{l.methods.length ? l.methods.join(', ') : 'any method'} {l.host}{l.path}</code> <span className="subtle-text">{l.purpose}</span></li>)}</ul>}
      empty={{title: 'No web rules yet.', hint: 'Choose the Standard level above for the common registries, use More ways to allow on a waiting request, or add a rule here.', action: <Button size="small" variant="secondary" onClick={() => setAdding(true)}>Add a web rule</Button>}}
      footer={<>
        {data.ruleHistory?.length ? <details><summary>Rule history</summary><ul className="rule-lines">{data.ruleHistory.map((e, i) => <li key={i}>{reportTime(e.at)}: {e.action}, {e.description}, by {e.by}{e.detail ? '. ' + e.detail : ''}</li>)}</ul></details> : null}
        {data.tools ? <details id="passthrough-tools"><summary>Clients that may need pass-through without inspection</summary><p className="subtle-text">{data.tools.note}</p>
          <ul className="rule-lines">{data.tools.tools.map(t => <li key={t.name}><strong>{t.name}</strong> <Tag size="sm" text={t.evidence} /><br />{t.problem}{t.alternative ? <><br />Instead: {t.alternative}</> : null}{t.source ? <><br /><span className="subtle-text">Source: {t.source}</span></> : null}</li>)}</ul></details> : null}
      </>}
    />
  );
}

function RequestLists({ws, data, setData, now, version}: {ws: SandboxState; data: Requests | null; setData: (r: Requests) => void; now: number; version: number}) {
  const {toast} = useData();
  const ask = useConfirm();
  const detail = ws.detail!;
  const [tab, setTab] = useState<'pending' | 'approved'>('pending');
  const initialFilter = hashQuery().get('q') || '';
  const [query, setQuery] = useState(initialFilter), [filter, setFilter] = useState(initialFilter);
  const [page, setPage] = useState(initialPage), [size, setSize] = useState(initialSize);
  const [busy, setBusy] = useState('');
  const load = useCallback(async () => { try { setData(await loadRequests(detail.id, filter, page, size)); } catch (e) { toast((e as Error).message, true); } }, [detail.id, filter, page, size, toast, setData]);
  useEffect(() => { load(); }, [load, detail.egressCounts?.pending, detail.egressCounts?.approved, detail.learning?.phase, ws.detail?.revision.id, version]);
  useEffect(() => { const t = window.setInterval(load, 30000); return () => window.clearInterval(t); }, [load]);
  useEffect(() => { const t = window.setTimeout(() => { if (query.trim() !== filter) { setFilter(query.trim()); setPage(1); } }, 300); return () => window.clearTimeout(t); }, [query, filter]);
  // Filter, page and size live in the URL, so a reload or a shared link keeps them.
  useEffect(() => { replaceHashQuery({q: filter, page: page > 1 ? page : undefined, size: size !== DEFAULT_PAGE_SIZE ? size : undefined}); }, [filter, page, size]);
  const learning = detail.learning?.phase === 'Learning';
  const decide = async (list: Observation[], approve: boolean) => {
    if (!approve && !await ask(list.length === 1 ? 'Revoke this approval?' : `Revoke ${list.length} approvals?`, <p>The sandbox can no longer make {list.length === 1 ? 'this request' : 'these requests'} until approved again.</p>, 'Revoke')) return;
    setBusy('decide');
    await each(ws, list, o => decideRequest(o, approve), approve ? 'requests approved' : 'approvals revoked', toast);
    await load();
    setBusy('');
  };
  const clear = async () => {
    if (!data || !await ask(`Clear all ${data.pending.total} unapproved requests?`, <p>They stay blocked; new ones are listed again when seen.</p>, 'Clear')) return;
    setBusy('clear');
    const f = clearUnapproved();
    if (await ws.act(f.path, f.fields, 'unapproved requests cleared')) await load();
    setBusy('');
  };
  const dismiss = async (days: number) => {
    if (!await ask(`Dismiss waiting requests not seen for ${plural(days, 'day')}?`, <p>Nothing is approved.</p>, 'Dismiss')) return;
    setBusy('clear');
    const f = dismissOlder(days);
    if (await ws.act(f.path, f.fields, 'old waiting requests dismissed')) await load();
    setBusy('');
  };
  const startFresh = async () => {
    if (!await ask('Start a fresh learning window?', <p className="danger-box">Every recorded request, approved or not, is deleted first. Then every request is allowed and recorded for 1 hour.</p>, 'Delete and start learning')) return;
    const f = webStartFresh(60);
    await ws.act(f.path, f.fields, 'fresh learning window started');
  };
  const current = data ? data[tab] : null;
  const shown = current ? pageOf(current, page, size) : null;
  const go = (p: number) => { if (shown) setPage(Math.min(Math.max(1, p), shown.pages)); };
  const resize = (next: number) => {
    try { localStorage.setItem(SIZE_KEY, String(next)); } catch { /* private mode */ }
    setPage(pageKeeping(shown?.from || 1, next));
    setSize(next);
  };
  const pending = tab === 'pending';
  return (
    <div data-testid={pending ? 'pending-requests' : 'approved-requests'}>
      <GrantTable<Observation> now={now} loading={!shown}
        title={<Tabs value={tab} onValueChange={v => { setTab(v as 'pending' | 'approved'); setPage(1); }} className="inner-tabs">
          <TabsList aria-label="Requests">
            <TabsTrigger value="pending" text={`Awaiting approval (${data?.pending.total ?? detail.egressCounts?.pending ?? 0})`} />
            <TabsTrigger value="approved" text={`Approved (${data?.approved.total ?? detail.egressCounts?.approved ?? 0})`} />
          </TabsList>
        </Tabs>}
        label={pending ? 'Requests awaiting approval' : 'Approved requests'}
        rows={shown?.rows || []} rowKey={o => o.observation} rowLabel={o => o.method + ' ' + requestUrl(o)}
        toolbar={pending ? (data?.pending.total && !filter ? <Button variant="secondary" buttonType="destructive" size="small" disabled={busy === 'clear'} onClick={clear}>Clear unapproved</Button> : null)
          : <Button variant="tertiary" buttonType="destructive" size="small" onClick={startFresh}>Start fresh…</Button>}
        intro={pending && data?.capacity ? <CapacityBanner c={data.capacity} filter={filter} busy={busy === 'clear'} clear={clear} dismiss={dismiss} /> : null}
        search={{placeholder: 'api.example.com', label: 'Filter by host or path', value: query, onChange: setQuery}}
        filters={[
          {key: 'packages', label: 'Packages', test: o => Boolean(o.package?.name)},
          {key: 'writes', label: 'Sends data', test: o => WRITES.includes(o.method)},
        ]}
        bulk={pending ? [{key: 'approve', label: 'Approve selected', run: rows => decide(rows, true)}]
          : [{key: 'revoke', label: learning ? 'Revoke (after learning ends)' : 'Revoke selected', danger: true, applies: () => !learning, run: rows => decide(rows, false)}]}
        columns={[
          {key: 'request', label: 'Request', cell: o => <RequestCell o={o} />},
          {key: 'seen', label: 'Last seen', cell: seen},
        ]}
        expiry={pending ? undefined : () => ({note: 'Until revoked'})}
        actions={(o, row) => pending ? <>
          <Button size="small" variant="primary" disabled={busy === 'decide'} onClick={() => decide([o], true)}>{(o.fingerprints ?? 1) > 1 ? `Approve all ${o.fingerprints!.toLocaleString()}` : 'Approve'}</Button>
          {o.allow ? <Button size="small" variant="secondary" aria-expanded={row.expanded} onClick={row.toggle}>More ways…</Button> : null}
        </> : <Button size="small" variant="secondary" buttonType="destructive" disabled={learning || busy === 'decide'} title={learning ? 'Revoking is available once the learning window ends' : undefined} onClick={() => decide([o], false)}>Revoke</Button>}
        detail={o => pending && o.allow ? <AllowPanel ws={ws} allow={o.allow} onDone={() => load()} /> : null}
        empty={pending
          ? {title: 'No requests awaiting approval.', hint: 'Blocked requests appear here for you to approve. The sandbox is not asking for anything right now.'}
          : {title: 'No approved requests yet.', hint: 'Approve requests as they arrive, or open with learning to record what a task uses.'}}
        footer={shown && current ? (
          <nav className="pager" aria-label={pending ? 'Pages of requests awaiting approval' : 'Pages of approved requests'}>
            <Button size="small" variant="secondary" disabled={shown.page <= 1} onClick={() => go(1)}>First</Button>
            <Button size="small" variant="secondary" disabled={shown.page <= 1} onClick={() => go(shown.page - 1)}>Previous</Button>
            <span className="subtle-text" aria-live="polite">{shown.total ? `${shown.from} to ${shown.to} of ${shown.total.toLocaleString()}, page ${shown.page} of ${shown.pages}` : 'None'}{current.truncated ? ` (newest ${current.items.length} of ${current.total.toLocaleString()})` : ''}</span>
            <Button size="small" variant="secondary" disabled={shown.page >= shown.pages} onClick={() => go(shown.page + 1)}>Next</Button>
            <Button size="small" variant="secondary" disabled={shown.page >= shown.pages} onClick={() => go(shown.pages)}>Last</Button>
            <label className="field inline"><span className="field-label">Per page</span>
              <select className="select" value={size} onChange={e => resize(pageSize(e.target.value))}>{PAGE_SIZES.map(n => <option key={n} value={n}>{n}</option>)}</select></label>
            <span className="end subtle-text">{!pending && learning ? 'Revoking is available once the learning window ends.' : 'Grouped by method and URL; a new query or body needs approval again.'}</span>
          </nav>
        ) : null}
      />
    </div>
  );
}

export function Internet({ws, now}: {ws: SandboxState; now: number}) {
  const detail = ws.detail!;
  const [data, setData] = useState<Requests | null>(null);
  const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion(v => v + 1), []);
  if (!webAccess(detail)) return (
    <div className="stack">
      <SectionIntro title="Internet">This sandbox uses fixed policy rules from Git. Access levels apply to sandboxes with learning egress.</SectionIntro>
      <Card variant="sunk" width="full"><CardHeader title="Fixed rules" subtitle="The console has no read API for this sandbox's rules yet." /><p><a href={classicHref(detail.id, 'access')}>View and edit the rules on the classic page</a></p></Card>
    </div>
  );
  const fineGrained = <ul aria-label="Fine-grained access">
    <li className="label">Fine-grained:</li>
    <li><a href="#web-requests">Requests</a></li>
    <li><a href="#web-rules">Web rules</a></li>
    <li><a href={sandboxHref(detail.id, 'lan')}>LAN devices</a></li>
    <li><a href={sandboxHref(detail.id, 'packages')}>Packages</a></li>
    <li><a href={sandboxHref(detail.id, 'credentials')}>Credentials</a></li>
  </ul>;
  return (
    <div className="stack">
      <SandboxAccessLevels ws={ws} now={now} data={data} onChanged={reload} fineGrained={fineGrained} />
      <div id="web-requests"><RequestLists key={'r' + detail.id} ws={ws} data={data} setData={setData} now={now} version={version} /></div>
      <div id="web-rules">{data?.rules ? <WebRules ws={ws} data={data} now={now} reload={reload} /> : data ? <Callout variant="neutral" description="This sandbox-control release has no web rules; approve requests one by one." /> : null}</div>
    </div>
  );
}

