import {useCallback, useEffect, useState} from 'react';
import type {DevLink} from '../api';
import {loadDevLinks} from '../workspace/sources/previews';
import {useData} from '../data';
import {Button, Card, Tag, TextArea, ToggleGroup, ToggleItem, type TagVariant} from '../ui';
import {timeOf, type SandboxContext} from './Sandbox';

const STATE: Record<string, TagVariant> = {Approved: 'success', Pending: 'warning', Scanning: 'info', Offline: 'neutral', 'Scan failed': 'danger', Denied: 'danger', Revoked: 'danger', Expired: 'neutral'};
const TTL: [string, string][] = [['1d', '1 day'], ['7d', '7 days'], ['30d', '30 days']];
const short = (s: string) => s.slice(0, 12);

function Finding({ok, warn, title, detail}: {ok: boolean; warn?: boolean; title: string; detail: string}) {
  return (
    <li className={'devlink-check ' + (ok ? 'is-ok' : warn ? 'is-warn' : 'is-bad')}>
      <span aria-hidden="true">{ok ? '✓' : warn ? '!' : '✗'}</span>
      <div><strong>{title}</strong><span className="subtle">{detail}</span></div>
    </li>
  );
}

function DevLinkCard({l, act, reload}: {l: DevLink; act: SandboxContext['act']; reload: () => Promise<void>}) {
  const {toast} = useData();
  const [ttl, setTtl] = useState('7d');
  const [rules, setRules] = useState(l.RulesText);
  const [busy, setBusy] = useState(false);
  const s = l.Scan, c = l.Counts;
  const scanned = s.Status === 'Complete';
  const decide = async (state: 'Approved' | 'Denied' | 'Revoked' | 'Deleted') => {
    if (state === 'Approved' && (c.critical || c.high || c.secrets)
      && !confirm(`${l.Name} has ${c.critical} critical and ${c.high} high vulnerabilities and ${c.secrets} possible secrets. Approve it anyway?`)) return;
    if (state === 'Deleted' && !confirm(`Remove ${l.Name} and its history? The agent can publish the name again.`)) return;
    setBusy(true);
    if (await act('/devlink-decision', {name: l.Name, state, duration: ttl, paths_text: rules, tab: 'previews'}, `${l.Name} ${state.toLowerCase()}`)) await reload();
    setBusy(false);
  };
  const copy = async () => {
    try { await navigator.clipboard.writeText(l.URL); toast('Link copied; it opens on the verification card'); }
    catch { toast(l.URL); }
  };
  const verify = l.URL + '_dev/verify';
  return (
    <Card variant="sunk" width="full" className="devlink" data-devlink={l.Name}>
      <div className="preview-head">
        <div>
          <h3>{l.Live ? <a href={l.URL} target="_blank" rel="noopener noreferrer">{l.Host}</a> : l.Host}</h3>
          <p className="muted">{l.Project && l.Project !== 'unknown' ? `${l.Project} / ${l.Worktree} · ` : ''}<code>{l.Cwd}</code> · port {l.Port} · requested {timeOf(l.Requested)}</p>
        </div>
        <Tag size="sm" variant={l.Live ? 'success' : STATE[l.State] || 'neutral'} text={l.Live ? 'Live' : l.State} />
      </div>
      {l.Note ? <p className="devlink-note">Agent's description (unverified): {l.Note}</p> : null}
      {s.Status === 'Running' ? <p className="muted">Scanning since {timeOf(s.Started)}…</p> : null}
      {s.Status === 'Failed' ? <p className="error">Scan failed: {s.Error}</p> : null}
      {scanned ? (
        <ul className="devlink-checks">
          <Finding ok={s.Worktree === 'clean'} warn title={s.Commit ? `Commit ${short(s.Commit)}${s.Branch ? ' on ' + s.Branch : ''}` : 'Not in Git'}
            detail={s.Worktree === 'clean' ? 'Working tree matches the commit' : s.Worktree === 'modified' ? `${c.modified} files differ from the commit` : 'Git state unknown'} />
          <Finding ok={!c.unapproved} warn title={`${c.packages} dependencies`} detail={c.unapproved ? `${c.unapproved} not on the workspace package list` : 'All on the workspace package list'} />
          <Finding ok={!(c.critical || c.high || c.medium || c.low)} warn={!(c.critical || c.high)} title={`${c.critical} critical · ${c.high} high · ${c.medium} medium · ${c.low} low`} detail={'Known vulnerabilities, ' + (s.Scanner || 'Trivy')} />
          <Finding ok={!c.secrets} title={c.secrets ? `${c.secrets} possible secrets` : 'No secrets found'} detail="Trivy secret rules over the source" />
        </ul>
      ) : null}
      {l.Live ? <p className="subtle">Approved by {l.Actor} for build {short(l.ApprovedDigest)} until {timeOf(l.Expires)}. Private to {l.Viewers}; {l.AccessTotal} visits.</p> : null}
      <p className="preview-links"><a href={verify} target="_blank" rel="noopener noreferrer">Verification card</a></p>
      {['Pending', 'Denied', 'Revoked'].includes(l.State) && scanned ? (
        <>
          <div className="field"><label className="field-label" htmlFor={'dev-rules-' + l.Name}>Allowed requests, one per line: METHODS [prefix:]/path</label>
            <TextArea id={'dev-rules-' + l.Name} rows={2} value={rules} onChange={e => setRules(e.target.value)} /></div>
          <div className="field"><span className="field-label" id={'dev-ttl-' + l.Name}>Approval lasts</span>
            <ToggleGroup type="single" value={ttl} onValueChange={v => v && setTtl(v)} aria-labelledby={'dev-ttl-' + l.Name} className="durations">
              {TTL.map(([v, label]) => <ToggleItem key={v} value={v} aria-label={'Approve for ' + label}>{label}</ToggleItem>)}
            </ToggleGroup></div>
        </>
      ) : null}
      <div className="actions">
        {['Pending', 'Denied', 'Revoked'].includes(l.State) && scanned ? <Button variant="primary" size="small" disabled={busy} onClick={() => decide('Approved')}>Approve build {short(s.Digest)}</Button> : null}
        {l.Live ? <Button variant="secondary" size="small" onClick={copy}>Copy link</Button> : null}
        {['Approved', 'Offline', 'Scanning'].includes(l.State) && l.ApprovedDigest ? <Button variant="secondary" buttonType="destructive" size="small" disabled={busy} onClick={() => decide('Revoked')}>Revoke</Button> : null}
        {['Pending', 'Scan failed', 'Offline'].includes(l.State) && !l.ApprovedDigest ? <Button variant="tertiary" size="small" disabled={busy} onClick={() => decide('Denied')}>Deny</Button> : null}
        {l.State !== 'Scanning' ? <Button variant="tertiary" size="small" buttonType="destructive" disabled={busy} onClick={() => decide('Deleted')}>Remove</Button> : null}
      </div>
    </Card>
  );
}

export function DevLinksTab({detail, act}: SandboxContext) {
  const {toast} = useData();
  const [links, setLinks] = useState<DevLink[] | null>(null);
  const load = useCallback(async () => {
    try { setLinks(await loadDevLinks(detail.id)); } catch (e) { toast((e as Error).message, true); }
  }, [detail.id, toast]);
  useEffect(() => { load(); }, [load, detail.devLinkPending]);
  // A scan takes a minute or two; refresh while one runs.
  useEffect(() => {
    if (!links?.some(l => l.State === 'Scanning')) return;
    const timer = window.setTimeout(load, 5000);
    return () => window.clearTimeout(timer);
  }, [links, load]);
  return (
    <div className="stack">
      <div className="section-intro">
        <p className="muted">Apps an agent published with <code>dev-link publish</code>. Each build is scanned for dependencies, vulnerabilities and secrets. An approved link is private to you behind Authentik, and always opens on its verification card. A restart with changed files or dependencies needs approval again.</p>
      </div>
      {!links ? <p className="empty">Loading dev links…</p> : links.length
        ? links.map(l => <DevLinkCard key={l.Name + l.State + l.Scan.ID} l={l} act={act} reload={load} />)
        : <Card variant="sunk" width="full" className="empty-card"><p className="muted">No dev links yet. Agents publish one with <code>orca-pilot dev-link publish --name NAME --port PORT</code>.</p></Card>}
    </div>
  );
}
