import {useCallback, useEffect, useState} from 'react';
import {sandboxAction, sandboxDetail, type SandboxDetail} from '../api';
import {useData} from '../data';
import {navigate, sandboxHref, SANDBOX_TABS, type SandboxTab} from '../router';
import {clock} from '../state';
import {Button, Callout, Card, CardHeader, Tabs, TabsContent, TabsList, TabsTrigger, Tag, TextArea, ToggleGroup, ToggleItem} from '../ui';
import {AccessTab} from './Access';
import {ActivityTab} from './Activity';
import {PreviewsTab} from './Previews';
import {SettingsTab} from './Settings';
import {SandboxRuns} from '../factory/SandboxRuns';

export const DURATIONS: [string, string][] = [['15', '15 min'], ['60', '1 hour'], ['240', '4 hours'], ['1440', '24 hours']];
const POLL_MS = 10000;

export interface SandboxContext {
  detail: SandboxDetail;
  reload: () => Promise<void>;
  // Runs a sandbox-control action, reports it, then refreshes the page and the list.
  act: (path: string, fields: Record<string, string | string[]>, done: string) => Promise<boolean>;
}

export const classicHref = (id: string, tab = 'access') => '/sandboxes/?workspace=' + encodeURIComponent(id) + '&tab=' + tab;
export const timeOf = (unix: number) => new Date(unix * 1000).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'});

function useSandbox(id: string) {
  const {reload: reloadList, toast, existing} = useData();
  const [detail, setDetail] = useState<SandboxDetail | null>(null);
  const [error, setError] = useState('');
  const reload = useCallback(async () => {
    try { setDetail(await sandboxDetail(id)); setError(''); }
    catch (e) { setError((e as Error).message); }
  }, [id]);
  useEffect(() => {
    setDetail(null); reload();
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') reload(); }, POLL_MS);
    return () => window.clearInterval(timer);
  }, [reload]);
  // The list poll notices egress changes made elsewhere sooner than this page's own poll.
  const summary = existing.find(s => s.id === id);
  useEffect(() => { if (summary) reload(); }, [summary?.egress, summary?.egressExpires, summary?.mode, reload]);
  const act = useCallback(async (path: string, fields: Record<string, string | string[]>, done: string) => {
    try {
      await sandboxAction(path, id, fields);
      toast(id + ': ' + done);
      await Promise.all([reload(), reloadList()]);
      return true;
    } catch (e) { toast((e as Error).message, true); return false; }
  }, [id, reload, reloadList, toast]);
  return {detail, error, reload, act};
}

function WebAccess({detail, act}: SandboxContext) {
  const [minutes, setMinutes] = useState(String(detail.learning?.minutes && DURATIONS.some(([v]) => v === String(detail.learning?.minutes)) ? detail.learning.minutes : 60));
  const [busy, setBusy] = useState(false);
  const learning = detail.learning;
  if (!detail.learningEgress || !learning) return (
    <Card variant="sunk" width="full"><CardHeader title="Web access" subtitle="This sandbox uses fixed policy rules." /><p className="muted">Instant open and lock are available for sandboxes with learning egress. Manage this sandbox's rules on the <a href={classicHref(detail.id)}>classic page</a>.</p></Card>
  );
  const run = async (action: string, done: string) => { setBusy(true); await act('/egress', {action: 'learning-' + action, minutes}, done); setBusy(false); };
  const open = learning.phase !== 'Locked';
  const description = learning.phase === 'Open' ? `Open to the public web until ${clock(learning.expires)}. Nothing is learned; locking removes this access.`
    : learning.phase === 'Learning' ? `Learning until ${clock(learning.expires)}. Every request made now becomes an approval.`
    : 'Locked to approved requests and provider hosts.';
  return (
    <section className={'egress-panel' + (open ? ' is-open' : '')} aria-labelledby="sandbox-egress-title">
      <div className="egress-copy">
        <h2 id="sandbox-egress-title">Web access</h2>
        <p className="egress-summary" data-testid="sandbox-egress">{description}</p>
        <p className="subtle">Private networks, cluster services and cloud metadata stay blocked in every state. Changes apply on the next request.</p>
      </div>
      <div className="egress-controls">
        <div className="field">
          <span className="field-label" id="sandbox-duration">Window</span>
          <ToggleGroup type="single" value={minutes} onValueChange={value => value && setMinutes(value)} aria-labelledby="sandbox-duration" className="durations">
            {DURATIONS.map(([value, label]) => <ToggleItem key={value} value={value} aria-label={'Window ' + label}>{label}</ToggleItem>)}
          </ToggleGroup>
        </div>
        <div className="egress-buttons">
          <Button variant="primary" size="large" disabled={busy} onClick={() => run('open', 'web access opened')}>{learning.phase === 'Open' ? 'Restart open window' : 'Open'}</Button>
          <Button variant="primary" buttonType="destructive" size="large" disabled={busy || !open} onClick={() => run('lock', 'web access locked')}>Lock</Button>
        </div>
        <div className="egress-secondary">
          <Button variant="tertiary" size="small" disabled={busy} onClick={() => run('resume', 'learning window started')}>{learning.phase === 'Learning' ? 'Restart learning' : 'Learn for this window'}</Button>
          <Button variant="tertiary" size="small" buttonType="destructive" disabled={busy}
            onClick={() => confirm('Start a fresh learning window? Every recorded request, approved or not, is deleted first.') && run('start', 'fresh learning window started')}>Start fresh</Button>
        </div>
      </div>
    </section>
  );
}

function Services({detail, act}: SandboxContext) {
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

function Overview(ctx: SandboxContext) {
  const {detail} = ctx;
  const facts: [string, string][] = [
    ['Namespace', detail.namespace], ['Sandbox', detail.sandbox], ['Policy revision', `${detail.revision.id} · ${detail.revision.status}`],
    ['Detected servers awaiting review', String(detail.previewPending)],
    ...(detail.egressCounts ? [['Web requests awaiting approval', String(detail.egressCounts.pending)], ['Approved web requests', String(detail.egressCounts.approved)]] as [string, string][] : []),
  ];
  return (
    <div className="stack">
      <WebAccess {...ctx} />
      <dl className="facts">{facts.map(([term, value]) => <div key={term}><dt>{term}</dt><dd>{value}</dd></div>)}</dl>
      {detail.providerHosts.length ? (
        <Card variant="sunk" width="full">
          <CardHeader title="Provider hosts" subtitle="Always reachable, in every web access state." />
          <div className="chip-list">{detail.providerHosts.map(h => <code key={h}>{h}</code>)}</div>
        </Card>
      ) : null}
      <Services key={detail.revision.id} {...ctx} />
    </div>
  );
}

export function Sandbox({id, tab}: {id: string; tab: SandboxTab}) {
  const {detail, error, reload, act} = useSandbox(id);
  const {session} = useData();
  const [working, setWorking] = useState(false);
  useEffect(() => { document.title = id + ' · Sandboxes'; }, [id]);
  const crumbs = <nav className="crumbs" aria-label="Breadcrumb"><a href="#/">Sandboxes</a><span aria-hidden="true">/</span><span>{id}</span></nav>;
  if (!detail) return <>{crumbs}<Card variant="sunk" width="full" className="empty-card">{error
    ? <><h2>Sandbox unavailable</h2><p className="muted">{error}</p><div className="actions"><Button variant="secondary" onClick={reload}>Retry</Button><a className="link-button" href={classicHref(id)}>Classic page</a></div></>
    : <p>Loading…</p>}</Card></>;
  const running = detail.mode === 'Running';
  const lifecycle = async () => {
    if (running && !confirm('Stop ' + id + '? Storage is kept; processes stop.')) return;
    setWorking(true);
    await act('/lifecycle', {mode: running ? 'Suspended' : 'Running'}, running ? 'stop requested' : 'start requested');
    setWorking(false);
  };
  const ctx: SandboxContext = {detail, reload, act};
  const counts: Partial<Record<SandboxTab, number>> = {access: detail.egressCounts?.pending, previews: detail.previewPending};
  const egress = detail.learning?.phase;
  return (
    <>
      {crumbs}
      <header className="detail-head" data-sandbox={id}>
        <div>
          <div className="title-row"><h1>{id}</h1>
            <Tag size="sm" variant={running ? 'success' : 'neutral'} text={running ? 'Running' : 'Stopped'} />
            {egress ? <Tag size="sm" variant={egress === 'Locked' ? 'success' : egress === 'Open' ? 'danger' : 'warning'} text={egress === 'Locked' ? 'Web locked' : `Web ${egress.toLowerCase()} until ${clock(detail.learning!.expires)}`} /> : null}
          </div>
          <p className="muted">{detail.namespace} · {detail.sandbox}</p>
        </div>
        <div className="actions">
          <a className="link-button subtle-link" href={classicHref(id, tab === 'overview' ? 'previews' : tab)}>Classic page</a>
          <Button variant={running ? 'secondary' : 'primary'} disabled={working} onClick={lifecycle}>{running ? 'Stop' : 'Start'}</Button>
        </div>
      </header>
      {detail.revision.error ? <Callout variant="danger" title={'Revision ' + detail.revision.id + ' ' + detail.revision.status.toLowerCase()} description={detail.revision.error} /> : null}
      {!detail.tailnetReady ? <Callout variant="warning" description="Tailscale routing is not ready; previews and services are reachable on the LAN only." /> : null}
      <Tabs value={tab} onValueChange={value => navigate(sandboxHref(id, value as SandboxTab))} className="detail-tabs">
        <TabsList aria-label="Sandbox sections">
          {SANDBOX_TABS.filter(([key]) => (key !== 'access' || detail.learningEgress) && (key !== 'runs' || session.runRecords)).map(([key, label]) => <TabsTrigger key={key} value={key} text={counts[key] ? `${label} ${counts[key]}` : label} />)}
        </TabsList>
        <TabsContent value="overview"><Overview {...ctx} /></TabsContent>
        {detail.learningEgress ? <TabsContent value="access"><AccessTab {...ctx} /></TabsContent> : null}
        <TabsContent value="previews"><PreviewsTab {...ctx} /></TabsContent>
        <TabsContent value="activity">{tab === 'activity' ? <ActivityTab {...ctx} /> : null}</TabsContent>
        {session.runRecords ? <TabsContent value="runs">{tab === 'runs' ? <SandboxRuns namespace={detail.namespace} /> : null}</TabsContent> : null}
        <TabsContent value="settings"><SettingsTab {...ctx} /></TabsContent>
      </Tabs>
    </>
  );
}

