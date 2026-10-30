// Home: every sandbox and project at a glance, with state, access and what
// waits on the operator, a filter, sort orders and recent activity. The same
// view, scoped, is the Sandboxes (#/sandboxes) and Projects (#/projects) page.
import {useEffect, useMemo, useState} from 'react';
import {api, egressControllable, egressState, orcaProjects, sandboxDetail, type AuditRecord, type ExistingSandbox, type OrcaProject} from './api';
import {useData} from './data';
import {ago, filterRows, projectRow, sandboxRow, SHOWS, sortRows, SORTS, toKind, toShow, toSort, totals, type Kind, type Row, type Show, type Sort} from './home';
import {existingKind, orcaRuntimeId, projectPreview} from './orca';
import {hashQuery, navigate, newSandboxHref, orcaHref, projectHref, replaceHashQuery, sandboxHref} from './router';
import {useShell} from './shell/state';
import {clock, type Status} from './state';
import {mergeHistory} from './workspace/sources/history';
import {loadLan} from './workspace/sources/lan';
import {Button, Callout, Card, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Tag, ToggleGroup, ToggleItem} from './ui';

const DURATIONS: [string, string][] = [['15', '15 min'], ['60', '1 hour'], ['240', '4 hours'], ['1440', '24 hours']];
// Each refresh lists Orca's repos in the runtime, so it runs far less often than the sandbox poll.
const ORCA_REFRESH_MS = 60000;
// Recent activity reads each workspace's history; it refreshes once a minute.
const ACTIVITY_MS = 60000;
const ACTIVITY_SHOWN = 8;

// The Orca runtime's projects, for its row. A failure leaves the row without them.
function useOrcaProjects(enabled: boolean) {
  const [records, setRecords] = useState<OrcaProject[]>([]);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    const load = () => orcaProjects().then(list => { if (live) setRecords(list); }, () => undefined);
    load();
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') load(); }, ORCA_REFRESH_MS);
    return () => { live = false; window.clearInterval(timer); };
  }, [enabled]);
  return records;
}

interface Activity { key: string; workspace: string; href: string; at: number; title: string; detail: string; by: string }
const humanize = (action: string) => { const text = action.replace(/[-_]+/g, ' ').trim().replace(/\blan\b/gi, 'LAN'); return text.charAt(0).toUpperCase() + text.slice(1); };

// The newest events across workspaces: policy revisions (which include start
// and stop) and LAN grant changes for sandboxes, the audit trail for projects.
// A workspace whose history fails to load is left out and counted.
function useRecentActivity(sandboxes: string[], projects: string[]) {
  const [items, setItems] = useState<Activity[] | null>(null);
  const [missing, setMissing] = useState(0);
  const key = sandboxes.join(',') + '|' + projects.join(',');
  useEffect(() => {
    let live = true;
    const load = async () => {
      let failed = 0;
      const parts = await Promise.all([
        ...sandboxes.map(id => Promise.all([sandboxDetail(id).catch(() => null), loadLan(id).catch(() => null)]).then(([detail, lan]) => {
          if (!detail && !lan) failed++;
          return mergeHistory({revisions: detail?.revisions, lan}).map(e => ({key: 's:' + id, workspace: id, href: sandboxHref(id, 'history'), at: e.at, title: e.title, detail: e.detail, by: e.by}));
        })),
        ...projects.map(name => api<AuditRecord[]>('workspaces/' + encodeURIComponent(name) + '/audit').then(list => (list || []).map(r => ({
          key: 'p:' + name, workspace: name, href: projectHref(name, 'history'), at: Date.parse(r.time) || 0, title: humanize(r.action), detail: r.message || '', by: '',
        })), () => { failed++; return [] as Activity[]; })),
      ]);
      if (!live) return;
      setItems(parts.flat().filter(a => a.at > 0).sort((a, b) => b.at - a.at));
      setMissing(failed);
    };
    load();
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') load(); }, ACTIVITY_MS);
    return () => { live = false; window.clearInterval(timer); };
  }, [key]); // sandboxes and projects are derived from key
  return {items, missing};
}

function OrcaProjectList({records}: {records: OrcaProject[]}) {
  const {names, more, total} = projectPreview(records);
  if (!total) return null;
  return <a className="row-projects" href={orcaHref()} data-testid="orca-project-list">{`${total} Orca project${total === 1 ? '' : 's'}: ${names.join(', ')}${more ? ` +${more} more` : ''}`}</a>;
}

function StatusTag({state}: {state: Status}) {
  return <Tag size="sm" variant={state.variant} text={state.label} className={state.busy ? 'busy' : undefined} />;
}

function EgressPanel({minutes, setMinutes}: {minutes: string; setMinutes: (value: string) => void}) {
  const {existing, egress, busy, existingError, session} = useData();
  const controllable = existing.filter(egressControllable);
  const open = controllable.filter(s => egressState(s) !== 'Locked');
  const ids = controllable.map(s => s.id);
  const working = ids.some(id => busy.has(id));
  if (!session.sandboxControl) return null;
  const summary = !controllable.length ? 'No sandboxes support instant web access changes.'
    : open.length ? `${open.length} of ${controllable.length} open to the public web.` : `All ${controllable.length} sandboxes are locked to approved requests.`;
  return (
    <section className={'egress-panel' + (open.length ? ' is-open' : '')} aria-labelledby="egress-title">
      <div className="egress-copy">
        <h2 id="egress-title">Web access</h2>
        <p className="egress-summary" data-testid="egress-summary">{summary}</p>
        <p className="subtle">Open allows any public HTTP and HTTPS request immediately, with nothing learned. Lock returns every sandbox to its approved requests on the next request. Private networks, cluster services and cloud metadata stay blocked either way.</p>
      </div>
      <div className="egress-controls">
        <div className="field">
          <span className="field-label" id="egress-duration">Open for</span>
          <ToggleGroup type="single" value={minutes} onValueChange={value => value && setMinutes(value)} aria-labelledby="egress-duration" className="durations">
            {DURATIONS.map(([value, label]) => <ToggleItem key={value} value={value} aria-label={'Open for ' + label}>{label}</ToggleItem>)}
          </ToggleGroup>
        </div>
        <div className="egress-buttons">
          <Button variant="primary" size="large" disabled={!controllable.length || working} onClick={() => egress(ids, 'open', Number(minutes))}>Open all</Button>
          <Button variant="primary" buttonType="destructive" size="large" disabled={!controllable.length || working} onClick={() => egress(ids, 'lock', Number(minutes))}>Lock all</Button>
        </div>
      </div>
      {existingError ? <Callout variant="danger" title="Existing sandboxes are unavailable" description={existingError} /> : null}
    </section>
  );
}

function AccessCell({row}: {row: Row}) {
  const a = row.access;
  return (
    <span className="access-cell">
      <Tag size="sm" variant={a.variant} text={a.label + (a.expires && a.rank <= 1 ? ' until ' + clock(a.expires) : '')} />
      {row.lan ? <span className={'access-lan' + (row.kind === 'sandbox' ? ' live' : '')}>{row.kind === 'sandbox' ? `LAN open (${row.lan})` : `LAN: ${row.lan} host${row.lan === 1 ? '' : 's'}`}</span> : null}
    </span>
  );
}

function RowActions({row, sandbox, minutes}: {row: Row; sandbox?: ExistingSandbox; minutes: string}) {
  const {egress, busy} = useData();
  if (row.kind === 'project') return <Button size="small" variant="tertiary" onClick={() => navigate(projectHref(row.name, 'internet'))}>Rules<span className="visually-hidden"> for {row.name}</span></Button>;
  if (!sandbox || !egressControllable(sandbox)) return <Button size="small" variant="tertiary" onClick={() => navigate(sandboxHref(row.name))}>Manage<span className="visually-hidden"> {row.name}</span></Button>;
  const pending = busy.has(row.name);
  return egressState(sandbox) === 'Locked'
    ? <Button size="small" variant="secondary" disabled={pending} onClick={() => egress([row.name], 'open', Number(minutes))} aria-label={'Open web access for ' + row.name}>{pending ? 'Opening…' : 'Open'}</Button>
    : <Button size="small" variant="primary" buttonType="destructive" disabled={pending} onClick={() => egress([row.name], 'lock', Number(minutes))} aria-label={'Lock web access for ' + row.name}>{pending ? 'Locking…' : 'Lock'}</Button>;
}

function WorkspaceRow({row, sandbox, minutes, orca, now, loadingActivity}: {row: Row; sandbox?: ExistingSandbox; minutes: string; orca?: OrcaProject[]; now: number; loadingActivity: boolean}) {
  const review = row.kind === 'sandbox' ? sandboxHref(row.name, sandbox && egressControllable(sandbox) ? 'internet' : 'overview') : projectHref(row.name);
  return (
    <TableRow data-sandbox={row.kind === 'sandbox' ? row.name : undefined} data-project={row.kind === 'project' ? row.name : undefined}>
      <TableCell data-label="Name"><a className="row-title" href={row.href}>{row.name}</a><span className="row-kind">{row.kindLabel}</span><span className="row-sub">{row.sub}</span>{orca ? <OrcaProjectList records={orca} /> : null}</TableCell>
      <TableCell data-label="Status"><StatusTag state={row.state} /></TableCell>
      <TableCell data-label="Access"><AccessCell row={row} /></TableCell>
      <TableCell data-label="Waiting on you">{row.waiting ? <a href={review} className="count-link"><span className="count num">{row.waiting}</span> to review</a>
        : row.attention ? <a href={review} className="attention">{row.attention}</a> : <span className="subtle">None</span>}</TableCell>
      <TableCell data-label="Last activity">{row.lastActivity ? <a className="row-when" href={row.kind === 'sandbox' ? sandboxHref(row.name, 'history') : projectHref(row.name, 'history')}><time dateTime={new Date(row.lastActivity).toISOString()}>{ago(row.lastActivity, now)}</time></a> : <span className="subtle">{loadingActivity ? 'Loading…' : 'None recorded'}</span>}</TableCell>
      <TableCell data-label="" align="right" className="row-actions"><RowActions row={row} sandbox={sandbox} minutes={minutes} /></TableCell>
    </TableRow>
  );
}

function RecentActivity({items, missing, now, scopeLabel}: {items: Activity[] | null; missing: number; now: number; scopeLabel: string}) {
  return (
    <section className="card dash-activity" aria-labelledby="activity-title">
      <div className="card-hd"><h2 id="activity-title">Recent activity</h2><span className="subtle-text">Policy, lifecycle, LAN and audit events {scopeLabel}</span></div>
      {items === null ? <p className="pad subtle-text">Loading activity…</p> : items.length ? (
        <ol className="dash-events">
          {items.slice(0, ACTIVITY_SHOWN).map((e, i) => (
            <li key={e.key + e.at + i}>
              <time dateTime={new Date(e.at).toISOString()} title={new Date(e.at).toLocaleString()}>{ago(e.at, now)}</time>
              <div className="item-main"><span><a href={e.href}>{e.workspace}</a>: {e.title}</span>{e.detail ? <span className="subtle-text">{e.detail}</span> : null}</div>
              {e.by ? <span className="subtle-text dash-by">by {e.by}</span> : null}
            </li>
          ))}
        </ol>
      ) : <p className="pad subtle-text">No recorded events yet.</p>}
      {missing ? <p className="card-note subtle-text">History of {missing} workspace{missing === 1 ? '' : 's'} could not be read.</p> : null}
    </section>
  );
}

// Filter state lives in the hash query (?q=&show=&kind=&sort=), so reloads and links keep it.
function useFilters() {
  const read = () => { const q = hashQuery(); return {q: q.get('q') || '', show: toShow(q.get('show')), kind: toKind(q.get('kind')), sort: toSort(q.get('sort'))}; };
  const [state, setState] = useState(read);
  useEffect(() => {
    const change = () => setState(read());
    window.addEventListener('hashchange', change);
    return () => window.removeEventListener('hashchange', change);
  }, []);
  const update = (next: Partial<typeof state>) => {
    const merged = {...state, ...next};
    setState(merged);
    replaceHashQuery({q: merged.q || undefined, show: merged.show === 'all' ? undefined : merged.show, kind: merged.kind === 'all' ? undefined : merged.kind, sort: merged.sort === 'urgency' ? undefined : merged.sort});
  };
  return [state, update] as const;
}

const SCOPES: Record<Kind, {title: string; lead: string; doc: string}> = {
  all: {title: 'All sandboxes', lead: 'Isolated agent workspaces with their own storage, repository access and outbound rules.', doc: 'Sandboxes'},
  sandbox: {title: 'Sandboxes', lead: 'Coding sandboxes and the Orca runtime, controlled through sandbox-control.', doc: 'Sandbox list · Sandboxes'},
  project: {title: 'Projects', lead: 'Project sandboxes: one repository each, with fixed outbound rules set by the console.', doc: 'Projects · Sandboxes'},
};

export function Home({scope = 'all'}: {scope?: Kind}) {
  const {loaded, projects, existing, session} = useData();
  const {lanOpen} = useShell();
  const [minutes, setMinutes] = useState('60');
  const [filters, setFilters] = useFilters();
  const [showRemoved, setShowRemoved] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => { const t = window.setInterval(() => setNow(Date.now()), 30000); return () => window.clearInterval(t); }, []);
  useEffect(() => { document.title = SCOPES[scope].doc; }, [scope]);
  const runtime = orcaRuntimeId(session, existing);
  const orca = useOrcaProjects(runtime !== undefined);
  const active = projects.filter(p => p.status?.phase !== 'Removed'), removed = projects.filter(p => p.status?.phase === 'Removed');
  const kind: Kind = scope === 'all' ? filters.kind : scope;
  const activity = useRecentActivity(kind === 'project' ? [] : existing.map(s => s.id), kind === 'sandbox' ? [] : active.map(p => p.metadata.name));
  const last = useMemo(() => {
    const out: Record<string, number> = {};
    for (const a of activity.items || []) out[a.key] = Math.max(out[a.key] || 0, a.at);
    return out;
  }, [activity.items]);
  const all = useMemo(() => [
    ...existing.map(s => sandboxRow(s, existingKind(s, runtime), lanOpen[s.id] || 0)),
    ...active.map(projectRow),
  ].map(r => ({...r, lastActivity: last[r.key]})), [existing, active, runtime, lanOpen, last]);
  const scoped = all.filter(r => kind === 'all' || r.kind === kind);
  const rows = sortRows(filterRows(scoped, {q: filters.q, show: filters.show}), filters.sort);
  const t = totals(scoped);
  const filtered = Boolean(filters.q) || filters.show !== 'all';
  const byId = new Map(existing.map(s => [s.id, s]));
  const toggleShow = (show: Show) => setFilters({show: filters.show === show ? 'all' : show});
  const stat = (show: Show, n: number, label: string, tone = '') => (
    <li><button type="button" className={'dash-stat' + (n && tone ? ' ' + tone : '')} aria-pressed={filters.show === show} onClick={() => toggleShow(show)}>
      <span className="dash-stat-n num">{n}</span><span className="dash-stat-label">{label}</span>
    </button></li>
  );
  const scopeLabel = kind === 'sandbox' ? 'for sandboxes' : kind === 'project' ? 'for projects' : 'across every workspace';
  return (
    <>
      <div className="page-head">
        <div><h1>{SCOPES[scope].title}</h1><p className="muted">{SCOPES[scope].lead}</p></div>
        <Button variant="secondary" onClick={() => navigate(newSandboxHref())}>New sandbox</Button>
      </div>
      {loaded && scoped.length ? (
        <ul className="dash-stats" aria-label="Summary; each one filters the list">
          {stat('running', t.running, `of ${t.total} running`)}
          {kind !== 'project' ? stat('open', t.open, 'open or learning on the web', 'bad') : null}
          {kind !== 'project' ? stat('waiting', t.waiting, 'requests waiting on you', 'warn') : null}
          {stat('attention', t.attention, 'need attention', 'warn')}
        </ul>
      ) : null}
      {kind !== 'project' ? <EgressPanel minutes={minutes} setMinutes={setMinutes} /> : null}
      <section className="dash-list" aria-labelledby="dash-list-title">
        <h2 id="dash-list-title" className="visually-hidden">{SCOPES[scope].title} list</h2>
        <div className="dash-toolbar" role="search">
          <div className="field dash-filter">
            <label className="field-label" htmlFor="dash-q">Filter</label>
            <input id="dash-q" className="text-input" type="search" placeholder="Name, repository, namespace or state" value={filters.q} onChange={e => setFilters({q: e.target.value})} />
          </div>
          {scope === 'all' ? (
            <div className="field">
              <label className="field-label" htmlFor="dash-kind">Kind</label>
              <select id="dash-kind" className="select" value={filters.kind} onChange={e => setFilters({kind: toKind(e.target.value)})}>
                <option value="all">Sandboxes and projects</option><option value="sandbox">Sandboxes</option><option value="project">Projects</option>
              </select>
            </div>
          ) : null}
          <div className="field">
            <label className="field-label" htmlFor="dash-show">Show</label>
            <select id="dash-show" className="select" value={filters.show} onChange={e => setFilters({show: toShow(e.target.value)})}>
              {SHOWS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
            </select>
          </div>
          <div className="field">
            <label className="field-label" htmlFor="dash-sort">Sort by</label>
            <select id="dash-sort" className="select" value={filters.sort} onChange={e => setFilters({sort: toSort(e.target.value) as Sort})}>
              {SORTS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
            </select>
          </div>
        </div>
        <p className="dash-count subtle-text" role="status">{!loaded ? '' : filtered ? `${rows.length} of ${scoped.length} shown` : `${scoped.length} ${scoped.length === 1 ? 'workspace' : 'workspaces'}`}
          {filtered ? <button type="button" className="text-button" onClick={() => setFilters({q: '', show: 'all'})}>Clear filters</button> : null}</p>
        {!loaded ? <p className="empty">Loading sandboxes…</p> : rows.length ? (
          <Table aria-labelledby="dash-list-title" containerClassName="sandbox-table">
            <TableHeader><TableRow><TableHead>Name</TableHead><TableHead>Status</TableHead><TableHead>Access</TableHead><TableHead>Waiting on you</TableHead><TableHead>Last activity</TableHead><TableHead><span className="visually-hidden">Actions</span></TableHead></TableRow></TableHeader>
            <TableBody>{rows.map(r => <WorkspaceRow key={r.key} row={r} sandbox={byId.get(r.name)} minutes={minutes} orca={r.kind === 'sandbox' && r.name === runtime ? orca : undefined} now={now} loadingActivity={activity.items === null} />)}</TableBody>
          </Table>
        ) : scoped.length ? (
          <Card variant="sunk" width="full" className="empty-card"><h2>Nothing matches</h2><p className="muted">No {kind === 'project' ? 'project' : 'sandbox'} matches this filter.</p><div><Button variant="secondary" onClick={() => setFilters({q: '', show: 'all'})}>Clear filters</Button></div></Card>
        ) : (
          <Card variant="sunk" width="full" className="empty-card"><h2>{kind === 'sandbox' ? 'No sandboxes enrolled' : kind === 'project' ? 'No projects yet' : 'No sandboxes yet'}</h2><p className="muted">{kind === 'sandbox' ? 'Sandboxes appear here once sandbox-control enrols them.' : 'Create one from a repository to give an agent an isolated place to work.'}</p>{kind !== 'sandbox' ? <div><Button variant="primary" onClick={() => navigate(newSandboxHref())}>New sandbox</Button></div> : null}</Card>
        )}
      </section>
      {loaded && scoped.length ? <RecentActivity items={activity.items} missing={activity.missing} now={now} scopeLabel={scopeLabel} /> : null}
      {removed.length && kind !== 'sandbox' ? (
        <div className="removed">
          <Button variant="tertiary" size="small" aria-expanded={showRemoved} onClick={() => setShowRemoved(!showRemoved)}>{(showRemoved ? 'Hide' : 'Show') + ` removed (${removed.length})`}</Button>
          {showRemoved ? <ul className="plain-list">{removed.map(p => <li key={p.metadata.name}><a href={projectHref(p.metadata.name)}>{p.metadata.name}</a> <span className="subtle">{p.spec.repository}</span></li>)}</ul> : null}
        </div>
      ) : null}
    </>
  );
}
