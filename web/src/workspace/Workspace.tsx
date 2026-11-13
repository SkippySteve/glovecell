// The sandbox workspace view (Option B): a sidebar of workspaces and sections,
// a sticky header with the workspace state and every open grant, and one short
// page per section. Section data comes only through ./sources modules.
import {useEffect, useMemo, useState, type ReactNode} from 'react';
import {useData} from '../data';
import {SandboxRuns} from '../factory/SandboxRuns';
import {existingKind, orcaRuntimeId} from '../orca';
import {LifecycleButton, ProjectOverview, ProjectRepositories} from '../Project';
import {ProjectInternet} from './sections/ProjectInternet';
import {LanHosts} from '../LanHosts';
import {Providers} from '../Providers';
import {navigate, projectHref, sandboxHref, SECTIONS, sectionsFor, type Section} from '../router';
import {useSectionBadges, type SectionBadges} from '../shell/state';
import {classicHref, type SandboxContext} from '../sandbox/Sandbox';
import {projectState} from '../state';
import {Button, Callout, Card, Tag} from '../ui';
import {Countdown, NotAvailable, SectionIntro} from './bits';
import {useConfirm} from './Confirm';
import {Credentials} from './sections/Credentials';
import {Files} from './sections/Files';
import {projectDownloads, sandboxDownloads} from './sources/downloads';
import {projectUploads, sandboxUploads} from './sources/uploads';
import {History, ProjectHistory} from './sections/History';
import {Internet} from './sections/Internet';
import {Lan, useRestrict} from './sections/Lan';
import {Overview} from './sections/Overview';
import {Packages} from './sections/Packages';
import {Previews, ProjectPreviews} from './sections/Previews';
import {Settings} from './sections/Settings';
import {webAccess} from './sources/internet';
import {liveLinks} from './sources/previews';
import {useNow} from './time';
import {useSandbox, type SandboxState} from './useSandbox';

type Ref = {kind: 'sandbox'; id: string} | {kind: 'project'; name: string};
type Badges = SectionBadges;

// The app shell's sidebar lists every workspace and, under the current one,
// its sections with these badges. On narrow screens the sidebar is a menu, so
// a row of section links stays at the top of the page.
function MobileNav({current, section, badges, kind}: {current: string; section: Section; badges: Badges; kind: Ref['kind']}) {
  const {session} = useData();
  const name = current.slice(2);
  const href = (s: Section) => kind === 'sandbox' ? sandboxHref(name, s) : projectHref(name, s);
  return (
    <div className="ws-mobile">
      <nav className="ws-pills" aria-label={'Sections of ' + name}>
        {sectionsFor(kind, Boolean(session.runRecords)).map(([key, label]) => (
          <a key={key} href={href(key)} aria-current={key === section ? 'page' : undefined}>{label.replace('Previews & dev links', 'Previews')}{badges[key]?.n ? <span className={'count-badge' + (badges[key]!.live ? ' live' : '')}>{badges[key]!.n}</span> : null}</a>
        ))}
      </nav>
    </div>
  );
}

function Shell({current, section, kind, badges, header, children}: {current: string; section: Section; kind: Ref['kind']; badges: Badges; header: ReactNode; children: ReactNode}) {
  useSectionBadges(current, badges);
  return (
    <div className="ws-shell">
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
  const downloads = useMemo(() => sandboxDownloads(id), [id]);
  const uploads = useMemo(() => sandboxUploads(id), [id]);
  useEffect(() => { document.title = id + ' · ' + (SECTIONS.find(([k]) => k === section)?.[1] || '') + ' · Sandboxes'; }, [id, section]);
  const runtime = orcaRuntimeId(session, existing) === id;
  const kind = existing.find(s => s.id === id);
  const d = ws.detail;
  const badges: Badges = d ? {
    overview: {n: ws.reports?.length || 0}, internet: {n: d.egressCounts?.pending || 0},
    lan: {n: ws.lan?.open || 0, live: true, label: ws.lan?.open ? `${ws.lan.open} open` : undefined}, previews: {n: d.previewPending + (d.devLinkPending || 0)},
  } : {};
  if (!d) return (
    <Shell current={'s:' + id} section={section} kind="sandbox" badges={badges} header={<div className="ws-head"><h1>{id}</h1></div>}>
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
    : <Settings {...ctx} />;
  return (
    <Shell current={'s:' + id} section={section} kind="sandbox" badges={badges} header={header}>
      {d.revision.error ? <Callout variant="danger" title={'Revision ' + d.revision.id + ' ' + d.revision.status.toLowerCase()} description={d.revision.error} /> : null}
      {!d.tailnetReady && (section === 'previews' || section === 'overview') ? <Callout variant="warning" description="Tailscale routing is not ready; previews and services are reachable on the LAN only." /> : null}
      {body}
    </Shell>
  );
}

const NO_BADGES: Badges = {};

export function ProjectWorkspace({name, section}: {name: string; section: Section}) {
  const {loaded, projects, session} = useData();
  // One stable source per project, so the section's effects do not refire on every render.
  const downloads = useMemo(() => projectDownloads(name, {downloads: session.downloads, downloadMaxBytes: session.downloadMaxBytes}), [name, session.downloads, session.downloadMaxBytes]);
  const uploads = useMemo(() => projectUploads(name, {downloads: session.downloads, downloadMaxBytes: session.downloadMaxBytes, uploads: session.uploads, uploadChunkBytes: session.uploadChunkBytes}), [name, session.downloads, session.downloadMaxBytes, session.uploads, session.uploadChunkBytes]);
  const p = projects.find(item => item.metadata.name === name);
  useEffect(() => { document.title = name + ' · Sandboxes'; }, [name]);
  if (!p) return (
    <Shell current={'p:' + name} section={section} kind="project" badges={NO_BADGES} header={<div className="ws-head"><h1>{name}</h1></div>}>
      <Card variant="sunk" width="full" className="empty-card">{loaded ? <><h2>Sandbox not found</h2><p className="muted">{name} does not exist or was deleted.</p><div><Button variant="secondary" onClick={() => navigate('#/')}>Back to sandboxes</Button></div></> : <p>Loading…</p>}</Card>
    </Shell>
  );
  const state = projectState(p), ready = p.status?.phase === 'Ready';
  const rules = (p.spec.https || []).length, hosts = (p.spec.lanHosts || []).length;
  const projectBadges: Badges = {lan: {n: hosts}};
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
    : section === 'previews' ? <ProjectPreviews />
    : section === 'credentials' ? <div className="stack"><SectionIntro title="Credentials">Repositories this sandbox can push task branches to, and the subscription providers it may use. Secret values are never shown.</SectionIntro><ProjectRepositories p={p} /><Providers p={p} /></div>
    : section === 'history' ? <ProjectHistory key={p.metadata.name} p={p} />
    : <ProjectOverview p={p} />;
  return (
    <Shell current={'p:' + name} section={section} kind="project" badges={projectBadges} header={header}>
      {state.attention ? <Callout variant="warning" title={state.attention} description={<><span className="block">{state.message}</span><span className="block subtle">The controller retries automatically. Correct the setting described above; keep this sandbox request.</span></>} />
        : state.message ? <Callout variant="info" description={state.message} /> : null}
      {body}
    </Shell>
  );
}
