import {useEffect, useState, type FormEvent} from 'react';
import {api, type Project as ProjectWorkspace} from './api';
import {useData} from './data';
import {LanHostList} from './LanHosts';
import {pausedForEdits, projectState, settled} from './state';
import {ruleDrafts as drafts} from './workspace/sources/drafts';
import {Button, Callout, Card, CardHeader, Input, Tag} from './ui';


function useLifecycle(p: ProjectWorkspace) {
  const {reload, toast} = useData();
  const [working, setWorking] = useState(false);
  return {working, async set(state: 'Running' | 'Paused' | 'Removed') {
    if (state === 'Removed' && !confirm('Remove ' + p.metadata.name + '? This revokes access and deletes project storage.')) return;
    setWorking(true);
    try {
      await api('workspaces/' + encodeURIComponent(p.metadata.name), 'PATCH', {desiredState: state, resourceVersion: p.metadata.resourceVersion});
      toast(p.metadata.name + ': ' + ({Running: 'resume', Paused: 'pause', Removed: 'removal'})[state] + ' requested');
      drafts.delete(p.metadata.name);
      await reload();
    } catch (error) { toast((error as Error).message, true); }
    finally { setWorking(false); }
  }};
}

export function LifecycleButton({p, primary = false, size = 'medium'}: {p: ProjectWorkspace; primary?: boolean; size?: 'small' | 'medium'}) {
  const lifecycle = useLifecycle(p);
  if (p.spec.desiredState === 'Removed') return null;
  const resume = p.spec.desiredState === 'Paused';
  const waiting = resume && (!settled(p) || p.status?.phase !== 'Paused') && Boolean(p.metadata.uid);
  return <Button size={size} variant={primary ? 'primary' : 'secondary'} disabled={lifecycle.working || waiting} title={waiting ? 'Available once the sandbox reports Paused' : undefined}
    onClick={() => lifecycle.set(resume ? 'Running' : 'Paused')}>{resume ? 'Resume' : 'Pause'}</Button>;
}

export function ProjectOverview({p}: {p: ProjectWorkspace}) {
  const {session, toast} = useData();
  const lifecycle = useLifecycle(p);
  const [copied, setCopied] = useState(false);
  const ready = p.status?.phase === 'Ready', command = 'agent-project-controller connect ' + p.metadata.name;
  const facts: [string, string][] = [['Repository', p.spec.repository], ['Status', projectState(p).label], ['Requested state', p.spec.desiredState], ['Namespace', p.status?.namespace || '—'], ['Outbound rules', String((p.spec.https || []).length)], ['Extra repositories', String((p.spec.repositories || []).length)], ['LAN hosts', String((p.spec.lanHosts || []).length)]];
  return (
    <div className="stack">
      <Card variant="sunk" width="full">
        <CardHeader title="Connect" subtitle={session.t3Connect ? 'Work in this sandbox from T3 Connect.' : 'Attach a terminal from a machine with controller access.'} />
        {!ready ? <p className="muted">Connection details appear when the sandbox is ready.</p>
          : session.t3Connect ? <div className="stack"><p className="muted">Choose this project environment in T3. Sign in with the account authorized for the controller.</p><div><a className="link-button" href="https://app.t3.codes" target="_blank" rel="noopener noreferrer">Open T3 Connect ↗</a></div></div>
          : <div className="copy-field"><code>{command}</code><Button size="small" variant="secondary" onClick={async () => { try { await navigator.clipboard.writeText(command); setCopied(true); } catch { toast('Copy failed; select the command instead.', true); } }}>{copied ? 'Copied' : 'Copy'}</Button></div>}
      </Card>
      <ClientRegistrations name={p.metadata.name} />
      <dl className="facts">{facts.map(([term, value]) => <div key={term}><dt>{term}</dt><dd>{value}</dd></div>)}</dl>
      {(p.spec.lanHosts || []).length ? <Card variant="sunk" width="full">
        <CardHeader title="LAN hosts" subtitle="Computers on your network this sandbox may open TCP connections to. Change them under LAN devices." />
        <LanHostList p={p} />
      </Card> : null}
      {p.spec.desiredState !== 'Removed' ? (
        <Card variant="sunk" width="full" className="danger-zone">
          <div><h2>Remove sandbox</h2><p className="muted">Revokes repository access and deletes project storage. Pause instead to keep files and credentials.</p></div>
          <Button variant="primary" buttonType="destructive" disabled={lifecycle.working} onClick={() => lifecycle.set('Removed')}>Remove sandbox</Button>
        </Card>
      ) : null}
    </div>
  );
}

function ClientRegistrations({name}: {name: string}) {
  const [clients, setClients] = useState<{kind: string; state: string}[]>([]);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    setClients([]);
    setFailed(false);
    const load = () => api<{clients: {kind: string; state: string}[]}>('workspaces/' + encodeURIComponent(name) + '/clients')
      .then(result => { if (live) { setClients(result.clients || []); setFailed(false); } }, () => { if (live) setFailed(true); });
    void load();
    const timer = setInterval(load, 10000);
    return () => { live = false; clearInterval(timer); };
  }, [name]);
  if (failed) return <Callout variant="warning" description="Client registration status is unavailable. Check the control host before removing this sandbox." />;
  if (!clients.length) return null;
  return <Card variant="sunk" width="full">
    <CardHeader title="Desktop clients" subtitle="Opened on your machine by register --watch (Local host). Phone access stays on the hub." />
    <ul className="items">{clients.map(client => <li key={client.kind}>
      <div className="item-main"><strong>{client.kind}</strong></div>
      <Tag size="sm" variant={client.state === 'registered' ? 'info' : 'neutral'} text={client.state} />
    </li>)}</ul>
  </Card>;
}

export function ProjectRepositories({p}: {p: ProjectWorkspace}) {
  const {session, reload, toast} = useData();
  const [name, setName] = useState(''), [host, setHost] = useState('forgejo'), [repository, setRepository] = useState(''), [saving, setSaving] = useState(false);
  const hostLabel = (h: string, isPublic?: boolean) => h === 'github' ? (isPublic ? 'Public GitHub' : 'Private GitHub') : 'Forgejo';
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setSaving(true);
    try {
      await api('workspaces/' + encodeURIComponent(p.metadata.name) + '/repositories', 'POST', {resourceVersion: p.metadata.resourceVersion, project: {name, host: host === 'github-private' ? 'github' : host, repository: repository.trim(), public: host === 'github'}});
      toast(p.spec.desiredState === 'Running'
        ? 'Repository enrollment requested. The worker refreshes briefly to clone it; Orca registers it on the next sync.'
        : 'Repository enrollment requested. Resume to clone it; hubspoke registers it with Orca on the next sync.');
      setName(''); setRepository('');
      await reload();
    } catch (error) { toast((error as Error).message, true); }
    finally { setSaving(false); }
  };
  const canEnroll = p.spec.desiredState !== 'Removed' && (pausedForEdits(p) || (p.spec.desiredState === 'Running' && p.status?.phase === 'Ready' && settled(p)));
  return (
    <div className="stack">
      <Card variant="sunk" width="full">
        <CardHeader title="Repositories in this sandbox" subtitle="Checked out on the sandbox volume. Protected branches and merges stay with the repository owner." />
        <ul className="items">
          <li><div className="item-main"><strong>{p.spec.repository}</strong><code className="subtle">/workspace/repo</code></div><Tag size="sm" variant="info" text="Primary" /></li>
          {(p.spec.repositories || []).map(r => <li key={r.name}><div className="item-main"><strong>{r.repository}</strong><code className="subtle">/workspace/projects/{r.name}</code></div><Tag size="sm" text={hostLabel(r.host, r.public)} /></li>)}
        </ul>
      </Card>
      {p.spec.desiredState === 'Removed' ? null : !canEnroll ? (
        <div className="callout-row"><Callout variant="neutral" title="Add another repository" description="Wait until this sandbox is Ready, then enroll another repository on its existing volume. The worker refreshes briefly to clone it; Pause is not required." /></div>
      ) : (
        <form onSubmit={submit}>
          <Card variant="sunk" width="full">
            <CardHeader title="Add a repository" subtitle={p.spec.desiredState === 'Running' ? 'Clones onto the existing volume; the worker refreshes briefly. Orca picks it up on the spoke.' : 'Resume afterwards to clone it; Orca (or T3) picks it up on the spoke.'} />
            <div className="grid-2">
              <div className="field"><label className="field-label" htmlFor="enroll-host">Repository host</label>
                <select id="enroll-host" className="select" value={host} onChange={event => setHost(event.target.value)}>
                  <option value="forgejo">My Forgejo repository</option><option value="github">Public GitHub repository</option>
                  {session.githubOwner ? <option value="github-private">Private GitHub repository for {session.githubOwner}</option> : null}
                </select></div>
              <div className="field"><label className="field-label" htmlFor="enroll-repository">Repository, owner/name</label><Input id="enroll-repository" required placeholder="owner/repository" autoCapitalize="off" spellCheck={false} value={repository} onChange={event => setRepository(event.target.value)} /></div>
              <div className="field"><label className="field-label" htmlFor="enroll-name">Directory name</label><Input id="enroll-name" required pattern="[a-z][a-z0-9-]{0,39}" maxLength={40} placeholder="docs" autoCapitalize="off" spellCheck={false} value={name} onChange={event => setName(event.target.value)} aria-describedby="enroll-name-help" /><small id="enroll-name-help" className="subtle">Checked out at /workspace/projects/&lt;name&gt;.</small></div>
            </div>
            <div className="card-foot"><span /><Button type="submit" variant="primary" disabled={saving}>Add repository</Button></div>
          </Card>
        </form>
      )}
    </div>
  );
}
