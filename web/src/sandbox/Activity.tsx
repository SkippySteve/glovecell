import {useCallback, useEffect, useState} from 'react';
import {sandboxActivity, sandboxReports, type Activity, type ActivityItem, type AgentReport} from '../api';
import {plainText, reportKind, REASON_LIMIT, RESOURCE_LIMIT} from '../orca';
import {Button, Callout, Card, CardHeader, Tag, ToggleGroup, ToggleItem} from '../ui';
import {reportTime} from '../workspace/sources/reports';
import type {SandboxContext} from './Sandbox';

const WINDOWS: [string, string][] = [['5', '5 min'], ['15', '15 min'], ['30', '30 min'], ['60', '1 hour']];

function Item({item, act}: {item: ActivityItem; act: SandboxContext['act']}) {
  const [busy, setBusy] = useState(false);
  const blocked = item.blocked;
  return (
    <li className="activity-item">
      <div className="activity-heading"><strong>{item.title}</strong>{item.outcome ? <Tag size="sm" variant={item.outcome === 'Blocked' ? 'danger' : 'success'} text={item.outcome} /> : null}<time>{item.time}</time></div>
      {item.target ? <p className="request-target"><code>{item.target}</code></p> : null}
      {item.count && item.count > 1 ? <p className="subtle">{item.count.toLocaleString()} blocked observations{item.first ? ` since ${item.first}` : ''}</p> : null}
      {item.details?.length ? <dl className="event-details">{item.details.map(d => <div key={d.label}><dt>{d.label}</dt><dd>{d.value}</dd></div>)}</dl> : null}
      {blocked ? blocked.token
        ? <div><Button size="small" variant="primary" disabled={busy} onClick={async () => { setBusy(true); await act('/allow-request', {observation: blocked.token!, tab: 'activity'}, `future ${blocked.method} ${blocked.host}${blocked.path} allowed`); setBusy(false); }}>Allow future requests</Button></div>
        : blocked.reason ? <p className="subtle">{blocked.reason}</p> : null : null}
    </li>
  );
}

// An agent's report that a denial blocks its task. All of it is agent text:
// shown as plain text, never as links or markup.
function Report({report, dismiss}: {report: AgentReport; dismiss: (report: AgentReport) => Promise<void>}) {
  const [busy, setBusy] = useState(false);
  const reason = plainText(report.reason, REASON_LIMIT);
  return (
    <li className="activity-item" data-report={report.id}>
      <div className="activity-heading"><strong>{reportKind(report.kind)}</strong><Tag size="sm" variant="warning" text="Agent blocked" /><time dateTime={plainText(report.last, 40)}>{reportTime(report.last)}</time></div>
      <p className="request-target"><code>{plainText(report.resource, RESOURCE_LIMIT)}</code></p>
      {reason ? <blockquote className="agent-reason">{reason}</blockquote> : null}
      <p className="subtle">{report.count > 1 ? `Reported ${report.count.toLocaleString()} times since ${reportTime(report.first)}. ` : ''}Written by the agent; it cannot approve anything. Grant access on the Internet or LAN devices page if it is needed.</p>
      <div><Button size="small" variant="secondary" disabled={busy} onClick={async () => { setBusy(true); await dismiss(report); setBusy(false); }}>Dismiss</Button></div>
    </li>
  );
}

export function ActivityTab({detail, act}: SandboxContext) {
  const [minutes, setMinutes] = useState('15');
  const [data, setData] = useState<Activity | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  // Null hides the section: older sandbox-control releases have no agent reports.
  const [reports, setReports] = useState<AgentReport[] | null>(null);
  const [reportError, setReportError] = useState('');
  const loadReports = useCallback(async () => {
    try { setReports(await sandboxReports(detail.id)); setReportError(''); } catch (e) { setReportError((e as Error).message); }
  }, [detail.id]);
  const load = useCallback(async () => {
    setLoading(true); setError('');
    loadReports();
    try { setData(await sandboxActivity(detail.id, Number(minutes))); } catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  }, [detail.id, minutes, loadReports]);
  const dismiss = useCallback(async (report: AgentReport) => {
    if (await act('/dismiss-report', {report: String(report.id), tab: 'activity'}, 'agent report dismissed')) await loadReports();
  }, [act, loadReports]);
  useEffect(() => { load(); }, [load]);
  return (
    <div className="stack">
      <div className="section-intro">
        <div className="field"><span className="field-label" id="activity-window">Last</span>
          <ToggleGroup type="single" value={minutes} onValueChange={v => v && setMinutes(v)} aria-labelledby="activity-window" className="durations">
            {WINDOWS.map(([v, l]) => <ToggleItem key={v} value={v} aria-label={'Last ' + l}>{l}</ToggleItem>)}
          </ToggleGroup></div>
        <div className="actions">
          <Button variant="secondary" size="small" disabled={loading} onClick={load}>{loading ? 'Loading…' : 'Refresh'}</Button>
          {data?.links.grafana ? <a className="link-button" href={data.links.grafana} target="_blank" rel="noopener noreferrer">Grafana<span className="visually-hidden"> (opens in a new tab)</span></a> : null}
          {data?.links.openlit ? <a className="link-button" href={data.links.openlit} target="_blank" rel="noopener noreferrer">OpenLIT<span className="visually-hidden"> (opens in a new tab)</span></a> : null}
        </div>
      </div>
      {reportError ? <Callout variant="danger" title="Agent reports unavailable" description={reportError} /> : null}
      {reports?.length ? (
        <Card variant="sunk" width="full" className="activity-source agent-reports">
          <CardHeader title={`Agent reports: blocked (${reports.length})`} subtitle="Agents in this sandbox say a denial stops their task. Review each one; dismissing changes no access." />
          <ul className="activity-list">{reports.map(r => <Report key={r.id} report={r} dismiss={dismiss} />)}</ul>
        </Card>) : null}
      {error ? <Callout variant="danger" title="Activity unavailable" description={error} /> : null}
      {!data ? (loading ? <p className="empty">Querying logs and traces…</p> : null) : <>
        {data.health.cards.length || data.health.error ? (
          <Card variant="sunk" width="full"><CardHeader title="Worker health" subtitle="Latest resource measurements." />
            {data.health.error ? <p className="subtle">{data.health.error}</p> : null}
            <div className="health">{data.health.cards.map(c => <div className="health-card" key={c.title}><strong>{c.title}</strong><span>{c.target}</span></div>)}</div>
          </Card>) : null}
        {data.sources.map(s => (
          <Card key={s.source} variant="sunk" width="full" className="activity-source">
            <CardHeader title={`${s.source} (${s.items.length})`} subtitle={s.scope} />
            {s.error ? <p className="subtle">{s.error}</p> : null}
            {s.items.length ? <ul className="activity-list">{s.items.map((item, i) => <Item key={i} item={item} act={act} />)}</ul> : null}
          </Card>
        ))}
      </>}
    </div>
  );
}
