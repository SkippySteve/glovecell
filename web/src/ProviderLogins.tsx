import {useEffect, useRef, useState} from 'react';
import {api} from './api';
import {useData} from './data';
import {Button, Callout} from './ui';

// Console sign-in for the operator's own provider subscriptions. The broker
// runs the provider's own sign-in in a short-lived pod and shows its link and
// code here; the login itself never reaches the browser or any sandbox.
export type LoginStatus = {provider: string; state: 'idle' | 'starting' | 'waiting' | 'completing' | 'done' | 'failed' | 'canceled'; url?: string; code?: string; message?: string; replaceAccount?: boolean};

const LABELS: Record<string, string> = {grok: 'Grok', cursor: 'Cursor'};
const RUNNING = new Set(['starting', 'waiting', 'completing']);

export const isRunning = (s?: LoginStatus) => !!s && RUNNING.has(s.state);

// Only links on the provider's own sign-in hosts are ever shown (the broker
// filters them too).
export function safeLink(provider: string, url?: string): string | undefined {
  if (!url) return undefined;
  try {
    const u = new URL(url);
    const hosts: Record<string, string[]> = {grok: ['accounts.x.ai', 'auth.x.ai'], cursor: ['cursor.com', 'www.cursor.com']};
    return u.protocol === 'https:' && (hosts[provider] || []).includes(u.hostname) && !u.username ? u.toString() : undefined;
  } catch { return undefined; }
}

export function ProviderLogins({providers}: {providers: string[]}) {
  const {toast} = useData();
  const [logins, setLogins] = useState<Record<string, LoginStatus>>({});
  const [replace, setReplace] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState('');
  const timer = useRef<ReturnType<typeof setInterval> | null>(null);
  const load = async () => {
    try {
      const r = await api<{logins?: LoginStatus[]}>('provider-logins');
      setLogins(Object.fromEntries((r.logins || []).map(s => [s.provider, s])));
    } catch { /* shown on the next action */ }
  };
  const anyRunning = Object.values(logins).some(isRunning);
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    if (!anyRunning) return;
    timer.current = setInterval(() => { void load(); }, 2000);
    return () => { if (timer.current) clearInterval(timer.current); };
  }, [anyRunning]);
  const start = async (provider: string) => {
    setBusy(provider);
    try {
      const s = await api<LoginStatus>('provider-logins/' + encodeURIComponent(provider), 'POST', {replaceAccount: !!replace[provider]});
      setLogins(current => ({...current, [provider]: s}));
    } catch (error) { toast((error as Error).message, true); }
    finally { setBusy(''); }
  };
  const cancel = async (provider: string) => {
    setBusy(provider);
    try { await api('provider-logins/' + encodeURIComponent(provider), 'DELETE'); await load(); }
    catch (error) { toast((error as Error).message, true); }
    finally { setBusy(''); }
  };
  return (
    <section aria-label="Sign in to providers">
      <h4>Your subscription logins</h4>
      <p className="subtle">Sign in once per provider. Glovecell keeps the login outside every sandbox and renews it for you. Or copy an existing login from your machine: <code>agent-project-controller provider import grok</code>.</p>
      <ul className="items">
        {providers.map(provider => {
          const s = logins[provider];
          const link = safeLink(provider, s?.url);
          const running = isRunning(s);
          return <li key={provider}>
            <div className="item-main">
              <strong>{LABELS[provider] || provider}</strong>
              {s?.message ? <span role="status">{s.message}</span> : null}
              {running && link ? <span>1. Open <a href={link} target="_blank" rel="noopener noreferrer">the {LABELS[provider]} sign-in page</a>{s?.code ? <> and confirm the code <code aria-label="sign-in code">{s.code}</code></> : null}. 2. Approve. This page updates by itself.</span> : null}
              {provider === 'cursor' && !running ? <label className="subtle"><input type="checkbox" checked={!!replace[provider]} onChange={e => setReplace(r => ({...r, [provider]: e.target.checked}))} /> Replace the signed-in Cursor account</label> : null}
            </div>
            {running
              ? <Button type="button" size="small" variant="tertiary" disabled={busy === provider} aria-label={'Cancel ' + LABELS[provider] + ' sign-in'} onClick={() => cancel(provider)}>Cancel</Button>
              : <Button type="button" size="small" variant="secondary" disabled={busy === provider || anyRunning} aria-label={'Connect ' + LABELS[provider]} onClick={() => start(provider)}>{s?.state === 'done' ? 'Sign in again' : 'Connect'}</Button>}
          </li>;
        })}
      </ul>
      {anyRunning ? <Callout variant="neutral" description="Only continue with a code you requested here. Don't share the code with anyone." /> : null}
    </section>
  );
}
