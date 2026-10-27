import {useEffect, useState} from 'react';
import {api, type Project} from './api';
import {useData} from './data';
import {Button, Callout, Card, CardHeader, Tag} from './ui';

const LABELS: Record<string, string> = {grok: 'Grok', cursor: 'Cursor'};
const COMMANDS: Record<string, string> = {grok: 'grok', cursor: 'cursor-agent'};
const when = (iso?: string) => iso ? new Date(iso).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'}) : '';

// Brokered subscription providers for one project sandbox. Each switch is a
// per-project opt-in: the sandbox gets a short-lived access lease for that
// provider only, and Revoke withdraws it while the sandbox keeps running.
export function Providers({p}: {p: Project}) {
  const {session, reload, toast} = useData();
  const name = p.metadata.name, removed = p.spec.desiredState === 'Removed';
  const enabled = new Map((p.spec.providers || []).map(item => [item.name, item]));
  const offered = session.providers || [];
  const [working, setWorking] = useState('');
  const [leases, setLeases] = useState<Record<string, boolean> | null>(null);
  useEffect(() => {
    if (!offered.length) return;
    let live = true;
    const load = () => api<{leases?: Record<string, boolean>}>('provider-leases').then(r => { if (live) setLeases(r.leases || {}); }, () => { if (live) setLeases({}); });
    void load();
    const timer = setInterval(load, 30000);
    return () => { live = false; clearInterval(timer); };
  }, [offered.length]);
  const names = Array.from(new Set([...offered, ...enabled.keys()]));
  if (!names.length) return null;
  const change = async (provider: string, on: boolean) => {
    if (!on && !confirm('Revoke ' + LABELS[provider] + ' for ' + name + '? Running ' + COMMANDS[provider] + ' sessions in this sandbox stop.')) return;
    setWorking(provider);
    try {
      if (on) await api('workspaces/' + encodeURIComponent(name) + '/providers', 'POST', {resourceVersion: p.metadata.resourceVersion, name: provider});
      else await api('workspaces/' + encodeURIComponent(name) + '/providers/' + encodeURIComponent(provider), 'DELETE', {resourceVersion: p.metadata.resourceVersion});
      toast(name + ': ' + LABELS[provider] + (on ? ' enabled. It is usable within about a minute.' : ' revoked.'));
      await reload();
    } catch (error) { toast((error as Error).message, true); }
    finally { setWorking(''); }
  };
  return (
    <Card variant="sunk" width="full">
      <CardHeader title="Subscription providers" subtitle="Lets this sandbox run a provider's command line tool with your subscription. The sandbox gets only a short-lived pass (at most five minutes, renewed automatically); your login stays outside every sandbox. Off by default, per sandbox, and changes apply while it runs." />
      <ul className="items" aria-label="Subscription providers">
        {names.map(provider => {
          const grant = enabled.get(provider);
          const lease = leases?.[provider];
          return <li key={provider}>
            <div className="item-main">
              <strong>{LABELS[provider] || provider}</strong>
              <span>Run <code>{COMMANDS[provider] || provider}</code> in the sandbox terminal.</span>
              {grant?.addedBy ? <small className="subtle">Enabled by {grant.addedBy}{grant.addedAt ? ' · ' + when(grant.addedAt) : ''}</small> : null}
              {grant && leases && !lease ? <small className="subtle">No current pass from the provider broker: its login may need renewing. The sandbox gets access as soon as one is issued.</small> : null}
            </div>
            {grant ? <Tag size="sm" variant="info" text="On" /> : <Tag size="sm" text="Off" />}
            {removed ? null : grant
              ? <Button type="button" size="small" variant="tertiary" buttonType="destructive" disabled={working === provider} aria-label={'Revoke ' + LABELS[provider]} onClick={() => change(provider, false)}>Revoke</Button>
              : offered.includes(provider) ? <Button type="button" size="small" variant="secondary" disabled={working === provider} aria-label={'Enable ' + LABELS[provider]} onClick={() => change(provider, true)}>Enable</Button> : null}
          </li>;
        })}
      </ul>
      {!offered.length ? <Callout variant="neutral" description="Subscription providers are off on this controller. Existing access can still be revoked." /> : null}
    </Card>
  );
}
