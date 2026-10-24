import {useState, type FormEvent} from 'react';
import {api, type Project} from './api';
import {useData} from './data';
import {isIPv4, parsePorts, portsLabel} from './lan';
import {Button, Callout, Card, CardHeader, Input, Tag} from './ui';

const when = (iso?: string) => iso ? new Date(iso).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'}) : '';

// Read-only list, shared by Overview and the Access tab.
export function LanHostList({p, onRemove, removing}: {p: Project; onRemove?: (address: string) => void; removing?: string}) {
  const hosts = p.spec.lanHosts || [];
  if (!hosts.length) return <p className="subtle">No LAN hosts are reachable from this sandbox.</p>;
  return (
    <ul className="items" aria-label="LAN host grants">
      {hosts.map(h => <li key={h.address}>
        <div className="item-main">
          <strong>{h.address}</strong>
          <span>{portsLabel(h)}{h.note ? ' · ' + h.note : ''}</span>
          {h.addedBy ? <small className="subtle">Added by {h.addedBy}{h.addedAt ? ' · ' + when(h.addedAt) : ''}</small> : null}
        </div>
        {onRemove ? <Button type="button" size="small" variant="tertiary" buttonType="destructive" disabled={removing === h.address} aria-label={'Remove LAN host ' + h.address} onClick={() => onRemove(h.address)}>Remove</Button> : <Tag size="sm" text="Sandbox → host" />}
      </li>)}
    </ul>
  );
}

export function LanHosts({p}: {p: Project}) {
  const {session, reload, toast} = useData();
  const name = p.metadata.name, removed = p.spec.desiredState === 'Removed';
  const [address, setAddress] = useState(''), [ports, setPorts] = useState(''), [note, setNote] = useState('');
  const [saving, setSaving] = useState(false), [removing, setRemoving] = useState('');
  const path = 'workspaces/' + encodeURIComponent(name) + '/lan-hosts';
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const ip = address.trim(), parsed = parsePorts(ports);
    if (!isIPv4(ip)) { toast('Enter one IPv4 address, such as 192.168.1.20. Ranges, CIDRs and hostnames are not accepted.', true); return; }
    if (typeof parsed === 'string') { toast(parsed, true); return; }
    setSaving(true);
    try {
      await api(path, 'POST', {resourceVersion: p.metadata.resourceVersion, address: ip, ports: parsed, note: note.trim()});
      toast(name + ': ' + ip + ' granted. The controller applies it without a restart.');
      setAddress(''); setPorts(''); setNote('');
      await reload();
    } catch (error) { toast((error as Error).message, true); }
    finally { setSaving(false); }
  };
  const remove = async (ip: string) => {
    if (!confirm('Remove ' + ip + ' from ' + name + '? New connections from the sandbox to it are refused once the controller applies the change.')) return;
    setRemoving(ip);
    try {
      await api(path + '/' + encodeURIComponent(ip), 'DELETE', {resourceVersion: p.metadata.resourceVersion});
      toast(name + ': ' + ip + ' removed.');
      await reload();
    } catch (error) { toast((error as Error).message, true); }
    finally { setRemoving(''); }
  };
  const allowed = (session.lanAllowed || []).join(', ');
  return (
    <form onSubmit={submit} className="access-form">
      <Card variant="sunk" width="full">
        <CardHeader title="LAN hosts" subtitle="Direct TCP from this sandbox to single computers on your network, for example SSH to a dev box. Outbound only: nothing on your network can connect in. Changes apply while the sandbox runs." />
        <LanHostList p={p} onRemove={removed ? undefined : remove} removing={removing} />
        {removed ? null : !session.lanHosts ? (
          <Callout variant="neutral" description="LAN host grants are off on this controller. An administrator sets LAN_GRANT_ALLOWED_CIDRS to enable them. Existing grants can still be removed." />
        ) : (
          <>
            <div className="grid-2">
              <div className="field"><label className="field-label" htmlFor="lan-address">IPv4 address</label><Input id="lan-address" required inputMode="decimal" placeholder="192.168.1.20" autoCapitalize="off" spellCheck={false} value={address} onChange={event => setAddress(event.target.value)} aria-describedby="lan-address-help" /><small id="lan-address-help" className="subtle">One host inside {allowed || 'the allowed ranges'}. Nodes, gateways, VIPs and cluster ranges are refused.</small></div>
              <div className="field"><label className="field-label" htmlFor="lan-ports">TCP ports</label><Input id="lan-ports" required placeholder="22, 8080" autoCapitalize="off" spellCheck={false} value={ports} onChange={event => setPorts(event.target.value)} aria-describedby="lan-ports-help" /><small id="lan-ports-help" className="subtle">Each port listed; no ranges. Up to 16.</small></div>
              <div className="field"><label className="field-label" htmlFor="lan-note">Note (optional)</label><Input id="lan-note" maxLength={200} placeholder="Desktop SSH" value={note} onChange={event => setNote(event.target.value)} /></div>
            </div>
            <p className="subtle">SSH and other raw TCP clients connect directly. HTTP clients that honour the sandbox's proxy variables need <code>NO_PROXY=&lt;address&gt;</code> or <code>curl --noproxy '*'</code>. Your router or firewall must also allow the cluster nodes to reach the host.</p>
            <div className="card-foot"><span /><Button type="submit" variant="primary" disabled={saving}>Grant LAN host</Button></div>
          </>
        )}
      </Card>
    </form>
  );
}
