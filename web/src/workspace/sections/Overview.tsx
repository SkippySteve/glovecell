import {useCallback, useEffect, useState} from 'react';
import {orcaProjects, type OrcaProject} from '../../api';
import {orcaHref, sandboxHref, type Section} from '../../router';
import {plainText, projectPreview, reportKind, REASON_LIMIT, RESOURCE_LIMIT} from '../../orca';
import {Button, Card, CardHeader, Tag} from '../../ui';
import {timeOf} from '../../sandbox/Sandbox';
import {plural} from '../bits';
import {setLanPrefill} from '../prefill';
import {decideRequest, loadRequests, requestUrl, webAccess, type Requests} from '../sources/internet';
import {liveLinks, pendingLinks} from '../sources/previews';
import {dismissReport, reportLanHost, reportTime, type AgentReport} from '../sources/reports';
import type {SandboxState} from '../useSandbox';
import {clockAt, left} from '../time';
import type {SandboxDetail} from '../../api';

const SHOWN_REQUESTS = 3;

function ReportRow({ws, report}: {ws: SandboxState; report: AgentReport}) {
  const [busy, setBusy] = useState(false);
  const lan = reportLanHost(report, ws.lan || null);
  const reason = plainText(report.reason, REASON_LIMIT);
  return (
    <li data-report={report.id}>
      <Tag size="sm" variant="danger" text="Agent report" />
      <div className="item-main">
        <strong>{reportKind(report.kind)}: <code>{plainText(report.resource, RESOURCE_LIMIT)}</code></strong>
        <span className="subtle-text">Agent's reason (unverified): {reason || '—'} · {plural(report.count, 'report')}, latest {reportTime(report.last)}</span>
        {lan ? <span>{lan.open ? <>LAN access to <code>{lan.address}</code> is open.</> : <>Names allowlisted LAN host <code>{lan.address}</code>.</>}</span> : null}
      </div>
      <div className="actions">
        {lan && !lan.open ? <a className="link-button primary-link" href={sandboxHref(ws.id, 'lan')} onClick={() => setLanPrefill(ws.id, lan.address, report)}>Open LAN access →</a> : null}
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

function Tile({href, label, value, sub, badge, live}: {href: string; label: string; value: string; sub: string; badge?: number; live?: boolean}) {
  return (
    <a className="tile" href={href}>
      <span className="tile-label">{label}{badge ? <span className={'count-badge' + (live ? ' live' : '')}>{badge}</span> : null}</span>
      <span className="tile-value">{value}</span>
      <span className="subtle-text">{sub}</span>
    </a>
  );
}

const waiting = (d: SandboxDetail) => (d.egressCounts?.pending || 0) + d.previewPending + (d.devLinkPending || 0);

export function Overview({ws, runtime, now}: {ws: SandboxState; runtime: boolean; now: number}) {
  const detail = ws.detail!;
  const web = webAccess(detail);
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
  const lan = ws.lan;
  const live = liveLinks(ws.devlinks);
  const orcaList = orca ? projectPreview(orca) : null;
  return (
    <div className="stack">
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
              <div className="item-main"><strong>{l.Name}</strong><span className="subtle-text">{l.Scan.Status === 'Complete' ? `Scan complete · ${l.Counts.critical} critical · ${l.Counts.high} high` : 'Scan ' + l.Scan.Status.toLowerCase()}</span></div>
              <a className="link-button" href={href('previews')}>Review</a>
            </li>
          ))}
          {detail.previewPending ? <li><Tag size="sm" variant="warning" text="Preview" /><div className="item-main"><strong>{plural(detail.previewPending, 'detected server')} awaiting review</strong></div><a className="link-button" href={href('previews')}>Review</a></li> : null}
          {!decisions ? <li className="empty-item">Nothing is waiting on you in this sandbox.</li> : null}
        </ul>
      </Card>
      <nav className="tiles" aria-label="Sections">
        <Tile href={href('internet')} label="Internet" badge={detail.egressCounts?.pending}
          value={web ? 'Web ' + web.phase.toLowerCase() : 'Fixed rules'}
          sub={!web ? 'Policy rules from sandbox-control' : web.phase === 'Locked' ? `${plural(detail.egressCounts?.approved || 0, 'approved request')} · ${detail.egressCounts?.pending || 0} waiting` : `until ${clockAt(web.expires)} · ${left(web.expires, now)} left`} />
        <Tile href={href('lan')} label="LAN devices" badge={lan?.open} live
          value={lan === undefined ? 'Loading…' : lan === null ? 'Not available yet' : lan.open ? `${lan.open} open` : 'Closed'}
          sub={lan ? `${plural(lan.hosts.filter(h => h.listed).length, 'allowlisted host')} · time-limited grants` : 'Direct TCP to home-network devices'} />
        <Tile href={href('packages')} label="Packages" value={detail.learningEgress ? 'Registry downloads' : 'Package lists'} sub="npm · Go · Python · Rust" />
        <Tile href={href('previews')} label="Previews & dev links" badge={detail.previewPending + (detail.devLinkPending || 0)} value={`${live.length} live`} sub={`${detail.previewPending + (detail.devLinkPending || 0)} waiting for review`} />
        <Tile href={href('credentials')} label="Credentials" value="Identity & grants" sub="Secret values are never shown" />
        <Tile href={href('history')} label="History" value={plural(detail.revisions.length + (lan?.history.length || 0), 'event')} sub="Decisions and grant changes" />
      </nav>
      {runtime && orcaList ? (
        <Card variant="sunk" width="full">
          <CardHeader title="Orca projects" subtitle="Repositories published into this Orca runtime." />
          <p>{orcaList.total ? `${orcaList.names.join(', ')}${orcaList.more ? ` +${orcaList.more} more` : ''}` : 'No projects in Orca yet.'} <a href={orcaHref()}>Manage Orca projects</a></p>
        </Card>
      ) : null}
      <dl className="facts">
        {([['Namespace', detail.namespace], ['Sandbox', detail.sandbox], ['Policy revision', `${detail.revision.id} · ${detail.revision.status}${detail.revision.at ? ' · ' + timeOf(detail.revision.at) : ''}`], ['Waiting on you', String(waiting(detail) + reports.length)]] as [string, string][])
          .map(([term, value]) => <div key={term}><dt>{term}</dt><dd>{value}</dd></div>)}
      </dl>
    </div>
  );
}
