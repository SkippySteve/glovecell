// Internet section of a project sandbox: the access level (AccessProfile) on
// top, then the sandbox's own HTTPS rules and the level's read-only rules in
// the shared grant table. Own rules change only while paused; saving them
// leaves the sandbox paused until Resume.
import {useEffect, useState} from 'react';
import {api, type AccessProfileView, type HTTPSRule, type Project} from '../../api';
import {ProjectAccessLevels, useProfiles} from '../../access/AccessLevels';
import {GrantTable} from '../../access/GrantTable';
import {LEVEL_NAMES} from '../../access/levels';
import {useData} from '../../data';
import {LifecycleButton} from '../../Project';
import {ReferenceRules} from '../../Reference';
import {projectHref} from '../../router';
import {newRule, RuleEditor, rulesError, toDraft, toRule, type RuleDraft} from '../../Rules';
import {pausedForEdits} from '../../state';
import {Button, Callout} from '../../ui';
import {ruleDrafts as drafts} from '../sources/drafts';
import {useNow} from '../time';
import {plural} from '../bits';

const pathsText = (paths: string[]) => paths.length === 1 && paths[0] === '/' ? 'every path' : paths.join(' ');

function OwnRules({p, now}: {p: Project; now: number}) {
  const {reload, toast} = useData();
  const name = p.metadata.name, canEdit = pausedForEdits(p);
  const [state, setState] = useState(() => drafts.get(name) || {rules: (p.spec.https || []).map(toDraft), resourceVersion: undefined});
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState<number | null>(null);
  const dirty = drafts.has(name);
  useEffect(() => { if (!drafts.has(name)) setState({rules: (p.spec.https || []).map(toDraft), resourceVersion: undefined}); }, [name, p.spec.https]);
  const change = (rules: RuleDraft[]) => { const next = {rules, resourceVersion: state.resourceVersion || p.metadata.resourceVersion}; drafts.set(name, next); setState(next); };
  const discard = () => { drafts.delete(name); setEditing(null); setState({rules: (p.spec.https || []).map(toDraft), resourceVersion: undefined}); };
  const save = async () => {
    const problem = rulesError(state.rules) || (state.rules.some(r => !r.host.trim()) ? 'Each destination needs a hostname.' : '');
    if (problem) { toast(problem, true); return; }
    setSaving(true);
    try {
      await api('workspaces/' + encodeURIComponent(name) + '/access', 'PATCH', {resourceVersion: state.resourceVersion || p.metadata.resourceVersion, https: state.rules.map(toRule)});
      drafts.delete(name); setEditing(null);
      toast(name + ': access update requested. The sandbox stays paused until you resume it.');
      await reload();
    } catch (e) { toast((e as Error).message, true); }
    finally { setSaving(false); }
  };
  const add = () => { const r = newRule(); change([...state.rules, r]); setEditing(r.key); };
  return (
    <GrantTable<RuleDraft> title={`Own HTTPS rules (${state.rules.length})${dirty ? ', not saved' : ''}`} label="Own HTTPS rules" testId="own-rules" now={now}
      rows={state.rules} rowKey={r => String(r.key)} rowLabel={r => r.host || 'new destination'}
      intro={<>
        <p>Exact hosts, methods and literal path prefixes this sandbox may use on top of its level; a path of / allows the whole host. Rules here make the level Custom.</p>
        {!canEdit && p.spec.desiredState !== 'Removed' ? <div className="callout-row"><Callout variant="neutral" title="Pause to change these rules" description="Own rules change only while the sandbox is paused; saving keeps it paused until Resume. The level above changes while it runs." />{p.spec.desiredState === 'Running' ? <LifecycleButton p={p} size="small" /> : null}</div> : null}
      </>}
      toolbar={canEdit ? <Button size="small" variant="secondary" onClick={add}>Add destination</Button> : null}
      search={{placeholder: 'api.example.com', label: 'Search rules', text: r => r.host + ' ' + r.paths}}
      filters={[{key: 'writes', label: 'Sends data', test: r => r.methods.some(m => ['POST', 'PUT', 'PATCH', 'DELETE'].includes(m))}]}
      bulk={canEdit ? [{key: 'remove', label: 'Remove selected', danger: true, run: rows => change(state.rules.filter(r => !rows.includes(r)))}] : undefined}
      columns={[
        {key: 'host', label: 'Host', cell: r => <code>{r.host || 'new destination'}</code>},
        {key: 'methods', label: 'Methods', cell: r => r.methods.join(', ')},
        {key: 'paths', label: 'Paths', cell: r => pathsText(r.paths.split('\n').map(x => x.trim()).filter(Boolean))},
      ]}
      expiry={() => ({note: 'Until removed'})}
      actions={canEdit ? r => <>
        <Button size="small" variant="tertiary" aria-expanded={editing === r.key} onClick={() => setEditing(editing === r.key ? null : r.key)}>{editing === r.key ? 'Done' : 'Edit'}</Button>
        <Button size="small" variant="secondary" buttonType="destructive" onClick={() => change(state.rules.filter(x => x !== r))}>Remove</Button>
      </> : undefined}
      detail={r => canEdit && editing === r.key ? <RuleEditor rule={r} disabled={false} onChange={next => change(state.rules.map(x => x.key === r.key ? next : x))} onRemove={() => change(state.rules.filter(x => x !== r))} /> : null}
      initiallyOpen={undefined}
      empty={{title: 'No rules of its own.', hint: canEdit ? 'Add a destination for a tool that needs one host, or choose a level above.' : 'Choose a level above. To add exact hosts, pause the sandbox first.'}}
      footer={canEdit && dirty ? <div className="actions"><Button variant="primary" disabled={saving} onClick={save}>Save rules</Button><Button variant="tertiary" onClick={discard}>Discard changes</Button></div> : null}
    />
  );
}

// The rules the current level adds, read only.
function LevelRules({profile, now}: {profile: AccessProfileView; now: number}) {
  const rows = profile.rules.map((r, i) => ({...r, key: String(i)}));
  return (
    <GrantTable<HTTPSRule & {key: string}> title={`${LEVEL_NAMES[profile.level]} level rules (${rows.length})`} label="Level rules" now={now} rows={rows} rowKey={r => r.key} rowLabel={r => r.host}
      intro={<p>From the {profile.name} profile: read only, the same for every sandbox at this level. They change with the level.</p>}
      search={{placeholder: 'npmjs', label: 'Search rules', text: r => r.host}}
      columns={[
        {key: 'host', label: 'Host', cell: r => <code>{r.host}</code>},
        {key: 'methods', label: 'Methods', cell: r => r.methods.join(', ')},
        {key: 'paths', label: 'Paths', cell: r => pathsText(r.paths)},
      ]}
      expiry={() => ({note: 'While at this level'})}
      empty={{title: 'This level adds no rules.', hint: 'Choose Standard above for package registries and GitHub downloads.'}}
    />
  );
}

export function ProjectInternet({p}: {p: Project}) {
  const now = useNow();
  const profiles = useProfiles();
  const profile = profiles?.find(x => x.name === p.spec.accessProfileName);
  const name = p.metadata.name;
  const fineGrained = <ul aria-label="Fine-grained access">
    <li className="label">Fine-grained:</li>
    <li><a href="#own-rules">Own rules</a></li>
    <li><a href={projectHref(name, 'lan')}>LAN devices</a></li>
    <li><a href={projectHref(name, 'credentials')}>Credentials</a></li>
  </ul>;
  const also = <>
    <h3>At every level</h3>
    <ul>
      <li>Its repository {p.spec.repository}{(p.spec.repositories || []).length ? ` and ${p.spec.repositories!.length} more` : ''}</li>
      {(p.spec.lanHosts || []).length ? <li className="warn">{plural(p.spec.lanHosts!.length, 'LAN host')} (LAN devices)</li> : null}
      {(p.spec.providers || []).length ? <li>Subscription providers: {p.spec.providers!.map(x => x.name).join(', ')}</li> : null}
      <li>Files you download or upload from the Files page</li>
    </ul>
  </>;
  return (
    <div className="stack">
      <ProjectAccessLevels p={p} now={now} profiles={profiles} fineGrained={fineGrained} also={also} />
      <div id="own-rules"><OwnRules key={name} p={p} now={now} /></div>
      {profile && profile.rules.length ? <LevelRules profile={profile} now={now} /> : null}
      <ReferenceRules />
    </div>
  );
}
