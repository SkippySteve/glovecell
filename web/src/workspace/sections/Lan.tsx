import {useEffect, useRef, useState} from 'react';
import {classicHref} from '../../sandbox/Sandbox';
import {plainText, REASON_LIMIT} from '../../orca';
import {Button, Callout, Tag, ToggleGroup, ToggleItem} from '../../ui';
import {Countdown, NotAvailable, plural, SectionIntro, WindowBar} from '../bits';
import {useConfirm} from '../Confirm';
import {takeLanPrefill, type LanPrefill} from '../prefill';
import {chosenPorts, grantLabel, lanExtend, lanForget, lanOpen, lanRestrict, portChips, windowLabel, type LanHost, type LanView, type OpenMode} from '../sources/lan';
import {lanEventTitle} from '../sources/history';
import {ago, clockAt, stamp} from '../time';
import type {SandboxState} from '../useSandbox';

const hostId = (address: string) => 'lan-host-' + address.replace(/\./g, '-');

// The confirmation every pod-restarting LAN control goes through (sandbox-control #257).
export function useRestrict(ws: SandboxState) {
  const ask = useConfirm();
  return async (address: string, lan: LanView) => {
    const open = lan.hosts.filter(h => h.grant).length;
    const ok = await ask(`Restrict ${address} now?`, <>
      <p>This removes the LAN rule and <strong>restarts the {ws.id} workspace pod</strong> to close open connections.</p>
      <p className="danger-box">It ends <strong>every LAN grant of {ws.id}</strong> ({open} open) and <strong>its agent sessions</strong>: running commands stop and terminals disconnect. Workspace files are preserved.</p>
      <p className="muted">Web access and dev links are not affected.</p>
    </>, 'Restrict and restart pod');
    if (!ok) return false;
    const f = lanRestrict(address, lan.podUid);
    return ws.act(f.path, f.fields, `${address}: LAN access restricting; the workspace pod restarts`);
  };
}

function OpenGrant({ws, lan, h, now}: {ws: SandboxState; lan: LanView; h: LanHost; now: number}) {
  const g = h.grant!;
  const restrict = useRestrict(ws);
  const [busy, setBusy] = useState(false);
  const extend = async (seconds: number) => { setBusy(true); const f = lanExtend(h.address, seconds, lan.podUid); await ws.act(f.path, f.fields, `${h.address} open for ${windowLabel(seconds)} from now`); setBusy(false); };
  return <>
    <div className="card-bd">
      <div className="grant-time">
        <span className="big-countdown">{g.state === 'Open' && g.deadline ? <Countdown until={g.deadline} now={now} /> : g.state + '…'}</span>
        <span className="muted">{g.state === 'Open' ? (g.deadline ? `left · until ${clockAt(g.deadline)}` : 'open') : g.state === 'Opening' ? 'applying the rule' : 'removing the rule and restarting the workspace pod'}</span>
      </div>
      {g.state === 'Open' && g.deadline ? <WindowBar until={g.deadline} total={g.deadline - g.openedAt} now={now} tone="bad" label={`Share of the ${h.address} window left`} /> : null}
      <p><strong>{grantLabel(g)}</strong>{g.openedBy ? ` · opened by ${g.openedBy}` : ''}{g.openedAt ? ` at ${clockAt(g.openedAt)}` : ''} · tools need <code>NO_PROXY={h.address}</code></p>
      {g.state !== 'Restricting' && h.listed ? (
        <div className="field"><span className="field-label">Extend: restart the window from now</span>
          <div className="seg" role="group" aria-label={`Extend ${h.address}`}>
            {lan.windows.map(s => <Button key={s} size="small" variant="secondary" disabled={busy || !lan.running || !lan.enabled} onClick={() => extend(s)}>{windowLabel(s)}</Button>)}
          </div></div>
      ) : null}
    </div>
    {g.state !== 'Restricting' ? (
      <div className="card-ft">
        <Button variant="secondary" buttonType="destructive" disabled={busy} onClick={async () => { setBusy(true); await restrict(h.address, lan); setBusy(false); }}>Restrict now</Button>
        <span className="subtle-text">Restarts the workspace pod and ends its agent sessions.</span>
      </div>
    ) : null}
  </>;
}

function ClosedHost({ws, lan, h, prefill}: {ws: SandboxState; lan: LanView; h: LanHost; prefill: LanPrefill | null}) {
  const chips = portChips(h);
  const [mode, setMode] = useState<OpenMode>(h.learnedGrant.length ? 'learned' : 'choose');
  const [picked, setPicked] = useState<number[]>(() => chips.filter(c => c.checked).map(c => c.port));
  const [extra, setExtra] = useState('');
  const [seconds, setSeconds] = useState(String(lan.windows.includes(3600) ? 3600 : lan.windows[0]));
  const [busy, setBusy] = useState(false);
  const chosen = mode === 'choose' ? chosenPorts(picked, h.all ? extra : '', lan.portLimit) : [];
  const error = typeof chosen === 'string' ? chosen : '';
  const full = lan.open >= lan.grantLimit;
  const blocked = !lan.enabled ? (lan.reason || 'LAN host grants are off') : !lan.running ? 'Start the workspace before opening LAN access.' : full ? `A workspace holds at most ${lan.grantLimit} LAN grants.` : '';
  const summary = mode === 'all' ? 'all ports' : mode === 'learned' ? h.learnedGrant.join(', ') : typeof chosen === 'string' ? '–' : chosen.join(', ');
  const until = clockAt(Date.now() + Number(seconds) * 1000);
  const open = async () => {
    setBusy(true);
    const f = lanOpen(h.address, mode, typeof chosen === 'string' ? [] : chosen, Number(seconds), lan.podUid);
    await ws.act(f.path, f.fields, `opening ${h.address} (applying the rule)`);
    setBusy(false);
  };
  const id = hostId(h.address);
  const modes = ([['learned', `Learned ports (${h.learnedGrant.length})`], ['choose', 'Choose ports'], ['all', 'All ports']] as [OpenMode, string][])
    .filter(([v]) => v === 'choose' || (v === 'learned' ? h.learnedGrant.length > 0 : h.all));
  return <>
    <div className="card-bd">
      {prefill ? <p className="hint">From {ws.id}'s blocked report (unverified): “{plainText(prefill.reason, REASON_LIMIT)}”. {h.learnedGrant.length ? 'Learned ports are preselected; ' : ''}the agent cannot open this itself.</p> : null}
      <div className="field"><span className="field-label" id={id + '-mode'}>Ports</span>
        <ToggleGroup type="single" value={mode} onValueChange={v => v && setMode(v as OpenMode)} aria-labelledby={id + '-mode'} className="durations">
          {modes.map(([v, l]) => <ToggleItem key={v} value={v}>{l}</ToggleItem>)}
        </ToggleGroup>
        {mode === 'learned' ? <div className="port-chips">{h.learnedGrant.map(p => <span key={p} className="port-chip">TCP {p}</span>)}</div> : null}
        {mode === 'choose' ? <>
          {chips.length ? <div className="port-chips" role="group" aria-label={`Ports of ${h.address}`}>{chips.map(c => (
            <label key={c.port} className="port-chip"><input type="checkbox" checked={picked.includes(c.port)} onChange={e => setPicked(e.target.checked ? [...picked, c.port] : picked.filter(p => p !== c.port))} /> TCP {c.port}{h.all ? <small>learned</small> : null}</label>
          ))}</div> : null}
          {h.all ? <>
            <label className="field-label" htmlFor={id + '-extra'}>{chips.length ? 'Other TCP ports' : 'TCP ports'}</label>
            <input id={id + '-extra'} className="text-input" inputMode="numeric" placeholder="1337, 2122" value={extra} onChange={e => setExtra(e.target.value)} aria-describedby={id + '-extra-help'} aria-invalid={Boolean(error) || undefined} />
            <small id={id + '-extra-help'} className="subtle-text">Comma-separated, no ranges, at most {lan.portLimit}. FTP needs its control port and its passive data ports.</small>
          </> : null}
        </> : null}
        {mode === 'all' ? <p className="warn-box">Every TCP port on {h.address}. Use when passive ports vary (FTP).</p> : null}
        {error ? <span className="error-text" role="alert">{error}</span> : null}
      </div>
      <div className="field"><span className="field-label" id={id + '-window'}>Window</span>
        <ToggleGroup type="single" value={seconds} onValueChange={v => v && setSeconds(v)} aria-labelledby={id + '-window'} className="durations">
          {lan.windows.map(s => <ToggleItem key={s} value={String(s)} aria-label={'Window ' + windowLabel(s)}>{windowLabel(s)}</ToggleItem>)}
        </ToggleGroup></div>
      <p className="summary-box">Opens <strong>TCP {summary}</strong> from <strong>{ws.id}</strong> to <strong>{h.address}</strong> until <strong>{until}</strong>. Not proxied or inspected; tools need <code>NO_PROXY={h.address}</code>.</p>
    </div>
    <div className="card-ft">
      <Button variant="primary" disabled={busy || Boolean(error) || Boolean(blocked)} onClick={open}>Open for {windowLabel(Number(seconds))}</Button>
      {blocked ? <span className="error-text">{blocked}</span> : null}
    </div>
  </>;
}

function HostCard({ws, lan, h, now, prefill}: {ws: SandboxState; lan: LanView; h: LanHost; now: number; prefill: LanPrefill | null}) {
  const ask = useConfirm();
  const ref = useRef<HTMLElement>(null);
  useEffect(() => { if (prefill) { ref.current?.scrollIntoView({block: 'start'}); ref.current?.focus({preventScroll: true}); } }, [prefill]);
  const forget = async () => {
    if (!await ask(`Forget learned ports of ${h.address}?`, <p>Only the records are removed; nothing opens or closes. Ports are learned again during the next grant.</p>, 'Forget')) return;
    const f = lanForget(h.address);
    await ws.act(f.path, f.fields, `learned ports of ${h.address} forgotten`);
  };
  const state = h.grant?.state;
  return <>
    <section ref={ref} tabIndex={-1} className={'card lan-host' + (prefill ? ' flash' : '')} id={hostId(h.address)} data-lan-host={h.address} aria-labelledby={hostId(h.address) + '-title'}>
      <div className="card-hd">
        <h3 id={hostId(h.address) + '-title'}><code>{h.address}</code></h3>
        {h.all ? <Tag size="sm" text="all TCP ports allowlisted" /> : h.listed ? <Tag size="sm" text={'allowlisted TCP ' + h.ports.join(', ')} /> : <Tag size="sm" variant="warning" text="no longer allowlisted" />}
        <span className="end">{state ? <Tag size="sm" variant="danger" text={state} /> : <Tag size="sm" variant="success" text="Closed" />}</span>
      </div>
      {h.grant ? <OpenGrant ws={ws} lan={lan} h={h} now={now} /> : h.listed ? <ClosedHost ws={ws} lan={lan} h={h} prefill={prefill} /> : null}
    </section>
    {h.learned.length ? (
      <section className="card" aria-label={`Learned ports on ${h.address}`}>
        <div className="card-hd"><h3>Learned ports on {h.address}</h3><span className="end"><Button size="small" variant="tertiary" onClick={forget}>Forget learned ports</Button></span></div>
        <ul className="items rows">{h.learned.map(l => (
          <li key={l.port}><Tag size="sm" variant={l.used ? 'success' : 'danger'} text={l.used ? 'used' : 'refused'} /><div className="item-main"><span><strong>TCP {l.port}</strong> <span className="subtle-text">· {plural(l.count, 'observation')}{l.lastSeen ? ' · last seen ' + ago(l.lastSeen, now) : ''}</span></span></div></li>
        ))}</ul>
        {h.suggestedAllowLine ? <p className="card-note">To allow every learned port, change this host's allowlist line to <code>{h.suggestedAllowLine}</code> through a pull request.</p> : null}
        <p className="card-note subtle-text">Learned from the node's flow logs while a grant was open and for two minutes after. Learning only records and preselects; it never opens anything.</p>
      </section>
    ) : null}
  </>;
}

export function Lan({ws, now}: {ws: SandboxState; now: number}) {
  const lan = ws.lan;
  const [prefill] = useState(() => takeLanPrefill(ws.id));
  if (lan === undefined) return <div className="stack"><SectionIntro title="LAN devices" /><p className="empty">{ws.lanError ? <span className="error-text">{ws.lanError}</span> : 'Loading LAN hosts…'}</p></div>;
  if (lan === null) return (
    <div className="stack">
      <SectionIntro title="LAN devices">Direct TCP from {ws.id} to an allowlisted computer on the home network, for a limited time.</SectionIntro>
      <NotAvailable what="LAN devices" needs="This console or its sandbox-control release does not serve GET /api/lan yet.">
        <a href={classicHref(ws.id, 'access')}>Manage LAN access on the classic page</a>
      </NotAvailable>
    </div>
  );
  const listed = lan.hosts.filter(h => h.listed).length;
  return (
    <div className="stack">
      <SectionIntro title="LAN devices" aside={`${listed} allowlisted · ${lan.open} open · at most ${lan.grantLimit} grants per workspace`}>
        Direct TCP from {ws.id} to an allowlisted computer on the home network, for {lan.windows.map(windowLabel).join(', ').replace(/, ([^,]*)$/, ' or $1')}. Agents cannot open these; they file a blocked report.
      </SectionIntro>
      {ws.lanError ? <Callout variant="warning" description={'LAN state may be stale: ' + ws.lanError} /> : null}
      {!lan.enabled ? <Callout variant="neutral" title="LAN host grants are off" description={[lan.reason, lan.error].filter(Boolean).join('. ') || 'No LAN hosts are allowlisted.'} /> : null}
      {lan.hosts.map(h => <HostCard key={h.address} ws={ws} lan={lan} h={h} now={now} prefill={prefill && prefill.address === h.address ? prefill : null} />)}
      {!lan.hosts.length && lan.enabled ? <p className="empty">No LAN hosts are allowlisted.</p> : null}
      <details className="card fold">
        <summary>Add another device</summary>
        <p className="muted">Hosts are allowlisted in Git, not here. Add one line such as <code>192.168.1.50: 22, 8080</code> (or <code>HOST: all</code>) to <code>allowed-hosts</code> in ConfigMap <code>lan-grants</code> (namespace agent-sandbox-control) through a pull request; after the rollout it appears above. Nodes, gateways, VIPs and the cluster ranges are always refused.</p>
      </details>
      <details className="card fold">
        <summary>LAN host history ({lan.history.length})</summary>
        <ol className="timeline" aria-label="LAN host history">
          {lan.history.length ? lan.history.map((e, i) => (
            <li key={i}><time dateTime={new Date(e.at).toISOString()}>{stamp(e.at)}</time><div><strong>{lanEventTitle(e.event)} · {e.host}</strong><p className="subtle-text">{[e.ports ? 'TCP ' + e.ports : '', e.deadline ? 'until ' + stamp(e.deadline) : '', e.by, e.detail].filter(Boolean).join(' · ')}</p></div></li>
          )) : <li className="timeline-empty"><p>No LAN history.</p></li>}
        </ol>
      </details>
      <p className="subtle-text">Restrict now, expiry, a replaced workspace pod and a sandbox-control restart remove the rule and restart the workspace pod to close open connections, which ends every LAN grant of this workspace and its agent sessions. Workspace files are preserved.</p>
    </div>
  );
}
