import {useCallback, useEffect, useState} from 'react';
import {sandboxActivity, type Activity, type ActivityItem} from '../api';
import {Button, Callout, Card, CardHeader, Tag, ToggleGroup, ToggleItem} from '../ui';
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

export function ActivityTab({detail, act}: SandboxContext) {
  const [minutes, setMinutes] = useState('15');
  const [data, setData] = useState<Activity | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const load = useCallback(async () => {
    setLoading(true); setError('');
    try { setData(await sandboxActivity(detail.id, Number(minutes))); } catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  }, [detail.id, minutes]);
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
          {data?.links.grafana ? <a className="link-button" href={data.links.grafana} target="_blank" rel="noopener noreferrer">Grafana ↗</a> : null}
          {data?.links.openlit ? <a className="link-button" href={data.links.openlit} target="_blank" rel="noopener noreferrer">OpenLIT ↗</a> : null}
        </div>
      </div>
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
