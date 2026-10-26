// Shared pieces of the sandbox-control workspace pages. The page itself is the
// sidebar workspace view in ../workspace.
import {useState} from 'react';
import type {SandboxDetail} from '../api';
import {Button, Card, CardHeader, TextArea} from '../ui';

export interface SandboxContext {
  detail: SandboxDetail;
  reload: () => Promise<void>;
  // Runs a sandbox-control action, reports it, then refreshes the page and the list.
  act: (path: string, fields: Record<string, string | string[]>, done: string) => Promise<boolean>;
}

export const classicHref = (id: string, tab = 'access') => '/sandboxes/?workspace=' + encodeURIComponent(id) + '&tab=' + tab;
export const timeOf = (unix: number) => new Date(unix * 1000).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'});

export function Services({detail, act}: SandboxContext) {
  const [drafts, setDrafts] = useState(() => Object.fromEntries(detail.services.map(s => [s.name, {...s.grant}])));
  const [saving, setSaving] = useState(false);
  if (!detail.services.length) return null;
  const editable = detail.services.some(s => s.editable);
  const save = async () => {
    setSaving(true);
    const fields: Record<string, string> = {};
    for (const s of detail.services) {
      const d = drafts[s.name];
      fields[`service.${s.name}.paths_text`] = d.rulesText;
      if (d.lan) fields[`service.${s.name}.lan`] = 'on';
      if (d.tailscale) fields[`service.${s.name}.tail`] = 'on';
    }
    await act('/services', fields, 'shared service access saved');
    setSaving(false);
  };
  return (
    <Card variant="sunk" width="full">
      <CardHeader title="Shared services" subtitle="Exposed cluster services this sandbox may call. One rule per line: METHODS [prefix:]/path." />
      {detail.services.map(s => {
        const d = drafts[s.name];
        return (
          <div className="service" key={s.name}>
            <div className="field"><label className="field-label" htmlFor={'svc-' + s.name}>{s.name} <span className="subtle">{s.host}</span></label>
              <TextArea id={'svc-' + s.name} rows={3} value={d.rulesText} disabled={!s.editable} onChange={e => setDrafts({...drafts, [s.name]: {...d, rulesText: e.target.value}})} /></div>
            <div className="chips">
              <label className="check"><input type="checkbox" checked={d.lan} disabled={!s.editable || !s.lanAvailable} onChange={e => setDrafts({...drafts, [s.name]: {...d, lan: e.target.checked}})} /> LAN</label>
              <label className="check"><input type="checkbox" checked={d.tailscale} disabled={!s.editable || !s.tailscaleAvailable} onChange={e => setDrafts({...drafts, [s.name]: {...d, tailscale: e.target.checked}})} /> Tailscale</label>
            </div>
          </div>
        );
      })}
      {editable ? <div className="card-foot"><span className="subtle">Applies to every sandbox's route to these services.</span><Button variant="primary" disabled={saving} onClick={save}>Save shared services</Button></div>
        : <p className="subtle">Shared services are managed from the primary sandbox.</p>}
    </Card>
  );
}
