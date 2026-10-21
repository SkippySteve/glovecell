import {useEffect, useState} from 'react';
import {egressControllable, egressExpires, egressState, orcaProjects, type ExistingSandbox, type OrcaProject, type Project} from './api';
import {useData} from './data';
import {existingKind, orcaRuntimeId, projectPreview, type SandboxKind} from './orca';
import {navigate, orcaHref, projectHref, sandboxHref} from './router';
import {clock, existingState, projectState, type Status} from './state';
import {Button, Callout, Card, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Tag, ToggleGroup, ToggleItem} from './ui';

const DURATIONS: [string, string][] = [['15', '15 min'], ['60', '1 hour'], ['240', '4 hours'], ['1440', '24 hours']];
// Each refresh lists Orca's repos in the runtime, so it runs far less often than the sandbox poll.
const ORCA_REFRESH_MS = 60000;

// The Orca runtime's projects, for its row. A failure leaves the row without them.
function useOrcaProjects(enabled: boolean) {
  const [records, setRecords] = useState<OrcaProject[]>([]);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    const load = () => orcaProjects().then(list => { if (live) setRecords(list); }, () => undefined);
    load();
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') load(); }, ORCA_REFRESH_MS);
    return () => { live = false; window.clearInterval(timer); };
  }, [enabled]);
  return records;
}

function OrcaProjectList({records}: {records: OrcaProject[]}) {
  const {names, more, total} = projectPreview(records);
  if (!total) return null;
  return <a className="row-projects" href={orcaHref()} data-testid="orca-project-list">{`${total} Orca project${total === 1 ? '' : 's'}: ${names.join(', ')}${more ? ` +${more} more` : ''}`}</a>;
}

function StatusTag({state}: {state: Status}) {
  return <Tag size="sm" variant={state.variant} text={state.label} className={state.busy ? 'busy' : undefined} />;
}

function EgressTag({sandbox}: {sandbox: ExistingSandbox}) {
  const state = egressState(sandbox), expires = egressExpires(sandbox);
  if (state === 'Open') return <Tag size="sm" variant="danger" text={'Open' + (expires ? ' until ' + clock(expires) : '')} />;
  if (state === 'Learning') return <Tag size="sm" variant="warning" text={'Learning' + (expires ? ' until ' + clock(expires) : '')} />;
  if (state === 'Locked') return <Tag size="sm" variant="success" text="Locked" />;
  return <span className="subtle">Policy rules</span>;
}

function EgressPanel({minutes, setMinutes}: {minutes: string; setMinutes: (value: string) => void}) {
  const {existing, egress, busy, existingError, session} = useData();
  const controllable = existing.filter(egressControllable);
  const open = controllable.filter(s => egressState(s) !== 'Locked');
  const ids = controllable.map(s => s.id);
  const working = ids.some(id => busy.has(id));
  if (!session.sandboxControl) return null;
  const summary = !controllable.length ? 'No sandboxes support instant web access changes.'
    : open.length ? `${open.length} of ${controllable.length} open to the public web.` : `All ${controllable.length} sandboxes are locked to approved requests.`;
  return (
    <section className={'egress-panel' + (open.length ? ' is-open' : '')} aria-labelledby="egress-title">
      <div className="egress-copy">
        <h2 id="egress-title">Web access</h2>
        <p className="egress-summary" data-testid="egress-summary">{summary}</p>
        <p className="subtle">Open allows any public HTTP and HTTPS request immediately, with nothing learned. Lock returns every sandbox to its approved requests on the next request. Private networks, cluster services and cloud metadata stay blocked either way.</p>
      </div>
      <div className="egress-controls">
        <div className="field">
          <span className="field-label" id="egress-duration">Open for</span>
          <ToggleGroup type="single" value={minutes} onValueChange={value => value && setMinutes(value)} aria-labelledby="egress-duration" className="durations">
            {DURATIONS.map(([value, label]) => <ToggleItem key={value} value={value} aria-label={'Open for ' + label}>{label}</ToggleItem>)}
          </ToggleGroup>
        </div>
        <div className="egress-buttons">
          <Button variant="primary" size="large" disabled={!controllable.length || working} onClick={() => egress(ids, 'open', Number(minutes))}>Open all</Button>
          <Button variant="primary" buttonType="destructive" size="large" disabled={!controllable.length || working} onClick={() => egress(ids, 'lock', Number(minutes))}>Lock all</Button>
        </div>
      </div>
      {existingError ? <Callout variant="danger" title="Existing sandboxes are unavailable" description={existingError} /> : null}
    </section>
  );
}

function SandboxRow({sandbox, minutes, kind, projects}: {sandbox: ExistingSandbox; minutes: string; kind: SandboxKind; projects?: OrcaProject[]}) {
  const {egress, busy} = useData();
  const state = egressState(sandbox), waiting = (sandbox.pending || 0) + (sandbox.approvals || 0);
  const pending = busy.has(sandbox.id);
  const review = sandboxHref(sandbox.id, egressControllable(sandbox) ? 'access' : 'overview');
  return (
    <TableRow data-sandbox={sandbox.id}>
      <TableCell data-label="Sandbox"><a className="row-title" href={sandboxHref(sandbox.id)}>{sandbox.id}</a><span className="row-kind">{kind}</span><span className="row-sub">{sandbox.namespace} · {sandbox.sandbox}</span>{projects ? <OrcaProjectList records={projects} /> : null}</TableCell>
      <TableCell data-label="Status"><StatusTag state={existingState(sandbox)} /></TableCell>
      <TableCell data-label="Web access"><EgressTag sandbox={sandbox} /></TableCell>
      <TableCell data-label="Review">{waiting ? <a href={review} className="count-link"><span className="count">{waiting}</span> awaiting review</a> : <span className="subtle">None</span>}</TableCell>
      <TableCell data-label="" align="right" className="row-actions">
        {egressControllable(sandbox) ? (state === 'Locked'
          ? <Button size="small" variant="secondary" disabled={pending} onClick={() => egress([sandbox.id], 'open', Number(minutes))} aria-label={'Open web access for ' + sandbox.id}>{pending ? 'Opening…' : 'Open'}</Button>
          : <Button size="small" variant="primary" buttonType="destructive" disabled={pending} onClick={() => egress([sandbox.id], 'lock', Number(minutes))} aria-label={'Lock web access for ' + sandbox.id}>{pending ? 'Locking…' : 'Lock'}</Button>)
          : <Button size="small" variant="tertiary" onClick={() => navigate(sandboxHref(sandbox.id))}>Manage</Button>}
      </TableCell>
    </TableRow>
  );
}

function ProjectRow({project}: {project: Project}) {
  const state = projectState(project), rules = (project.spec.https || []).length;
  return (
    <TableRow data-project={project.metadata.name}>
      <TableCell data-label="Sandbox"><a className="row-title" href={projectHref(project.metadata.name)}>{project.metadata.name}</a><span className="row-kind">Project sandbox</span><span className="row-sub">{project.spec.repository}</span></TableCell>
      <TableCell data-label="Status"><StatusTag state={state} /></TableCell>
      <TableCell data-label="Web access"><span className="subtle">{rules ? `${rules} fixed rule${rules === 1 ? '' : 's'}` : 'Repositories only'}</span></TableCell>
      <TableCell data-label="Review">{state.attention ? <span className="attention">{state.attention}</span> : <span className="subtle">None</span>}</TableCell>
      <TableCell data-label="" align="right" className="row-actions"><Button size="small" variant="tertiary" onClick={() => navigate(projectHref(project.metadata.name, 'access'))}>Rules</Button></TableCell>
    </TableRow>
  );
}

export function Home() {
  const {loaded, projects, existing, session} = useData();
  const [minutes, setMinutes] = useState('60');
  const runtime = orcaRuntimeId(session, existing);
  const orca = useOrcaProjects(runtime !== undefined);
  const [showRemoved, setShowRemoved] = useState(false);
  const active = projects.filter(p => p.status?.phase !== 'Removed'), removed = projects.filter(p => p.status?.phase === 'Removed');
  // Anything open or needing a decision sorts first.
  const rows = [
    ...existing.map(s => ({name: s.id, rank: egressState(s) && egressState(s) !== 'Locked' ? 0 : (s.pending || 0) + (s.approvals || 0) ? 1 : 2, node: <SandboxRow key={'s:' + s.id} sandbox={s} minutes={minutes} kind={existingKind(s, runtime)} projects={s.id === runtime ? orca : undefined} />})),
    ...active.map(p => ({name: p.metadata.name, rank: projectState(p).attention ? 1 : 2, node: <ProjectRow key={'p:' + p.metadata.name} project={p} />})),
  ].sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name));
  return (
    <>
      <div className="page-head">
        <div><h1>Sandboxes</h1><p className="muted">Isolated agent workspaces with their own storage, repository access and outbound rules.</p></div>
        <Button variant="secondary" onClick={() => navigate('#/new')}>New sandbox</Button>
      </div>
      <EgressPanel minutes={minutes} setMinutes={setMinutes} />
      {!loaded ? <p className="empty">Loading sandboxes…</p> : rows.length ? (
        <Table aria-label="Sandboxes" containerClassName="sandbox-table">
          <TableHeader><TableRow><TableHead>Sandbox</TableHead><TableHead>Status</TableHead><TableHead>Web access</TableHead><TableHead>Review</TableHead><TableHead><span className="visually-hidden">Actions</span></TableHead></TableRow></TableHeader>
          <TableBody>{rows.map(r => r.node)}</TableBody>
        </Table>
      ) : (
        <Card variant="sunk" width="full" className="empty-card"><h2>No sandboxes yet</h2><p className="muted">Create one from a repository to give an agent an isolated place to work.</p><div><Button variant="primary" onClick={() => navigate('#/new')}>New sandbox</Button></div></Card>
      )}
      {removed.length ? (
        <div className="removed">
          <Button variant="tertiary" size="small" aria-expanded={showRemoved} onClick={() => setShowRemoved(!showRemoved)}>{(showRemoved ? 'Hide' : 'Show') + ` removed (${removed.length})`}</Button>
          {showRemoved ? <ul className="plain-list">{removed.map(p => <li key={p.metadata.name}><a href={projectHref(p.metadata.name)}>{p.metadata.name}</a> <span className="subtle">{p.spec.repository}</span></li>)}</ul> : null}
        </div>
      ) : null}
    </>
  );
}
