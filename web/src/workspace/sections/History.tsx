import {useEffect, useMemo, useState} from 'react';
import {failure, type Project, type RuleEvent} from '../../api';
import {useData} from '../../data';
import {hashQuery, replaceHashQuery} from '../../router';
import {ActivityTab} from '../../sandbox/Activity';
import type {SandboxContext} from '../../sandbox/Sandbox';
import {Button, Tag, type TagVariant} from '../../ui';
import {NotAvailable, SectionIntro} from '../bits';
import {loadDownloadHistory, type DownloadEvent} from '../sources/downloads';
import {auditToHistory, DEFAULT_FILTER, filterHistory, filterQuery, HISTORY_FILTERS, historyHosts, mergeAudit, mergeHistory, OUTCOME_LABEL, OUTCOMES, RANGES, readFilter, toAuditEntries, type AuditSource, type HistoryEvent, type HistoryFilter, type Outcome} from '../sources/history';
import {loadRequests, paginate} from '../sources/internet';
import {stamp, useNow} from '../time';
import type {SandboxState} from '../useSandbox';
import '../../attention/attention.css';

const PER_PAGE = 20;
const TONE: Record<Outcome, TagVariant> = {granted: 'warning', ended: 'neutral', blocked: 'danger', failed: 'danger', done: 'success', info: 'neutral'};

// A filtered, paged timeline. Filters and page are in the URL query
// (?type=&host=&outcome=&range=&page=), so reloads and shared links keep them.
export function Timeline({events, label, loading, types}: {events: HistoryEvent[]; label: string; loading?: boolean; types?: HistoryEvent['type'][]}) {
  const [filter, setFilter] = useState<HistoryFilter>(() => readFilter(hashQuery()));
  const [page, setPage] = useState(() => Math.max(0, (Number(hashQuery().get('page')) || 1) - 1));
  const now = useNow(60_000);
  const hosts = useMemo(() => historyHosts(events), [events]);
  const matched = filterHistory(events, filter, now);
  const shown = paginate(matched, page, PER_PAGE);
  const update = (patch: Partial<HistoryFilter>) => { const next = {...filter, ...patch}; setFilter(next); setPage(0); replaceHashQuery(filterQuery(next)); };
  const go = (p: number) => { setPage(p); replaceHashQuery({...filterQuery(filter), page: p ? p + 1 : undefined}); };
  const filtered = JSON.stringify(filter) !== JSON.stringify(DEFAULT_FILTER);
  const typeOptions = HISTORY_FILTERS.filter(([k]) => k === 'all' || !types || types.includes(k));
  return <>
    <div className="hist-filters" role="search" aria-label="Filter history">
      <label>Type
        <select className="select" value={filter.type} onChange={e => update({type: e.target.value as HistoryFilter['type']})}>
          {typeOptions.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
        </select></label>
      <label>Host or address
        <input className="text-input" type="search" list="history-hosts" value={filter.host} placeholder="github.com" autoCapitalize="off" spellCheck={false} onChange={e => update({host: e.target.value})} />
        <datalist id="history-hosts">{hosts.map(h => <option key={h} value={h} />)}</datalist></label>
      <label>Outcome
        <select className="select" value={filter.outcome} onChange={e => update({outcome: e.target.value as HistoryFilter['outcome']})}>
          {OUTCOMES.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
        </select></label>
      <label>Time
        <select className="select" value={filter.range} onChange={e => update({range: e.target.value})}>
          {RANGES.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
        </select></label>
      {filtered ? <Button className="hist-reset" size="small" variant="tertiary" onClick={() => update(DEFAULT_FILTER)}>Clear filters</Button> : null}
    </div>
    <section className="card" aria-label="Decisions and grant changes">
      <div className="card-hd"><h3>{loading ? 'Loading history…' : filtered ? `${matched.length} of ${events.length} events` : `${events.length} events`}</h3></div>
      <ol className="timeline pad" aria-label={label}>
        {loading && !events.length ? <li className="timeline-empty"><p>Loading history…</p></li> : shown.rows.length ? shown.rows.map((e, i) => (
          <li key={e.type + e.at + i}><time dateTime={e.at ? new Date(e.at).toISOString() : undefined}>{stamp(e.at) || 'time not recorded'}</time>
            <div><span className="hist-line"><strong>{e.title}</strong>{e.outcome !== 'info' ? <Tag className="hist-outcome" size="sm" variant={TONE[e.outcome]} text={OUTCOME_LABEL[e.outcome]} /> : null}</span>
              {e.detail ? <p className="subtle-text">{e.detail}</p> : null}
              {e.by ? <p className="subtle-text">By {e.by}</p> : null}</div></li>
        )) : <li className="timeline-empty"><p>{events.length ? 'No events match these filters.' : 'No events yet.'}</p></li>}
      </ol>
      <div className="pager">
        <Button size="small" variant="secondary" disabled={shown.page === 0} onClick={() => go(shown.page - 1)}>Newer</Button>
        <span className="subtle-text">{shown.rows.length ? `Events ${shown.from} to ${shown.to} of ${matched.length}, page ${shown.page + 1} of ${shown.pages}` : `Page ${shown.page + 1} of ${shown.pages}`}</span>
        <Button size="small" variant="secondary" disabled={shown.page >= shown.pages - 1} onClick={() => go(shown.page + 1)}>Older</Button>
      </div>
    </section>
  </>;
}

// A sandbox-control sandbox: LAN, dev links, policy revisions, reports, files and web rules.
export function History({ws, ctx}: {ws: SandboxState; ctx: SandboxContext}) {
  const [downloads, setDownloads] = useState<DownloadEvent[] | null>(null);
  const [rules, setRules] = useState<RuleEvent[] | null>(null);
  const learning = Boolean(ws.detail?.learningEgress);
  useEffect(() => { let live = true; loadDownloadHistory(ws.id).then(d => { if (live) setDownloads(d); }, () => undefined); return () => { live = false; }; }, [ws.id]);
  // Web rule changes ride on the request list; one small page is enough.
  useEffect(() => { let live = true; if (learning) loadRequests(ws.id, '', 1, 10).then(r => { if (live) setRules(r.ruleHistory || null); }, () => undefined); return () => { live = false; }; }, [ws.id, learning]);
  const events = useMemo(() => mergeHistory({lan: ws.lan, devlinks: ws.devlinks, revisions: ws.detail?.revisions, reports: ws.reports, downloads, rules}), [ws.lan, ws.devlinks, ws.detail?.revisions, ws.reports, downloads, rules]);
  return (
    <div className="stack">
      <SectionIntro title="History">Decisions, grant changes, transfers and agent reports for {ws.id}, newest first.</SectionIntro>
      <Timeline events={events} label="History" />
      <NotAvailable what="Web access window history" needs="Open, lock and single-request approval events have no read API yet; web rule changes are listed above, and blocked requests appear under Recent activity below." />
      <details className="card fold" open>
        <summary>Recent activity from logs and traces</summary>
        <ActivityTab {...ctx} />
      </details>
    </div>
  );
}

// Where a console project's history can come from. null means "not served here".
async function optionalJSON(path: string): Promise<unknown | null> {
  const response = await fetch('/api/v1/' + path, {credentials: 'same-origin', headers: {Accept: 'application/json'}});
  if (response.status === 404 || response.status === 405) return null;
  if (!response.ok) throw await failure(response);
  return response.json();
}
// Today's API. When durable history moves to its own store, the controller keeps
// serving this route (or a second source is added here); the section is unchanged.
export const PROJECT_HISTORY_SOURCES: AuditSource[] = [
  {name: 'audit', load: project => optionalJSON('workspaces/' + encodeURIComponent(project) + '/audit')},
];

// A console project: lifecycle, access and preparation changes from every source the controller serves.
export function ProjectHistory({p, sources = PROJECT_HISTORY_SOURCES}: {p: Project; sources?: AuditSource[]}) {
  const {toast} = useData();
  const [events, setEvents] = useState<HistoryEvent[] | null>(null);
  const name = p.metadata.name;
  useEffect(() => {
    let live = true;
    Promise.allSettled(sources.map(s => s.load(name))).then(results => {
      if (!live) return;
      const lists = results.map(r => r.status === 'fulfilled' ? toAuditEntries(r.value) : []);
      const failed = results.filter((r): r is PromiseRejectedResult => r.status === 'rejected');
      if (failed.length === results.length && failed.length) toast((failed[0].reason as Error).message, true);
      setEvents(auditToHistory(mergeAudit(lists)));
    });
    return () => { live = false; };
  }, [name, sources, toast]);
  return (
    <div className="stack">
      <SectionIntro title="History">Lifecycle, access and preparation changes for {name}, newest first.</SectionIntro>
      <Timeline events={events || []} loading={events === null} label="Access history" types={['web', 'lan', 'download', 'policy']} />
    </div>
  );
}
