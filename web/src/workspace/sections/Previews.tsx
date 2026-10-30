import {useCallback, useEffect, useMemo, useState} from 'react';
import type {DevLink, Preview} from '../../api';
import {useData} from '../../data';
import {DevLinksTab} from '../../sandbox/DevLinks';
import {PreviewsTab} from '../../sandbox/Previews';
import type {SandboxContext} from '../../sandbox/Sandbox';
import {Button, Card, Tag} from '../../ui';
import {NotAvailable, plural, SectionIntro} from '../bits';
import {browserSessionEntry, linkCounts, linkRows, loadDevLinks, loadPreviews, openBlocked, type LinkRow} from '../sources/previews';
import {ago, clockAt, stamp, useNow} from '../time';
import '../previews.css';

// The list refreshes on this cadence while the page is visible, and every few
// seconds while a dev link scan runs.
const REFRESH_MS = 30000, SCANNING_MS = 5000;

async function copyText(text: string, toast: (text: string, error?: boolean) => void, what: string) {
  try { await navigator.clipboard.writeText(text); toast(what + ' copied'); }
  catch { toast(text); }
}

// Moves focus to the detailed card of a row, below the list.
function reveal(id: string) {
  const el = document.getElementById(id);
  if (!el) return;
  el.scrollIntoView({block: 'start'});
  el.focus({preventScroll: true});
}

function Row({r, now}: {r: LinkRow; now: number}) {
  const {toast} = useData();
  const blocked = openBlocked(r);
  const reasonId = 'pv-why-' + r.key.replace(/[^a-z0-9-]/gi, '-');
  return (
    <li className={'pv-row is-' + r.status} data-link={r.key}>
      <div className="pv-status"><Tag size="sm" variant={r.variant} text={r.label} /></div>
      <div className="pv-main">
        <strong className="pv-name">{r.name}</strong>
        <span className="pv-sub">{r.kind === 'devlink' ? 'Dev link' : 'Detected server'} · {r.where}</span>
        {r.urls.map(u => (
          <span className="pv-url" key={u.url}>
            {r.urls.length > 1 ? <span className="pv-url-label">{u.label}</span> : null}
            <code>{u.url}</code>
            <button type="button" className="pv-mini" onClick={() => copyText(u.url, toast, u.label + ' URL')} aria-label={`Copy ${u.label} URL of ${r.name}`}>Copy</button>
          </span>
        ))}
        {r.note ? <span className="pv-note">{r.note}</span> : null}
      </div>
      <div className="pv-meta">
        {r.checked ? <span title={stamp(r.checked)}>{r.checkedLabel} {ago(r.checked, now)}</span> : <span>{r.checkedLabel}: unknown</span>}
        {r.expires && r.status === 'serving' ? <span title={stamp(r.expires)}>Until {stamp(r.expires)}</span> : null}
      </div>
      <div className="pv-actions">
        {r.open
          ? <a className="link-button pv-open" href={r.open} target="_blank" rel="noopener noreferrer">Open<span aria-hidden="true"> ↗</span><span className="visually-hidden"> {r.name} in a new tab</span></a>
          : <Button variant="secondary" size="small" disabled aria-describedby={reasonId}>Open</Button>}
        {r.status === 'pending' ? <Button variant="tertiary" size="small" onClick={() => reveal(r.target)}>Review</Button> : null}
        {blocked ? <span className="pv-why" id={reasonId}>{blocked}</span> : null}
      </div>
    </li>
  );
}

function BrowserSession({kind}: {kind: 'sandbox' | 'project'}) {
  const entry = browserSessionEntry(kind);
  return (
    <Card variant="sunk" width="full" className="pv-browser" data-testid="browser-session">
      <div className="pv-browser-row">
        <div className="pv-main">
          <strong>View the page in the console</strong>
          <span className="pv-note">{entry.detail}</span>
        </div>
        <div className="pv-actions">
          <Button variant="secondary" size="small" disabled aria-describedby="pv-browser-why">Open browser session</Button>
          <span className="pv-why" id="pv-browser-why">{entry.reason}</span>
        </div>
      </div>
    </Card>
  );
}

// Every dev link and detected server of a sandbox in one list, then the
// detailed cards where the decisions are made.
export function Previews(ctx: SandboxContext) {
  const {detail, act} = ctx;
  const {toast} = useData();
  const now = useNow(15000);
  const [links, setLinks] = useState<DevLink[] | null>(null);
  const [servers, setServers] = useState<{expiredCount: number; requests: Preview[]} | null>(null);
  const [inactive, setInactive] = useState(false);
  const [checked, setChecked] = useState(0);
  const [loading, setLoading] = useState(false);

  const loadLinks = useCallback(async () => {
    try { setLinks(await loadDevLinks(detail.id)); setChecked(Date.now()); } catch (e) { toast((e as Error).message, true); }
  }, [detail.id, toast]);
  const loadServers = useCallback(async () => {
    try { setServers(await loadPreviews(detail.id, inactive)); setChecked(Date.now()); } catch (e) { toast((e as Error).message, true); }
  }, [detail.id, inactive, toast]);
  const refresh = useCallback(async () => {
    setLoading(true);
    await Promise.all([loadLinks(), loadServers()]);
    setLoading(false);
  }, [loadLinks, loadServers]);

  useEffect(() => { loadLinks(); }, [loadLinks, detail.devLinkPending]);
  useEffect(() => { loadServers(); }, [loadServers, detail.previewPending]);
  const scanning = Boolean(links?.some(l => l.State === 'Scanning'));
  useEffect(() => {
    const t = window.setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, scanning ? SCANNING_MS : REFRESH_MS);
    return () => window.clearInterval(t);
  }, [refresh, scanning]);

  const rows = useMemo(() => linkRows(links, servers?.requests || null, now), [links, servers, now]);
  const counts = linkCounts(rows);
  const summary = [counts.pending ? `${counts.pending} waiting` : '', counts.serving ? `${counts.serving} serving` : '', counts.approved ? `${counts.approved} approved` : '', counts.stale ? `${counts.stale} stale` : '', counts.inactive ? `${counts.inactive} inactive` : ''].filter(Boolean).join(' · ');
  const ready = links !== null && servers !== null;

  return (
    <div className="stack">
      <SectionIntro title="Previews & dev links">Every app this sandbox serves, with its status and address. Approve or revoke in the cards below the list.</SectionIntro>
      <Card variant="sunk" width="full" className="pv-card" data-testid="link-list">
        <div className="pv-head">
          <div className="pv-head-text">
            <h3>All links</h3>
            <span className="pv-sub">{ready ? (rows.length ? plural(rows.length, 'link') + (summary ? ' · ' + summary : '') : 'Nothing published or detected yet') : 'Loading…'}</span>
          </div>
          <div className="pv-head-end">
            <span className="pv-sub" aria-live="polite">{checked ? `Checked ${clockAt(checked)}` : ''}</span>
            <Button variant="secondary" size="small" disabled={loading} onClick={refresh}>{loading ? 'Checking…' : 'Refresh'}</Button>
          </div>
        </div>
        {ready && rows.length ? <ul className="pv-list" aria-label="Previews and dev links">{rows.map(r => <Row key={r.key} r={r} now={now} />)}</ul>
          : ready ? <p className="pv-empty muted">Agents publish an app with <code>orca-pilot dev-link publish --name NAME --port PORT</code>; servers the agent starts on <code>0.0.0.0</code> appear on their own.</p> : null}
      </Card>
      <BrowserSession kind="sandbox" />
      <h3 className="sub-head" id="pv-devlinks">Dev links</h3>
      <DevLinksTab act={act} links={links} reload={loadLinks} />
      <h3 className="sub-head" id="pv-servers">Detected servers</h3>
      <PreviewsTab detail={detail} act={act} data={servers} inactive={inactive} setInactive={setInactive} reload={loadServers} />
    </div>
  );
}

// Project sandboxes have no previews or dev links API yet; browser sessions
// (PR #76) would arrive here first.
export function ProjectPreviews() {
  return (
    <div className="stack">
      <SectionIntro title="Previews & dev links" />
      <NotAvailable what="Previews and dev links" needs="Project sandboxes do not publish previews or dev links yet." />
      <BrowserSession kind="project" />
    </div>
  );
}
