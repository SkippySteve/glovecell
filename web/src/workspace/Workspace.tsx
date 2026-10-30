// The sandbox workspace view (Option B): a sidebar of workspaces and sections,
// a sticky header with the workspace state and every open grant, and one short
// page per section. Section data comes only through ./sources modules.
import {useEffect, useMemo, useState, type ReactNode} from 'react';
import type {ExistingSandbox, Project} from '../api';
import {useData} from '../data';
import {SandboxRuns} from '../factory/SandboxRuns';
import {existingKind, orcaRuntimeId} from '../orca';
import {LifecycleButton, ProjectHistory, ProjectOverview, ProjectRepositories} from '../Project';
import {ProjectInternet} from './sections/ProjectInternet';
import {LanHosts} from '../LanHosts';
import {Providers} from '../Providers';
import {navigate, projectHref, sandboxHref, SECTIONS, type Section} from '../router';
import {classicHref, Services, type SandboxContext} from '../sandbox/Sandbox';
import {SettingsTab} from '../sandbox/Settings';
import {existingState, projectState} from '../state';
import {Button, Callout, Card, Tag} from '../ui';
import {Countdown, NotAvailable, SectionIntro} from './bits';
import {useConfirm} from './Confirm';
import {Credentials} from './sections/Credentials';
import {Files} from './sections/Files';
import {projectDownloads, sandboxDownloads} from './sources/downloads';
import {projectUploads, sandboxUploads} from './sources/uploads';
import {History} from './sections/History';
import {Internet} from './sections/Internet';
import {Lan, useRestrict} from './sections/Lan';
import {Overview} from './sections/Overview';
import {Packages} from './sections/Packages';
import {Previews} from './sections/Previews';
import {loadLan} from './sources/lan';
import {webAccess} from './sources/internet';
import {liveLinks} from './sources/previews';
import {useNow} from './time';
import {useSandbox, type SandboxState} from './useSandbox';

type Ref = {kind: 'sandbox'; id: string} | {kind: 'project'; name: string};
const SIDEBAR_LAN_MS = 30000;

// Sections each kind of workspace lists. Project sandboxes have no packages or
// previews API; they are listed so the layout matches, and say so.
const sectionsFor = (kind: Ref['kind'], runs: boolean) => SECTIONS.filter(([key]) => kind === 'sandbox' ? key !== 'runs' || runs : key !== 'runs' && key !== 'settings');

// Open LAN grants per sandbox for the sidebar badges, polled slowly.
function useLanCounts(ids: string[]) {
  const [counts, setCounts] = useState<Record<string, number>>({});
  const key = ids.join(',');
  useEffect(() => {
    let live = true;
    const load = () => Promise.all(ids.map(id => loadLan(id).then(v => [id, v?.open || 0] as const, () => [id, 0] as const))).then(pairs => { if (live) setCounts(Object.fromEntries(pairs)); });
    load();
    const t = window.setInterval(() => { if (document.visibilityState === 'visible') load(); }, SIDEBAR_LAN_MS);
    return () => { live = false; window.clearInterval(t); };
  }, [key]); // ids is derived from key
  return counts;
}

interface Entry { ref: Ref; key: string; label: string; sub: string; running: boolean; waiting: number; attention: boolean; href: (s: Section) => string }

function useEntries(): Entry[] {
  const {existing, projects, session} = useData();
  const runtime = orcaRuntimeId(session, existing);
  const sandboxes = existing.map((s: ExistingSandbox): Entry => ({ref: {kind: 'sandbox', id: s.id}, key: 's:' + s.id, label: s.id, sub: existingKind(s, runtime), running: existingState(s).label === 'Running', waiting: (s.pending || 0) + (s.approvals || 0), attention: false, href: sec => sandboxHref(s.id, sec)}));
  const active = projects.filter((p: Project) => p.status?.phase !== 'Removed').map((p: Project): Entry => {
    const state = projectState(p);
    return {ref: {kind: 'project', name: p.metadata.name}, key: 'p:' + p.metadata.name, label: p.metadata.name, sub: 'Project sandbox', running: state.label === 'Ready', waiting: 0, attention: Boolean(state.attention), href: sec => projectHref(p.metadata.name, sec === 'runs' || sec === 'settings' ? 'overview' : sec)};
  });
  return [...sandboxes, ...active];
}

function Sidebar({current, section, badges, lanOpen}: {current: string; section: Section; badges: Partial<Record<Section, {n: number; live?: boolean; label?: string}>>; lanOpen: Record<string, number>}) {
  const entries = useEntries();
  const {session} = useData();
  const me = entries.find(e => e.key === current);
  return (
    <aside className="ws-side" aria-label="Sandbox navigation">
      <nav aria-labelledby="ws-side-workspaces">
        <h2 className="ws-side-label" id="ws-side-workspaces">Workspaces</h2>
        <ul>{entries.map(e => (
          <li key={e.key}><a href={e.href(section)} aria-current={e.key === current ? 'page' : undefined} className="ws-side-item">
            <span className={'dot' + (e.running ? ' on' : '')} aria-hidden="true" />
            <span className="ws-side-name">{e.label}<span className="ws-side-sub">{e.sub}{e.running ? '' : ' · stopped'}</span></span>
            <span className="end">
              {lanOpen[e.label] && e.ref.kind === 'sandbox' ? <span className="count-badge live" title="LAN access open">LAN</span> : null}
              {e.waiting ? <span className="count-badge" title={`${e.waiting} waiting on you`}>{e.waiting}<span className="visually-hidden"> waiting</span></span> : null}
              {e.attention ? <span className="count-badge" title="Needs attention">!<span className="visually-hidden"> needs attention</span></span> : null}
            </span>
          </a></li>
        ))}</ul>
      </nav>
      {me ? (
        <nav aria-labelledby="ws-side-sections">
          <h2 className="ws-side-label" id="ws-side-sections"><span className="visually-hidden">Sections of </span>{me.label}</h2>
          <ul>{sectionsFor(me.ref.kind, Boolean(session.runRecords)).map(([key, label]) => {
            const b = badges[key];
            return <li key={key}><a href={me.href(key)} aria-current={key === section ? 'page' : undefined} className="ws-side-item" data-section={key}>
              {label}{b?.n ? <span className={'end count-badge' + (b.live ? ' live' : '')}>{b.label || b.n}<span className="visually-hidden"> {b.live ? 'open' : 'waiting'}</span></span> : null}
            </a></li>;
          })}</ul>
        </nav>
      ) : null}
      <nav aria-labelledby="ws-side-elsewhere">
        <h2 className="ws-side-label" id="ws-side-elsewhere">Elsewhere</h2>
        <ul>
          <li><a className="ws-side-item" href="#/">All sandboxes</a></li>
          {me?.ref.kind === 'sandbox' ? <li><a className="ws-side-item" href={classicHref(me.label, section === 'internet' || section === 'lan' || section === 'packages' ? 'access' : section === 'history' ? 'activity' : 'previews')}>Classic page ↗</a></li> : null}
        </ul>
      </nav>
    </aside>
  );
}

// On narrow screens the sidebar becomes a workspace picker and a row of section pills.
function MobileNav({current, section, badges, kind}: {current: string; section: Section; badges: Partial<Record<Section, {n: number; live?: boolean}>>; kind: Ref['kind']}) {
  const entries = useEntries();
  const {session} = useData();
  const me = entries.find(e => e.key === current);
  return (
    <div className="ws-mobile">
      <label className="visually-hidden" htmlFor="ws-picker">Workspace</label>
      <select id="ws-picker" className="select ws-picker" value={current} onChange={e => { const next = entries.find(x => x.key === e.target.value); if (next) navigate(next.href(section)); }}>
        {entries.map(e => <option key={e.key} value={e.key}>{e.label} · {e.sub}</option>)}
      </select>
      <nav className="ws-pills" aria-label="Sections">
        {me ? sectionsFor(kind, Boolean(session.runRecords)).map(([key, label]) => (
          <a key={key} href={me.href(key)} aria-current={key === section ? 'page' : undefined}>{label.replace('Previews & dev links', 'Previews')}{badges[key]?.n ? <span className={'count-badge' + (badges[key]!.live ? ' live' : '')}>{badges[key]!.n}</span> : null}</a>
        )) : null}
      </nav>
    </div>
  );
}

function Shell({current, section, kind, badges, lanOpen, header, children}: {current: string; section: Section; kind: Ref['kind']; badges: Partial<Record<Section, {n: number; live?: boolean; label?: string}>>; lanOpen: Record<string, number>; header: ReactNode; children: ReactNode}) {
  return (
    <div className="ws-shell">
      <Sidebar current={current} section={section} badges={badges} lanOpen={lanOpen} />
      <div className="ws-main">
        <div className="ws-sticky">{header}<MobileNav current={current} section={section} badges={badges} kind={kind} /></div>
        <div className="ws-content">{children}</div>
      </div>
    </div>
  );
}

function GrantChips({ws, now}: {ws: SandboxState; now: number}) {
  const d = ws.detail!, web = webAccess(d), lan = ws.lan;
  const {egress} = useData();
  const restrict = useRestrict(ws);
  const waiting = (d.egressCounts?.pending || 0) + d.previewPending + (d.devLinkPending || 0) + (ws.reports?.length || 0);
  return (
    <ul className="grant-chips" aria-label="Active grants">
      {!web ? <li className="gchip">Fixed policy rules</li>
        : web.phase === 'Locked' ? <li className="gchip ok">Web locked</li>
        : <li className={'gchip ' + (web.phase === 'Open' ? 'bad' : 'warn')}>Web {web.phase.toLowerCase()} <Countdown until={web.expires} now={now} /><button type="button" onClick={() => egress([d.id], 'lock', web.minutes)} aria-label={`Lock web access for ${d.id}`}>Lock</button></li>}
      {lan?.hosts.filter(h => h.grant).map(h => (
        <li key={h.address} className="gchip bad">LAN {h.address} {h.grant!.state === 'Open' && h.grant!.deadline ? <Countdown until={h.grant!.deadline} now={now} /> : h.grant!.state.toLowerCase() + '…'}
          {h.grant!.state !== 'Restricting' ? <button type="button" onClick={() => restrict(h.address, lan)} aria-label={`Restrict LAN access to ${h.address}`}>Restrict</button> : null}</li>
      ))}
      {liveLinks(ws.devlinks).map(l => <li key={l.name} className="gchip ok">Dev link {l.name}{l.expires ? <> · <Countdown until={l.expires} now={now} /></> : null}</li>)}
      {waiting ? <li className="gchip dashed">{waiting} waiting on you</li> : null}
    </ul>
  );
}

export function SandboxWorkspace({id, section}: {id: string; section: Section}) {
  const ws = useSandbox(id);
  const {existing, session} = useData();
  const ask = useConfirm();
  const now = useNow();
  const [working, setWorking] = useState(false);
  const lanOpen = useLanCounts(existing.map(s => s.id));
  const downloads = useMemo(() => sandboxDownloads(id), [id]);
  const uploads = useMemo(() => sandboxUploads(id), [id]);
  useEffect(() => { document.title = id + ' · ' + (SECTIONS.find(([k]) => k === section)?.[1] || '') + ' · Sandboxes'; }, [id, section]);
  const runtime = orcaRuntimeId(session, existing) === id;
  const kind = existing.find(s => s.id === id);
  const d = ws.detail;
  const counts = {...lanOpen, ...(ws.lan ? {[id]: ws.lan.open} : {})};
  const badges: Partial<Record<Section, {n: number; live?: boolean; label?: string}>> = d ? {
    overview: {n: ws.reports?.length || 0}, internet: {n: d.egressCounts?.pending || 0},
    lan: {n: ws.lan?.open || 0, live: true, label: ws.lan?.open ? `${ws.lan.open} open` : undefined}, previews: {n: d.previewPending + (d.devLinkPending || 0)},
  } : {};
  if (!d) return (
    <Shell current={'s:' + id} section={section} kind="sandbox" badges={badges} lanOpen={counts} header={<div className="ws-head"><h1>{id}</h1></div>}>
      <Card variant="sunk" width="full" className="empty-card">{ws.error
        ? <><h2>Sandbox unavailable</h2><p className="muted">{ws.error}</p><div className="actions"><Button variant="secondary" onClick={ws.reload}>Retry</Button><a className="link-button" href={classicHref(id)}>Classic page</a></div></>
        : <p>Loading…</p>}</Card>
    </Shell>
  );
  const running = d.mode === 'Running';
  const lifecycle = async () => {
    if (running && !await ask(`Stop ${id}?`, <><p>Storage is kept; processes stop and agent sessions end.</p>{ws.lan?.open ? <p className="danger-box">LAN grants of {id} end with the pod ({ws.lan.open} open).</p> : null}</>, 'Stop and keep storage')) return;
    setWorking(true);
    await ws.act('/lifecycle', {mode: running ? 'Suspended' : 'Running'}, running ? 'stop requested' : 'start requested');
    setWorking(false);
  };
  const ctx: SandboxContext = {detail: d, reload: ws.reload, act: ws.act};
  const header = (
    <header className="ws-head" data-sandbox={id}>
      <div className="ws-title">
        <h1>{id}</h1>
        <Tag size="sm" variant={running ? 'success' : 'neutral'} text={running ? 'Running' : 'Stopped'} />
        <span className="subtle-text">{kind ? existingKind(kind, runtime ? id : undefined) : 'Workspace'} · {d.namespace}{ws.lan?.podUid ? ' · pod ' + ws.lan.podUid.slice(0, 8) : ''}</span>
        <span className="actions end">
          <a className="link-button subtle-link" href={classicHref(id, section === 'history' ? 'activity' : section === 'previews' || section === 'overview' ? 'previews' : 'access')}>Classic page</a>
          <Button variant={running ? 'secondary' : 'primary'} size="small" disabled={working} onClick={lifecycle}>{running ? 'Stop' : 'Start'}</Button>
        </span>
      </div>
      <GrantChips ws={ws} now={now} />
    </header>
  );
  const body = section === 'overview' ? <Overview ws={ws} runtime={runtime} now={now} />
    : section === 'internet' ? <Internet ws={ws} now={now} />
    : section === 'lan' ? <Lan key={id} ws={ws} now={now} />
    : section === 'packages' ? <Packages ws={ws} />
    : section === 'downloads' ? <Files key={id} downloads={downloads} uploads={uploads} workspace={id} />
    : section === 'previews' ? <Previews {...ctx} />
    : section === 'credentials' ? <Credentials ws={ws} now={now} />
    : section === 'history' ? <History ws={ws} ctx={ctx} />
    : section === 'runs' ? <div className="stack"><SectionIntro title="Runs">Orchestration runs collected from this sandbox.</SectionIntro><SandboxRuns namespace={d.namespace} /></div>
    : <div className="stack"><SectionIntro title="Settings">Policy profiles, revisions and shared services.</SectionIntro><Services key={d.revision.id} {...ctx} /><SettingsTab {...ctx} /></div>;
  return (
    <Shell current={'s:' + id} section={section} kind="sandbox" badges={badges} lanOpen={counts} header={header}>
      {d.revision.error ? <Callout variant="danger" title={'Revision ' + d.revision.id + ' ' + d.revision.status.toLowerCase()} description={d.revision.error} /> : null}
      {!d.tailnetReady && (section === 'previews' || section === 'overview') ? <Callout variant="warning" description="Tailscale routing is not ready; previews and services are reachable on the LAN only." /> : null}
      {body}
    </Shell>
  );
}

export function ProjectWorkspace({name, section}: {name: string; section: Section}) {
  const {loaded, projects, existing, session} = useData();
  const lanOpen = useLanCounts(existing.map(s => s.id));
  // One stable source per project, so the section's effects do not refire on every render.
  const downloads = useMemo(() => projectDownloads(name, {downloads: session.downloads, downloadMaxBytes: session.downloadMaxBytes}), [name, session.downloads, session.downloadMaxBytes]);
  const uploads = useMemo(() => projectUploads(name, {downloads: session.downloads, downloadMaxBytes: session.downloadMaxBytes, uploads: session.uploads, uploadChunkBytes: session.uploadChunkBytes}), [name, session.downloads, session.downloadMaxBytes, session.uploads, session.uploadChunkBytes]);
  const p = projects.find(item => item.metadata.name === name);
  useEffect(() => { document.title = name + ' · Sandboxes'; }, [name]);
  if (!p) return (
    <Shell current={'p:' + name} section={section} kind="project" badges={{}} lanOpen={lanOpen} header={<div className="ws-head"><h1>{name}</h1></div>}>
      <Card variant="sunk" width="full" className="empty-card">{loaded ? <><h2>Sandbox not found</h2><p className="muted">{name} does not exist or was deleted.</p><div><Button variant="secondary" onClick={() => navigate('#/')}>Back to sandboxes</Button></div></> : <p>Loading…</p>}</Card>
    </Shell>
  );
  const state = projectState(p), ready = p.status?.phase === 'Ready';
  const rules = (p.spec.https || []).length, hosts = (p.spec.lanHosts || []).length;
  const header = (
    <header className="ws-head" data-project={p.metadata.name}>
      <div className="ws-title">
        <h1>{p.metadata.name}</h1>
        <Tag size="sm" variant={state.variant} text={state.label} />
        <span className="subtle-text">Project sandbox · {p.spec.repository}{p.status?.namespace ? ' · ' + p.status.namespace : ''}</span>
        <span className="actions end">
          {ready && session.downloads && section !== 'downloads' ? <a className="link-button" href={projectHref(p.metadata.name, 'downloads')}>Download or upload files</a> : null}
          {ready && session.t3Connect ? <a className="link-button" href="https://app.t3.codes" target="_blank" rel="noopener noreferrer">Open T3 Connect ↗</a> : null}
          <LifecycleButton p={p} primary={ready} size="small" />
        </span>
      </div>
      <ul className="grant-chips" aria-label="Active grants">
        <li className="gchip">{rules ? `Fixed rules · ${rules}` : 'Repositories only'}</li>
        {hosts ? <li className="gchip">LAN · {hosts} static host{hosts === 1 ? '' : 's'}</li> : null}
        {(p.spec.providers || []).map(item => <li key={item.name} className="gchip">Provider · {item.name}</li>)}
      </ul>
    </header>
  );
  const body = section === 'internet' ? <ProjectInternet key={p.metadata.name} p={p} />
    : section === 'lan' ? <div className="stack"><SectionIntro title="LAN devices">Direct TCP from this sandbox to one IPv4 address and explicit ports, kept until removed or for a limited time. Outbound only.</SectionIntro><LanHosts p={p} /></div>
    : section === 'packages' ? <div className="stack"><SectionIntro title="Packages" /><NotAvailable what="Package approvals" needs="Project sandboxes have no package approval list; registry rules on the Internet page allow downloads." /></div>
    : section === 'downloads' ? <Files key={p.metadata.name} downloads={downloads} uploads={uploads} workspace={p.metadata.name} />
    : section === 'previews' ? <div className="stack"><SectionIntro title="Previews & dev links" /><NotAvailable what="Previews and dev links" needs="Project sandboxes do not publish previews or dev links yet." /></div>
    : section === 'credentials' ? <div className="stack"><SectionIntro title="Credentials">Repositories this sandbox can push task branches to, and the subscription providers it may use. Secret values are never shown.</SectionIntro><ProjectRepositories p={p} /><Providers p={p} /></div>
    : section === 'history' ? <div className="stack"><SectionIntro title="History" /><ProjectHistory p={p} /></div>
    : <ProjectOverview p={p} />;
  return (
    <Shell current={'p:' + name} section={section} kind="project" badges={{lan: {n: hosts}, credentials: {n: 0}}} lanOpen={lanOpen} header={header}>
      {state.attention ? <Callout variant="warning" title={state.attention} description={<><span className="block">{state.message}</span><span className="block subtle">The controller retries automatically. Correct the setting described above; keep this sandbox request.</span></>} />
        : state.message ? <Callout variant="info" description={state.message} /> : null}
      {body}
    </Shell>
  );
}
