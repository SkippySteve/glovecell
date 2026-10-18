import {useCallback, useEffect, useState} from 'react';
import {sandboxPreviews, type Preview} from '../api';
import {useData} from '../data';
import {Button, Card, Tag, TextArea, type TagVariant} from '../ui';
import {timeOf, type SandboxContext} from './Sandbox';

const STATE: Record<string, TagVariant> = {Approved: 'success', Pending: 'warning', 'Needs network binding': 'warning', Observing: 'info', Denied: 'danger', Revoked: 'danger', Expired: 'neutral'};

function PreviewCard({p, tailnetReady, act, reload}: {p: Preview; tailnetReady: boolean; act: SandboxContext['act']; reload: () => Promise<void>}) {
  const [rules, setRules] = useState(p.rulesText || p.proposedRulesText);
  const [lan, setLan] = useState(p.state === 'Approved' ? p.lan : false);
  const [tail, setTail] = useState(p.state === 'Approved' ? p.tailscale : tailnetReady);
  const [gaps, setGaps] = useState(p.gapAcknowledged);
  const [busy, setBusy] = useState(false);
  const decide = async (state: 'Approved' | 'Denied' | 'Revoked') => {
    setBusy(true);
    const fields: Record<string, string> = {id: p.id, state, paths_text: rules, tab: 'previews'};
    if (lan) fields.lan = 'on';
    if (tail) fields.tail = 'on';
    if (gaps) fields.gaps = 'on';
    if (await act('/decision', fields, `${p.proposedName || 'preview'} ${state.toLowerCase()}`)) await reload();
    setBusy(false);
  };
  const approvable = ['Pending', 'Denied', 'Revoked'].includes(p.state) && !p.listener.loopback;
  return (
    <Card variant="sunk" width="full" className="preview" data-preview={p.id}>
      <div className="preview-head">
        <div><h3>{p.proposedName || 'Unnamed server'} <span className="subtle">· port {p.listener.port}</span></h3>
          <p className="muted">{p.listener.project} / {p.listener.worktree} · PID {p.listener.pid} · {p.listener.address} · seen {timeOf(p.seen)}</p></div>
        <Tag size="sm" variant={STATE[p.state] || 'neutral'} text={p.state} />
      </div>
      {p.state === 'Approved' ? <p className="preview-links"><a href={p.urls.lan} target="_blank" rel="noopener noreferrer">{p.urls.lan}</a>{p.tailscale ? <a href={p.urls.tailnet} target="_blank" rel="noopener noreferrer">{p.urls.tailnet}</a> : null}</p> : null}
      {p.listener.loopback ? <p className="subtle">Bound to loopback. Ask the agent to bind a sandbox interface before approving.</p> : null}
      <div className="field"><label className="field-label" htmlFor={'rules-' + p.id}>Allowed paths, one per line: METHODS [prefix:]/path</label>
        <TextArea id={'rules-' + p.id} rows={2} value={rules} onChange={e => setRules(e.target.value)} /></div>
      <div className="chips">
        <label className="check"><input type="checkbox" checked={lan} onChange={e => setLan(e.target.checked)} /> LAN</label>
        <label className="check"><input type="checkbox" checked={tail} disabled={!tailnetReady} onChange={e => setTail(e.target.checked)} /> Tailscale</label>
        <label className="check"><input type="checkbox" checked={gaps} onChange={e => setGaps(e.target.checked)} /> I reviewed observation gaps</label>
      </div>
      <div className="actions">
        {approvable ? <Button variant="primary" size="small" disabled={busy || !gaps || (!lan && !tail)} onClick={() => decide('Approved')}>Approve</Button> : null}
        {p.state === 'Approved' ? <Button variant="primary" size="small" disabled={busy || !gaps || (!lan && !tail)} onClick={() => decide('Approved')}>Update</Button> : null}
        {p.state === 'Approved' ? <Button variant="secondary" buttonType="destructive" size="small" disabled={busy} onClick={() => decide('Revoked')}>Revoke</Button>
          : p.state !== 'Denied' ? <Button variant="tertiary" size="small" disabled={busy} onClick={() => decide('Denied')}>Deny</Button> : null}
      </div>
    </Card>
  );
}

export function PreviewsTab({detail, act}: SandboxContext) {
  const {toast} = useData();
  const [expired, setExpired] = useState(false);
  const [data, setData] = useState<{expiredCount: number; requests: Preview[]} | null>(null);
  const load = useCallback(async () => {
    try { setData(await sandboxPreviews(detail.id, expired)); } catch (e) { toast((e as Error).message, true); }
  }, [detail.id, expired, toast]);
  useEffect(() => { load(); }, [load, detail.previewPending]);
  return (
    <div className="stack">
      <div className="section-intro">
        <p className="muted">Servers the agent starts inside the sandbox. Approval exposes the observed process and socket; it ends when the process stops or is replaced.</p>
        {data?.expiredCount ? <Button variant="tertiary" size="small" onClick={() => setExpired(!expired)}>{expired ? 'Hide inactive' : `Show inactive (${data.expiredCount})`}</Button> : null}
      </div>
      {!data ? <p className="empty">Loading servers…</p> : data.requests.length
        ? data.requests.map(p => <PreviewCard key={p.id + p.state} p={p} tailnetReady={detail.tailnetReady} act={act} reload={load} />)
        : <Card variant="sunk" width="full" className="empty-card"><p className="muted">No servers detected. They appear here when the agent listens on a port.</p></Card>}
    </div>
  );
}
