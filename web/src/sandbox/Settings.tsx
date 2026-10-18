import {useState} from 'react';
import {Button, Card, CardHeader, Input, Tag} from '../ui';
import {timeOf, type SandboxContext} from './Sandbox';

const PROFILE = /^[a-z0-9]([a-z0-9-]{0,45}[a-z0-9])?$/;

export function SettingsTab({detail, act}: SandboxContext) {
  const [name, setName] = useState('');
  const [busy, setBusy] = useState('');
  const profile = async (action: 'save' | 'apply') => {
    if (action === 'apply' && !confirm(`Apply profile ${name}? It replaces this sandbox's policy with the saved one and keeps its running state.`)) return;
    setBusy(action);
    await act('/profile', {name, action}, action === 'save' ? `profile ${name} saved` : `profile ${name} applied`);
    setBusy('');
  };
  const restore = async (id: number) => {
    if (!confirm(`Restore revision ${id}? Its policy is copied into a new revision.`)) return;
    setBusy('r' + id);
    await act('/rollback', {revision: String(id)}, `revision ${id} restored`);
    setBusy('');
  };
  return (
    <div className="stack">
      <Card variant="sunk" width="full">
        <CardHeader title="Policy profiles" subtitle="Save this sandbox's current policy under a name, or apply a saved one." />
        <div className="profile-row">
          <div className="field"><label className="field-label" htmlFor="profile-name">Profile name</label>
            <Input id="profile-name" value={name} placeholder="research-default" autoCapitalize="off" spellCheck={false} hasError={Boolean(name) && !PROFILE.test(name)} onChange={e => setName(e.target.value.toLowerCase())} /></div>
          <div className="actions">
            <Button variant="secondary" disabled={!PROFILE.test(name) || Boolean(busy)} onClick={() => profile('save')}>Save current</Button>
            <Button variant="secondary" disabled={!PROFILE.test(name) || Boolean(busy)} onClick={() => profile('apply')}>Apply</Button>
          </div>
        </div>
      </Card>
      <Card variant="sunk" width="full">
        <CardHeader title="Revision history" subtitle="Every policy change is a revision. Restoring copies an earlier policy into a new one." />
        <ul className="items" aria-label="Revisions">
          {detail.revisions.map(r => (
            <li key={r.id}>
              <div className="item-main"><strong>Revision {r.id} {r.id === detail.revision.id ? <Tag size="sm" variant="info" text="Current" className="inline-tag" /> : null}</strong>
                <span className="subtle">{[r.actor, r.status, r.at ? timeOf(r.at) : ''].filter(Boolean).join(' · ')}</span>
                {r.error ? <span className="attention">{r.error}</span> : null}</div>
              {r.id !== detail.revision.id ? <Button size="small" variant="tertiary" disabled={busy === 'r' + r.id} onClick={() => restore(r.id)}>Restore</Button> : null}
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}
