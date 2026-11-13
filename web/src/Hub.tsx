import {useCallback, useEffect, useState} from 'react';
import {api, type HubDevice, type HubOffer} from './api';
import {useData} from './data';
import {Button, Callout, Card, CardHeader} from './ui';

export function Hub() {
  const {session} = useData();
  const [offer, setOffer] = useState<HubOffer | null>(null);
  const [devices, setDevices] = useState<HubDevice[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    if (!session.hubPairing) return;
    setError('');
    try {
      const list = await api<{devices: HubDevice[]}>('hub/devices');
      setDevices(list.devices || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'hub devices unavailable');
    }
  }, [session.hubPairing]);

  useEffect(() => { refresh(); }, [refresh]);

  if (!session.hubPairing) {
    return <Callout variant="warning" title="Hub pairing is off" description="This controller has no HUB_ADMIN_URL. Phone pairing stays on the hub pod until that private admin path is configured." />;
  }

  const mint = async () => {
    setBusy(true); setError('');
    try {
      setOffer(await api<HubOffer>('hub/pairing', 'POST'));
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'pairing unavailable');
    } finally { setBusy(false); }
  };

  const revoke = async (id: string) => {
    setBusy(true); setError('');
    try {
      await api('hub/devices/' + encodeURIComponent(id), 'DELETE');
      if (offer?.deviceId === id) setOffer(null);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'revocation unavailable');
    } finally { setBusy(false); }
  };

  return (
    <div className="stack">
      <Card>
        <CardHeader title="Phone hub" subtitle="Mint a short-lived phone offer, then revoke devices when they leave your trust boundary." />
        <div className="row">
          <Button variant="primary" disabled={busy} onClick={mint}>Mint phone offer</Button>
          <Button disabled={busy} onClick={refresh}>Refresh devices</Button>
        </div>
        {error ? <Callout variant="danger" description={error} /> : null}
        {offer?.available ? (
          <div className="stack" data-testid="hub-offer">
            <p className="subtle">Expires {new Date(offer.expiresAt).toLocaleString()}. Endpoint {offer.endpoint}.</p>
            {offer.qrDataUrl ? <img alt="Phone pairing QR" src={offer.qrDataUrl} width={240} height={240} /> : null}
            <code className="block">{offer.pairingUrl}</code>
          </div>
        ) : null}
      </Card>
      <Card>
        <CardHeader title="Paired devices" subtitle="Revocation is immediate for new dials; already-open sessions drop on the next hub check." />
        {!devices.length ? <p className="subtle">No paired devices.</p> : (
          <ul className="stack">
            {devices.map(d => (
              <li key={d.deviceId} className="row">
                <span>{d.name || d.deviceId}</span>
                <Button buttonType="destructive" disabled={busy} onClick={() => revoke(d.deviceId)}>Revoke</Button>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
