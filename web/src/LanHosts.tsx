// LAN access of a project sandbox: hosts kept until removed (spec.lanHosts)
// and time-limited LAN grants (AccessGrants), in one table.
import {useCallback, useEffect, useState, type FormEvent} from 'react';
import {api, type AccessGrant, type LANHost, type Project} from './api';
import {useProfiles} from './access/AccessLevels';
import {GrantTable, type Expiry} from './access/GrantTable';
import {useData} from './data';
import {isIPv4, parsePorts, portsLabel} from './lan';
import {Button, Callout, Input, Tag} from './ui';
import {useConfirm} from './workspace/Confirm';
import {useNow} from './workspace/time';

const when = (iso?: string) => iso ? new Date(iso).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'}) : '';

// Read-only list for the Overview.
export function LanHostList({p}: {p: Project}) {
  const hosts = p.spec.lanHosts || [];
  if (!hosts.length) return <p className="subtle">No LAN hosts are reachable from this sandbox.</p>;
  return (
    <ul className="items" aria-label="LAN host grants">
      {hosts.map(h => <li key={h.address}>
        <div className="item-main">
          <strong>{h.address}</strong>
          <span>{portsLabel(h)}{h.note ? ', ' + h.note : ''}</span>
          {h.addedBy ? <small className="subtle">Added by {h.addedBy}{h.addedAt ? ' on ' + when(h.addedAt) : ''}</small> : null}
        </div>
        <Tag size="sm" text="Sandbox to host" />
      </li>)}
    </ul>
  );
}

interface Row { key: string; address: string; ports: number[]; note: string; by: string; at: string; host?: LANHost; grant?: AccessGrant }

const DURATIONS: [number, string][] = [[0, 'Until removed'], [15, '15 minutes'], [60, '1 hour'], [240, '4 hours'], [1440, '24 hours']];

function useGrants(name: string, version: string | undefined) {
  const [grants, setGrants] = useState<AccessGrant[] | null>(null);
  const load = useCallback(async () => {
    try { setGrants(await api<AccessGrant[]>('workspaces/' + encodeURIComponent(name) + '/grants')); } catch { setGrants(null); }
  }, [name]);
  useEffect(() => { load(); const t = window.setInterval(load, 15000); return () => window.clearInterval(t); }, [load, version]);
  return {grants, load};
}

export function LanHosts({p}: {p: Project}) {
  const {session, reload, toast} = useData();
  const ask = useConfirm();
  const now = useNow();
  const profiles = useProfiles();
  const name = p.metadata.name, removed = p.spec.desiredState === 'Removed';
  const {grants, load} = useGrants(name, p.metadata.resourceVersion);
  const [address, setAddress] = useState(''), [ports, setPorts] = useState(''), [note, setNote] = useState(''), [minutes, setMinutes] = useState(0);
  const [saving, setSaving] = useState(false);
  const profile = profiles?.find(x => x.name === p.spec.accessProfileName);
  const maxGrant = profile?.allowedGrants.includes('lan') ? profile.maxGrantMinutes?.lan || 0 : 0;
  const grantReason = !profiles ? 'This controller has no access grants yet.' : !p.spec.accessProfileName ? 'Choose an access level on the Internet page first.' : !maxGrant ? `The ${profile?.level || p.spec.accessProfileName} level allows no LAN grants; choose Standard or Custom.` : '';
  const path = 'workspaces/' + encodeURIComponent(name);
  const rows: Row[] = [
    ...(p.spec.lanHosts || []).map((h): Row => ({key: 'h:' + h.address, address: h.address, ports: h.ports, note: h.note || '', by: h.addedBy || '', at: h.addedAt || '', host: h})),
    ...(grants || []).filter(g => g.spec.type === 'lan' && g.spec.lan).map((g): Row => ({key: 'g:' + g.metadata.name, address: g.spec.lan!.address, ports: g.spec.lan!.ports, note: g.spec.lan!.note || g.spec.reason || '', by: g.spec.requestedBy || '', at: g.metadata.creationTimestamp || '', grant: g})),
  ];
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const ip = address.trim(), parsed = parsePorts(ports);
    if (!isIPv4(ip)) { toast('Enter one IPv4 address, such as 192.168.1.20. Ranges, CIDRs and hostnames are not accepted.', true); return; }
    if (typeof parsed === 'string') { toast(parsed, true); return; }
    setSaving(true);
    try {
      if (minutes) await api(path + '/grants', 'POST', {type: 'lan', minutes, lan: {address: ip, ports: parsed, note: note.trim()}});
      else await api(path + '/lan-hosts', 'POST', {resourceVersion: p.metadata.resourceVersion, address: ip, ports: parsed, note: note.trim()});
      toast(`${name}: ${ip} granted${minutes ? ' for ' + DURATIONS.find(([m]) => m === minutes)?.[1] : ''}. The controller applies it without a restart.`);
      setAddress(''); setPorts(''); setNote('');
      await Promise.all([reload(), load()]);
    } catch (error) { toast((error as Error).message, true); }
    finally { setSaving(false); }
  };
  const remove = async (list: Row[]) => {
    if (!await ask(list.length === 1 ? `Remove ${list[0].address}?` : `Remove ${list.length} LAN entries?`, <p>New connections from {name} are refused once the controller applies the change. Open connections are expected to stop; pause the sandbox to be certain.</p>, 'Remove')) return;
    let version = p.metadata.resourceVersion;
    try {
      for (const r of list) {
        if (r.grant) await api(path + '/grants/' + encodeURIComponent(r.grant.metadata.name), 'DELETE');
        else version = (await api<Project>(path + '/lan-hosts/' + encodeURIComponent(r.address), 'DELETE', {resourceVersion: version})).metadata.resourceVersion;
      }
      toast(`${name}: ${list.length === 1 ? list[0].address : list.length + ' entries'} removed.`);
    } catch (error) { toast((error as Error).message, true); }
    await Promise.all([reload(), load()]);
  };
  const expiry = (r: Row): Expiry => r.grant ? {at: Date.parse(r.grant.spec.expiresAt)} : 'never';
  const allowed = (session.lanAllowed || []).join(', ');
  return (
    <div className="stack">
      <GrantTable<Row> title={`LAN hosts (${rows.length})`} label="LAN hosts" testId="lan-hosts" now={now} rows={rows} rowKey={r => r.key} rowLabel={r => r.address}
        intro={<p>Direct TCP from this sandbox to single computers on your network, for example SSH to a dev box. Outbound only: nothing on your network can connect in. Changes apply while the sandbox runs.</p>}
        search={{placeholder: '192.168.1.20', label: 'Search hosts', text: r => r.address + ' ' + r.note + ' ' + r.ports.join(' ')}}
        filters={[{key: 'timed', label: 'Time-limited', test: r => Boolean(r.grant)}, {key: 'kept', label: 'Until removed', test: r => !r.grant}, {key: 'refused', label: 'Refused', test: r => r.grant?.status?.state === 'Refused'}]}
        bulk={removed ? undefined : [{key: 'remove', label: 'Remove selected', danger: true, run: remove}]}
        columns={[
          {key: 'host', label: 'Host', cell: r => <><code>{r.address}</code>{r.note ? <span className="cell-sub">{r.note}</span> : null}</>},
          {key: 'ports', label: 'Ports', cell: r => 'TCP ' + r.ports.join(', ')},
          {key: 'state', label: 'State', cell: r => r.grant
            ? (r.grant.status?.state === 'Refused' ? <><Tag size="sm" variant="danger" text="Refused" /><span className="cell-sub">{r.grant.status.message}</span></> : r.grant.status?.state === 'Active' ? <Tag size="sm" variant="warning" text="Open" /> : <Tag size="sm" text="Applying" />)
            : <Tag size="sm" variant="warning" text="Open" />},
          {key: 'by', label: 'Added by', cell: r => <>{r.by}{r.at ? <span className="cell-sub">{when(r.at)}</span> : null}</>},
        ]}
        expiry={expiry}
        actions={removed ? undefined : r => <Button size="small" variant="secondary" buttonType="destructive" aria-label={'Remove LAN host ' + r.address} onClick={() => remove([r])}>Remove</Button>}
        empty={{title: 'No LAN hosts are reachable from this sandbox.', hint: session.lanHosts ? 'Grant one below: one IPv4 address, the TCP ports, and how long.' : 'LAN host grants are off on this controller.'}}
      />
      {removed ? null : !session.lanHosts ? (
        <Callout variant="neutral" description="LAN host grants are off on this controller. An administrator sets LAN_GRANT_ALLOWED_CIDRS to enable them. Existing grants can still be removed." />
      ) : (
        <form onSubmit={submit} className="card access-form" aria-labelledby="lan-add-title">
          <div className="card-hd"><h3 id="lan-add-title">Grant a LAN host</h3></div>
          <div className="card-bd">
            <div className="grid-2">
              <div className="field"><label className="field-label" htmlFor="lan-address">IPv4 address</label><Input id="lan-address" required inputMode="decimal" placeholder="192.168.1.20" autoCapitalize="off" spellCheck={false} value={address} onChange={event => setAddress(event.target.value)} aria-describedby="lan-address-help" /><small id="lan-address-help" className="subtle">One host inside {allowed || 'the allowed ranges'}. Nodes, gateways, VIPs and cluster ranges are refused.</small></div>
              <div className="field"><label className="field-label" htmlFor="lan-ports">TCP ports</label><Input id="lan-ports" required placeholder="22, 8080" autoCapitalize="off" spellCheck={false} value={ports} onChange={event => setPorts(event.target.value)} aria-describedby="lan-ports-help" /><small id="lan-ports-help" className="subtle">Each port listed; no ranges. Up to 16.</small></div>
              <div className="field"><label className="field-label" htmlFor="lan-note">Note (optional)</label><Input id="lan-note" maxLength={200} placeholder="Desktop SSH" value={note} onChange={event => setNote(event.target.value)} /></div>
              <div className="field"><label className="field-label" htmlFor="lan-duration">How long</label>
                <select id="lan-duration" className="select" value={minutes} onChange={e => setMinutes(Number(e.target.value))} aria-describedby="lan-duration-help">
                  {DURATIONS.map(([m, l]) => <option key={m} value={m} disabled={m > 0 && (Boolean(grantReason) || m > maxGrant)}>{l}</option>)}
                </select>
                <small id="lan-duration-help" className="subtle">{grantReason || `Time-limited grants end on their own (at most ${DURATIONS.filter(([m]) => m && m <= maxGrant).pop()?.[1] || 'the level limit'}).`}</small></div>
            </div>
            <p className="subtle">SSH and other raw TCP clients connect directly. HTTP clients that honour the sandbox's proxy variables need <code>NO_PROXY=&lt;address&gt;</code> or <code>curl --noproxy '*'</code>. Your router or firewall must also allow the cluster nodes to reach the host.</p>
          </div>
          <div className="card-ft"><Button type="submit" variant="primary" disabled={saving}>{minutes ? 'Grant for ' + DURATIONS.find(([m]) => m === minutes)?.[1] : 'Grant LAN host'}</Button></div>
        </form>
      )}
    </div>
  );
}
