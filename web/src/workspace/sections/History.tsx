import {useEffect, useMemo, useState} from 'react';
import {ActivityTab} from '../../sandbox/Activity';
import type {SandboxContext} from '../../sandbox/Sandbox';
import {Button} from '../../ui';
import {NotAvailable, SectionIntro} from '../bits';
import {loadDownloadHistory, type DownloadEvent} from '../sources/downloads';
import {HISTORY_FILTERS, mergeHistory, type HistoryType} from '../sources/history';
import {paginate} from '../sources/internet';
import {stamp} from '../time';
import type {SandboxState} from '../useSandbox';

const PER_PAGE = 8;

export function History({ws, ctx}: {ws: SandboxState; ctx: SandboxContext}) {
  const [filter, setFilter] = useState<HistoryType | 'all'>('all');
  const [page, setPage] = useState(0);
  const [downloads, setDownloads] = useState<DownloadEvent[] | null>(null);
  useEffect(() => { let live = true; loadDownloadHistory(ws.id).then(d => { if (live) setDownloads(d); }, () => undefined); return () => { live = false; }; }, [ws.id]);
  const events = useMemo(() => mergeHistory({lan: ws.lan, devlinks: ws.devlinks, revisions: ws.detail?.revisions, reports: ws.reports, downloads}), [ws.lan, ws.devlinks, ws.detail?.revisions, ws.reports, downloads]);
  const shown = paginate(events.filter(e => filter === 'all' || e.type === filter), page, PER_PAGE);
  return (
    <div className="stack">
      <SectionIntro title="History">Console decisions and grant changes for {ws.id}, newest first.</SectionIntro>
      <section className="card" aria-label="Decisions and grant changes">
        <div className="card-hd">
          <div className="seg" role="group" aria-label="Filter history">
            {HISTORY_FILTERS.map(([k, l]) => <button key={k} type="button" className="seg-button" aria-pressed={filter === k} onClick={() => { setFilter(k); setPage(0); }}>{l}</button>)}
          </div>
        </div>
        <ol className="timeline pad" aria-label="History">
          {shown.rows.length ? shown.rows.map((e, i) => (
            <li key={e.type + e.at + i}><time dateTime={e.at ? new Date(e.at).toISOString() : undefined}>{stamp(e.at) || '—'}</time><div><strong>{e.title}</strong><p className="subtle-text">{[e.detail, e.by].filter(Boolean).join(' · ')}</p></div></li>
          )) : <li className="timeline-empty"><p>No events.</p></li>}
        </ol>
        <div className="pager">
          <Button size="small" variant="secondary" disabled={shown.page === 0} onClick={() => setPage(shown.page - 1)}>Newer</Button>
          <span className="subtle-text">Page {shown.page + 1} of {shown.pages}</span>
          <Button size="small" variant="secondary" disabled={shown.page >= shown.pages - 1} onClick={() => setPage(shown.page + 1)}>Older</Button>
        </div>
      </section>
      <NotAvailable what="Web access history" needs="Open, lock and approval events have no read API yet; blocked requests appear under Recent activity below." />
      <details className="card fold" open>
        <summary>Recent activity from logs and traces</summary>
        <ActivityTab {...ctx} />
      </details>
    </div>
  );
}
