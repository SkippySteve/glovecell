import {useCallback, useEffect, useState, type ReactNode} from 'react';
import {orcaProjects, type OrcaProject} from '../../api';
import {attentionHref, orcaHref, sandboxHref, withQuery, type Section} from '../../router';
import {useData} from '../../data';
import {listRuns, type RunSummary} from '../../factory/api';
import {runHref} from '../../factory/href';
import {formatDuration, runsFor, runView} from '../../factory/runs';
import {mergeHistory} from '../sources/history';
import type {LanView} from '../sources/lan';
import {useRestrict} from './Lan';
import '../../attention/attention.css';
import {plainText, projectPreview, reportKind, REASON_LIMIT, RESOURCE_LIMIT} from '../../orca';
import {Button, Card, CardHeader, Tag} from '../../ui';
import {timeOf} from '../../sandbox/Sandbox';
import {plural} from '../bits';
import {setLanPrefill} from '../prefill';
import {decideRequest, loadRequests, requestUrl, webAccess, type Requests} from '../sources/internet';
import {liveLinks, pendingLinks} from '../sources/previews';
import {dismissReport, reportLanHost, reportRequests, reportTime, type AgentReport} from '../sources/reports';
import type {SandboxState} from '../useSandbox';
import {ago, clockAt, left, stamp} from '../time';
import type {SandboxDetail} from '../../api';
import {AccessLevelTag} from '../../access/AccessLevels';
import {levelText, type LevelState, type SandboxRules} from '../../access/levels';
import {detailLevel} from '../../access/summary';

const SHOWN_REQUESTS = 3;

function ReportRow({ws, report}: {ws: SandboxState; report: AgentReport}) {
  const [busy, setBusy] = useState(false);
  const lan = reportLanHost(report, ws.lan || null);
  const web = reportRequests(report);
  const reason = plainText(report.reason, REASON_LIMIT);
  return (
    <li data-report={report.id}>
      <Tag size="sm" variant="danger" text="Agent report" />
      <div className="item-main">
        <strong>{reportKind(report.kind)}: <code>{plainText(report.resource, RESOURCE_LIMIT)}</code></strong>
        <span className="subtle-text">Agent's reason (unverified): {reason || 'none given'}</span>
        <span className="subtle-text">{plural(report.count, 'report')}, latest {reportTime(report.last)}</span>
        {lan ? <span>{lan.open ? <>LAN access to <code>{lan.address}</code> is open.</> : <>Names allowlisted LAN host <code>{lan.address}</code>.</>}</span> : null}
        {web ? <span className={web.note ? 'warning-text' : 'subtle-text'}>{web.note || `Recorded requests matching “${web.filter}”: ${web.waiting} waiting, ${web.approved} approved.`}</span> : null}
      </div>
      <div className="actions">
        {lan && !lan.open ? <a className="link-button primary-link" href={sandboxHref(ws.id, 'lan')} onClick={() => setLanPrefill(ws.id, lan.address, report)}>Open LAN access</a> : null}
        {web ? <a className="link-button primary-link" href={withQuery(sandboxHref(ws.id, 'internet'), {q: web.filter})}>Find it in Internet</a> : null}
        <Button size="small" variant="tertiary" disabled={busy} onClick={async () => { setBusy(true); const f = dismissReport(report); await ws.act(f.path, f.fields, 'agent report dismissed'); await ws.reloadReports(); setBusy(false); }}>Dismiss</Button>
      </div>
    </li>
  );
}

function useOrca(enabled: boolean) {
  const [records, setRecords] = useState<OrcaProject[] | null>(null);
  useEffect(() => { if (enabled) orcaProjects().then(setRecords, () => setRecords([])); }, [enabled]);
  return records;
}

// The sandbox's access: its level (the same as the Internet page's level
// control and Home), open LAN grants, and what that means right now.
export function accessSummary(d: SandboxDetail, lan: LanView | null | undefined, now: number, rules: SandboxRules['rules']): {state: LevelState; lan: string; detail: string} {
  const state = detailLevel(d, rules), web = webAccess(d);
  const lanOpen = lan?.open || 0;
  const lanText = lanOpen ? `Plus ${plural(lanOpen, 'open LAN grant')}.` : '';
  if (!state.supported) return {state, lan: lanText, detail: 'Requests follow the policy profile; there is no web window to open.'};
  if (web && state.level === 'open') return {state, lan: lanText, detail: state.learning
    ? `Every request becomes an approval until ${clockAt(web.expires)}, ${left(web.expires, now)} left.`
    : `Any public destination until ${clockAt(web.expires)}, ${left(web.expires, now)} left.`};
  const approved = d.egressCounts?.approved || 0;
  return {state, lan: lanText, detail: state.level ? `${levelText(state.level, 'sandbox').summary}${approved ? ` ${plural(approved, 'approved request')}.` : ''}` : state.note || ''};
}

// What the sandbox is doing now, from facts the console has: its runs, live dev
// links, LAN grants and the newest recorded event.
function useRuns(namespace: string, enabled: boolean) {
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  useEffect(() => { let live = true; if (enabled) listRuns().then(all => { if (live) setRuns(runsFor(all, namespace)); }, () => { if (live) setRuns(null); }); return () => { live = false; }; }, [namespace, enabled]);
  return runs;
}

function Summary({ws, now, waitingCount, rules}: {ws: SandboxState; now: number; waitingCount: number; rules: SandboxRules['rules']}) {
  const d = ws.detail!, lan = ws.lan, web = webAccess(d);
  const {session, egress, busy} = useData();
  const restrict = useRestrict(ws);
  const runs = useRuns(d.namespace, Boolean(session.runRecords));
  const access = accessSummary(d, lan, now, rules);
  const running = d.mode === 'Running';
  const active = (runs || []).filter(r => r.status === 'running' || r.status === 'waiting');
  const live = liveLinks(ws.devlinks);
  const grants = lan?.hosts.filter(h => h.grant) || [];
  const latest = mergeHistory({lan, devlinks: ws.devlinks, revisions: d.revisions, reports: ws.reports})[0];
  const doing: ReactNode[] = [];
  if (!running) doing.push(<li key="stopped">Stopped. Storage is kept; start it from the header.</li>);
  for (const r of active.slice(0, 2)) {
    const view = runView(r, now);
    doing.push(<li key={r.id}><a href={runHref(r.id)}>Run {r.id}</a>{view === 'waiting' ? ' is waiting on your decision' : view === 'stuck' ? ` has recorded no progress for ${formatDuration(now - (Date.parse(r.lastActivity || r.startedAt || '') || now))}` : ` is running ${plural(r.running || 0, 'task')}`}: {plainText(r.objective, 120)}</li>);
  }
  for (const l of live.slice(0, 2)) doing.push(<li key={'dl' + l.name}>Serving dev link <a href={l.url} target="_blank" rel="noopener noreferrer">{l.host}</a></li>);
  for (const h of grants.slice(0, 2)) doing.push(<li key={'lan' + h.address}>LAN access to <code>{h.address}</code> {h.grant!.state === 'Open' && h.grant!.deadline ? `for ${left(h.grant!.deadline, now)} more` : h.grant!.state.toLowerCase()}</li>);
  if (running && !doing.length) doing.push(<li key="idle" className="subtle-text">No run, dev link or LAN grant is active.</li>);
  return (
    <section className="ov-summary" aria-labelledby="ov-summary-title">
      <div className="ov-state">
        <h2 id="ov-summary-title">Summary</h2>
        <dl className="ov-facts">
          <dt>State</dt>
          <dd><Tag size="sm" variant={d.revision.error ? 'danger' : running ? 'success' : 'neutral'} text={d.revision.error ? 'Policy error' : running ? 'Running' : 'Stopped'} /><span className="ov-meta">Policy revision {d.revision.id}, {d.revision.status.toLowerCase()}</span></dd>
          <dt>Access</dt>
          <dd><AccessLevelTag state={access.state} now={now} />{access.lan ? <span className="ov-access-lan">{access.lan}</span> : null}<span className="ov-meta">{access.detail}</span></dd>
          <dt>Doing now</dt>
          <dd><ul className="ov-now">{doing}</ul></dd>
          <dt>Waiting on you</dt>
          <dd>{waitingCount ? <a href={attentionHref()}>{plural(waitingCount, 'item')}</a> : <span className="subtle-text">Nothing</span>}</dd>
          {latest ? <><dt>Last event</dt><dd>{latest.title}<span className="ov-meta">{ago(latest.at, now)}</span></dd></> : null}
        </dl>
      </div>
      <div className="ov-quick">
        <h3>Quick actions</h3>
        <div className="ov-quick-actions">
          {web && web.phase !== 'Locked' ? <Button size="small" variant="primary" buttonType="destructive" disabled={busy.has(d.id)} onClick={() => egress([d.id], 'lock', web.minutes)}>Lock web access</Button> : null}
          {grants.filter(h => h.grant!.state !== 'Restricting').slice(0, 1).map(h => <Button key={h.address} size="small" variant="secondary" buttonType="destructive" onClick={() => restrict(h.address, lan!)}>Restrict LAN {h.address}</Button>)}
          {(d.egressCounts?.pending || 0) ? <a className="link-button" href={sandboxHref(ws.id, 'internet')}>Review {plural(d.egressCounts!.pending, 'request')}</a> : null}
          {web && web.phase === 'Locked' ? <a className="link-button" href={sandboxHref(ws.id, 'internet')}>Change web access</a> : null}
          {lan?.enabled && !grants.length ? <a className="link-button" href={sandboxHref(ws.id, 'lan')}>Open LAN access</a> : null}
          <a className="link-button" href={sandboxHref(ws.id, 'downloads')}>Put a file in the sandbox</a>
          {session.runRecords ? <a className="link-button" href={sandboxHref(ws.id, 'runs')}>Runs</a> : null}
        </div>
        <dl className="ov-facts ov-ids">
          <dt>Namespace</dt><dd><code>{d.namespace}</code></dd>
          <dt>Sandbox</dt><dd><code>{d.sandbox}</code></dd>
          {d.revision.at ? <><dt>Policy since</dt><dd>{timeOf(d.revision.at)}</dd></> : null}
        </dl>
      </div>
    </section>
  );
}

const RECENT = 5;
function RecentActivity({ws, now}: {ws: SandboxState; now: number}) {
  const d = ws.detail!;
  const events = mergeHistory({lan: ws.lan, devlinks: ws.devlinks, revisions: d.revisions, reports: ws.reports}).slice(0, RECENT);
  return (
    <section className="ov-recent" aria-labelledby="ov-recent-title">
      <div className="ov-recent-head"><h2 id="ov-recent-title">Recent activity</h2><a href={sandboxHref(ws.id, 'history')}>All history</a></div>
      {events.length ? <ol aria-label="Recent activity">{events.map((e, i) => (
        <li key={e.type + e.at + i}><time dateTime={e.at ? new Date(e.at).toISOString() : undefined} title={stamp(e.at)}>{e.at ? ago(e.at, now) : 'time not recorded'}</time>
          <div className="ov-event"><strong>{e.title}</strong>{e.detail ? <span>{e.detail}</span> : null}{e.by ? <span>By {e.by}</span> : null}</div></li>
      ))}</ol> : <p className="subtle-text">No recorded events yet.</p>}
    </section>
  );
}

const waiting = (d: SandboxDetail) => (d.egressCounts?.pending || 0) + d.previewPending + (d.devLinkPending || 0);

export function Overview({ws, runtime, now}: {ws: SandboxState; runtime: boolean; now: number}) {
  const detail = ws.detail!;
  const [requests, setRequests] = useState<Requests | null>(null);
  const [busy, setBusy] = useState('');
  const load = useCallback(async () => { if (detail.learningEgress) try { setRequests(await loadRequests(detail.id, '')); } catch { setRequests(null); } }, [detail.id, detail.learningEgress]);
  useEffect(() => { load(); }, [load, detail.egressCounts?.pending]);
  const orca = useOrca(runtime);
  const href = (s: Section) => sandboxHref(ws.id, s);
  const reports = ws.reports || [];
  const devPending = pendingLinks(ws.devlinks);
  const pending = requests?.pending.items.slice(0, SHOWN_REQUESTS) || [];
  const decisions = reports.length + pending.length + devPending.length + (detail.previewPending ? 1 : 0);
  const orcaList = orca ? projectPreview(orca) : null;
  return (
    <div className="stack">
      <Summary ws={ws} now={now} waitingCount={waiting(detail) + reports.length} rules={requests ? (requests.rules ?? null) : undefined} />
      <Card variant="sunk" width="full" className="decisions" data-testid="needs-decision">
        <CardHeader title="Needs a decision" subtitle={decisions ? `${decisions} item${decisions === 1 ? '' : 's'} waiting on you in ${ws.id}.` : undefined} />
        <ul className="items decision-list">
          {reports.map(r => <ReportRow key={r.id} ws={ws} report={r} />)}
          {pending.map(o => (
            <li key={o.observation}>
              <Tag size="sm" variant="info" text="Request" />
              <div className="item-main"><span className="request"><span className="method">{o.method}</span><code>{requestUrl(o)}</code></span></div>
              <Button size="small" variant="primary" disabled={busy === o.observation} onClick={async () => { setBusy(o.observation); const f = decideRequest(o, true); if (await ws.act(f.path, f.fields, 'request approved')) await load(); setBusy(''); }}>{(o.fingerprints ?? 1) > 1 ? `Approve all ${o.fingerprints}` : 'Approve'}</Button>
            </li>
          ))}
          {(detail.egressCounts?.pending || 0) > pending.length ? <li><span className="subtle-text">{plural((detail.egressCounts?.pending || 0) - pending.length, 'more request')} awaiting approval.</span><a href={href('internet')}>Review on Internet</a></li> : null}
          {devPending.map(l => (
            <li key={l.Name}>
              <Tag size="sm" variant="success" text="Dev link" />
              <div className="item-main"><strong>{l.Name}</strong><span className="subtle-text">{l.Scan.Status === 'Complete' ? `Scan complete: ${l.Counts.critical} critical, ${l.Counts.high} high` : 'Scan ' + l.Scan.Status.toLowerCase()}</span></div>
              <a className="link-button" href={href('previews')}>Review</a>
            </li>
          ))}
          {detail.previewPending ? <li><Tag size="sm" variant="warning" text="Preview" /><div className="item-main"><strong>{plural(detail.previewPending, 'detected server')} awaiting review</strong></div><a className="link-button" href={href('previews')}>Review</a></li> : null}
          {!decisions ? <li className="empty-item">Nothing is waiting on you in this sandbox.</li> : null}
        </ul>
      </Card>
      <RecentActivity ws={ws} now={now} />
      {runtime && orcaList ? (
        <Card variant="sunk" width="full">
          <CardHeader title="Orca projects" subtitle="Repositories published into this Orca runtime." />
          <p>{orcaList.total ? `${orcaList.names.join(', ')}${orcaList.more ? ` +${orcaList.more} more` : ''}` : 'No projects in Orca yet.'} <a href={orcaHref()}>Manage Orca projects</a></p>
        </Card>
      ) : null}
    </div>
  );
}
