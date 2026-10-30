// The run list shared by the Runs page and a sandbox's Runs section: status
// filters, a search, duration and the first failure reason on each row. Filter,
// search and page live in the URL query, so reloads and shared links keep them.
import {useMemo, useState} from 'react';
import {hashQuery, replaceHashQuery} from '../router';
import {plainText} from '../orca';
import {paginate} from '../workspace/sources/internet';
import {useNow} from '../workspace/time';
import type {RunSummary} from './api';
import {runHref} from './href';
import {countRuns, failureText, filterRuns, formatDuration, runDuration, runFilter, RUN_FILTERS, runLabel, runView, type RunView} from './runs';
import {Button, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Tag} from './ui';
import '../attention/attention.css';

const PER_PAGE = 25;
const when = (iso: string) => { const t = Date.parse(iso); return Number.isFinite(t) ? new Date(t).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'}) : plainText(iso, 40); };

export function RunTable({runs, showHost = true}: {runs: RunSummary[]; showHost?: boolean}) {
  const now = useNow(30_000);
  const [view, setView] = useState<RunView | 'all'>(() => runFilter(hashQuery().get('status')));
  const [query, setQuery] = useState(() => hashQuery().get('q') || '');
  const [page, setPage] = useState(() => Math.max(0, (Number(hashQuery().get('page')) || 1) - 1));
  const sync = (next: {status?: string; q?: string; page?: number}) => {
    const cur = {status: view === 'all' ? undefined : view, q: query || undefined, page: page ? page + 1 : undefined};
    replaceHashQuery({...cur, ...next});
  };
  const counts = useMemo(() => countRuns(runs, now), [runs, now]);
  const derived = runs.some(r => r.status);
  const shown = paginate(filterRuns(runs, view, query, now), page, PER_PAGE);
  return (
    <div className="stack run-table">
      <div className="run-filters">
        {derived ? (
          <div className="seg" role="group" aria-label="Filter runs by status">
            {RUN_FILTERS.filter(([k]) => k === 'all' || counts[k] > 0 || k === view).map(([k, l]) => (
              <button key={k} type="button" className="seg-button" aria-pressed={view === k} onClick={() => { setView(k); setPage(0); sync({status: k === 'all' ? undefined : k, page: undefined}); }}>
                {l} <span className="num">{counts[k]}</span>
              </button>
            ))}
          </div>
        ) : <p className="subtle-text">This controller does not report run status yet; open a run for its report.</p>}
        <label className="run-search"><span className="visually-hidden">Search runs</span>
          <input className="text-input" type="search" placeholder="Search runs" value={query} onChange={e => { setQuery(e.target.value); setPage(0); sync({q: e.target.value || undefined, page: undefined}); }} /></label>
      </div>
      {!shown.rows.length ? <p className="empty">No runs match.</p> : (
        <Table>
          <TableHeader><TableRow><TableHead>Run</TableHead><TableHead className="run-status-col">Status</TableHead><TableHead>Objective</TableHead><TableHead className="run-opt">Tasks</TableHead><TableHead>Duration</TableHead><TableHead className="run-opt">Collected</TableHead></TableRow></TableHeader>
          <TableBody>{shown.rows.map(run => {
            const v = runView(run, now), [label, variant] = runLabel(v);
            const failure = failureText(run);
            return (
              <TableRow key={run.id} data-run={run.id}>
                <TableCell><a className="row-title" href={runHref(run.id)}>{run.id}</a>{showHost ? <span className="row-sub run-host">{run.host}</span> : null}<span className="run-inline-status"><Tag size="sm" variant={variant} text={label} /></span></TableCell>
                <TableCell className="run-status-col"><Tag size="sm" variant={variant} text={label} /></TableCell>
                <TableCell>
                  <span className="run-objective">{run.objective}</span>
                  {failure ? <span className="run-failure">{run.failure?.title ? `${plainText(run.failure.title, 120)} failed at ${plainText(failure, 300)}` : plainText(failure, 300)}</span> : null}
                  {v === 'stuck' ? <span className="run-failure">No attempt started or ended for over {formatDuration(now - (Date.parse(run.lastActivity || run.startedAt || '') || now))}.</span> : null}
                  {run.pendingGates ? <span className="run-note">{run.pendingGates === 1 ? 'One decision waits' : `${run.pendingGates} decisions wait`} on you.</span> : null}
                </TableCell>
                <TableCell className="num run-opt">{run.failed ? `${run.failed} of ${run.tasks} failed` : run.status === 'succeeded' ? `${run.tasks} done` : run.tasks}</TableCell>
                <TableCell className="num">{formatDuration(runDuration(run, now)) || <span className="subtle-text">not recorded</span>}</TableCell>
                <TableCell className="run-opt">{when(run.collectedAt)}</TableCell>
              </TableRow>
            );
          })}</TableBody>
        </Table>
      )}
      {shown.pages > 1 ? (
        <div className="pager">
          <Button size="small" variant="secondary" disabled={shown.page === 0} onClick={() => { setPage(shown.page - 1); sync({page: shown.page || undefined}); }}>Newer</Button>
          <span className="subtle-text">Runs {shown.from} to {shown.to}, page {shown.page + 1} of {shown.pages}</span>
          <Button size="small" variant="secondary" disabled={shown.page >= shown.pages - 1} onClick={() => { setPage(shown.page + 1); sync({page: shown.page + 2}); }}>Older</Button>
        </div>
      ) : null}
    </div>
  );
}
