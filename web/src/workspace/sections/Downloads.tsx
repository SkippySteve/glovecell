// "Download into sandbox": check a URL's redirect chain, approve exactly that
// chain, and let the sandbox fetch it through its own proxy. Data and actions
// come only from a DownloadSource (../sources/downloads).
import {useCallback, useEffect, useRef, useState} from 'react';
import {Button, Callout, Tag} from '../../ui';
import {NotAvailable, SectionIntro} from '../bits';
import {useConfirm} from '../Confirm';
import {checkDestination, checkSha256, checkUrl, final, formatBytes, hostsOf, jobSummary, maskQuery, normalSha256, progressPercent, splitUrl, statusLabel, TERMINAL, tooLarge, type DownloadCheck, type DownloadHop, type DownloadJob, type DownloadSource} from '../sources/downloads';
import {stamp} from '../time';

const CHECK_POLL_MS = 700;
const JOB_POLL_MS = 1000;

function HopUrl({url, show}: {url: string; show: boolean}) {
  const p = splitUrl(url);
  return <code className="dl-url"><span className="dl-scheme">{p.origin}</span><strong className="dl-host">{p.host}</strong>{p.path}{p.query ? <span className="dl-query">{show ? p.query : maskQuery(p.query)}</span> : null}</code>;
}

// The checked chain: what Steve typed, then every hop with its status, the
// host emphasised where it changes, and the final file's size and type.
export function Chain({check, show}: {check: DownloadCheck; show: boolean}) {
  const last = final(check);
  return (
    <ol className="dl-chain" aria-label="Redirect chain">
      {check.hops.map((h: DownloadHop, i: number) => (
        <li key={i} className={'dl-hop' + (h.crossHost ? ' cross' : '') + (h.refused ? ' refused' : '')} data-hop={i}>
          <span className="dl-step">{i === 0 ? 'You entered' : `Redirect ${i}`}</span>
          <div className="dl-hop-main">
            <HopUrl url={h.url} show={show} />
            <span className="dl-tags">
              {h.status ? <Tag size="sm" variant={h.status === 200 ? 'success' : h.status >= 300 && h.status < 400 ? 'neutral' : 'danger'} text={statusLabel(h.status)} /> : null}
              {h.crossHost ? <Tag size="sm" variant="warning" text={'new host: ' + h.host} /> : null}
              {h.probe === 'GET' ? <span className="subtle-text">server refused HEAD; checked with a GET closed after its headers</span> : null}
            </span>
            {h.refused ? <span className="error-text">Not followed: {h.refused}</span> : null}
            {h === last && h.status === 200 ? <span className="subtle-text">File: {h.contentLength >= 0 ? formatBytes(h.contentLength) : 'size not announced'}{h.contentType ? ' · ' + h.contentType : ''}</span> : null}
          </div>
        </li>
      ))}
      {check.state === 'checking' ? <li className="dl-hop pending"><span className="dl-step">Checking…</span><div className="dl-hop-main subtle-text">Asking the server through the sandbox proxy (HEAD, no download).</div></li> : null}
    </ol>
  );
}

function JobCard({job, onCancel, onRecheck}: {job: DownloadJob; onCancel: () => void; onRecheck: () => void}) {
  const pct = progressPercent(job);
  const live = !TERMINAL.includes(job.state);
  return (
    <section className={'card dl-job ' + job.state} aria-label="Download" data-download={job.id} data-state={job.state}>
      <div className="card-hd">
        <h3>{live ? 'Downloading into the sandbox' : job.state === 'done' ? 'Downloaded' : job.state === 'changed' ? 'Redirect chain changed' : job.state === 'cancelled' ? 'Cancelled' : 'Download failed'}</h3>
        <span className="end"><Tag size="sm" variant={job.state === 'done' ? 'success' : live ? 'neutral' : job.state === 'changed' ? 'warning' : 'danger'} text={job.state} /></span>
      </div>
      <div className="card-bd">
        <p role="status">{jobSummary(job)}</p>
        {live ? <progress className="dl-progress" max={100} value={pct} aria-label="Download progress">{pct === undefined ? '' : pct + '%'}</progress> : null}
        {job.state === 'done' ? (
          <dl className="dl-result">
            <dt>Saved to</dt><dd><code>{job.path || job.destination}</code></dd>
            <dt>Size</dt><dd>{formatBytes(job.bytes)} ({job.bytes.toLocaleString()} bytes)</dd>
            <dt>SHA-256</dt><dd><code className="dl-sha">{job.sha256}</code>{job.expectedSha256 ? <Tag size="sm" variant="success" text="matches the expected hash" /> : null}</dd>
          </dl>
        ) : null}
        {job.note ? <p className="subtle-text">{job.note}</p> : null}
        {job.change ? (
          <div className="warn-box" data-testid="chain-change">
            <p><strong>Hop {job.change.hop + 1} changed.</strong> {job.change.queryOnly ? 'Same host and path, different query string (signed links often get a fresh signature on every request).' : 'The server now redirects somewhere you did not approve.'}</p>
            <p>Approved: <code>{job.change.expected}</code><br />Now: <code>{job.change.observed}</code></p>
            <p>Nothing was downloaded from the new URL. Check the chain again and approve it if it looks right.</p>
          </div>
        ) : null}
        {job.followed.length ? <details className="dl-followed"><summary>URLs used ({job.followed.length}, query strings hashed)</summary><ol>{job.followed.map((u, i) => <li key={i}><code>{u}</code></li>)}</ol></details> : null}
      </div>
      <div className="card-ft">
        {live ? <Button variant="secondary" onClick={onCancel}>Cancel download</Button> : null}
        {job.state === 'changed' ? <Button variant="primary" onClick={onRecheck}>Check the new chain</Button> : null}
        <span className="subtle-text end">{job.destination}{job.overwrite ? ' · overwrite' : ''}</span>
      </div>
    </section>
  );
}

export function Downloads({source, workspace}: {source: DownloadSource; workspace: string}) {
  const ask = useConfirm();
  const [url, setUrl] = useState('');
  const [check, setCheck] = useState<DownloadCheck | null>(null);
  const [error, setError] = useState('');
  const [show, setShow] = useState(false);
  const [destination, setDestination] = useState('');
  const [sha, setSha] = useState('');
  const [overwrite, setOverwrite] = useState(false);
  const [acceptQuery, setAcceptQuery] = useState(false);
  const [reapprove, setReapprove] = useState(false);
  const [job, setJob] = useState<DownloadJob | null>(null);
  const [recent, setRecent] = useState<DownloadJob[]>([]);
  const [busy, setBusy] = useState(false);
  const lastDefault = useRef('');

  const refreshRecent = useCallback(() => { if (source.available) source.list().then(setRecent, () => undefined); }, [source]);
  useEffect(refreshRecent, [refreshRecent]);

  // Poll a running check, then prefill the destination from the URL's file name.
  useEffect(() => {
    if (!check || check.state !== 'checking') return;
    const t = window.setTimeout(() => source.getCheck(check.id).then(next => {
      setCheck(next);
      if (next.state !== 'checking' && next.defaultDestination && (!destination || destination === lastDefault.current)) {
        setDestination(next.defaultDestination);
        lastDefault.current = next.defaultDestination;
      }
    }, (e: Error) => setError(e.message)), CHECK_POLL_MS);
    return () => window.clearTimeout(t);
  }, [check, source, destination]);

  useEffect(() => {
    if (!job || TERMINAL.includes(job.state)) return;
    const t = window.setTimeout(() => source.get(job.id).then(next => { setJob(next); if (TERMINAL.includes(next.state)) refreshRecent(); }, (e: Error) => setError(e.message)), JOB_POLL_MS);
    return () => window.clearTimeout(t);
  }, [job, source, refreshRecent]);

  if (!source.available) return (
    <div className="stack">
      <SectionIntro title="Downloads">Download a file from the internet into {workspace}.</SectionIntro>
      <NotAvailable what="Download into sandbox" needs={source.reason} />
    </div>
  );

  const urlError = url ? checkUrl(url) : null;
  const destError = destination ? checkDestination(destination) : null;
  const shaError = checkSha256(sha);
  const running = (check?.state === 'checking') || Boolean(job && !TERMINAL.includes(job.state));
  const ready = check?.state === 'ready';
  const large = check && ready && tooLarge(check, source.maxBytes);

  const runCheck = async (target: string, keepAccept = false) => {
    setError(''); setBusy(true);
    if (!keepAccept) { setAcceptQuery(false); setReapprove(false); }
    try {
      setCheck(await source.check(target.trim()));
      if (!keepAccept) setJob(null);
    } catch (e) { setError((e as Error).message); setCheck(null); }
    setBusy(false);
  };

  const start = async () => {
    if (!check || !ready) return;
    const hosts = hostsOf(check.hops);
    const ok = await ask('Download into ' + workspace + '?', <>
      <p>The sandbox fetches the file through its own proxy, with a one-time permission for each of these {check.hops.length} exact URL{check.hops.length === 1 ? '' : 's'} (valid 10 minutes, used once):</p>
      <ul className="dl-confirm-hosts">{check.hops.map((h, i) => <li key={i}><code>{h.redacted}</code>{h.crossHost ? <strong> · new host</strong> : null}</li>)}</ul>
      {hosts.length > 1 ? <p className="warn-box">The file comes from <strong>{hosts[hosts.length - 1]}</strong>, not {hosts[0]}.</p> : null}
      <p>Saved as <code>{destination}</code>{overwrite ? <strong> (replacing an existing file)</strong> : ' (refused if a file is already there)'}. At most {formatBytes(source.maxBytes)}.{normalSha256(sha) ? ' The file is deleted unless its SHA-256 matches.' : ''}</p>
      {acceptQuery ? <p className="subtle-text">Query strings may change on the same host and path (signed links).</p> : null}
    </>, 'Download');
    if (!ok) return;
    setBusy(true); setError('');
    try {
      const j = await source.start(check, {destination, sha256: normalSha256(sha), overwrite, acceptQueryChanges: acceptQuery});
      setJob(j);
      setCheck(null);
      setReapprove(false);
    } catch (e) { setError((e as Error).message); }
    setBusy(false);
  };

  const recheck = () => {
    if (!job) return;
    // A query-only change (fresh signature) is offered as a one-click re-approval
    // that accepts refreshed query strings on the same hosts and paths.
    const queryOnly = Boolean(job.change?.queryOnly);
    setAcceptQuery(queryOnly);
    setReapprove(true);
    setDestination(job.destination);
    setOverwrite(job.overwrite);
    setSha(job.expectedSha256);
    setUrl(job.input);
    runCheck(job.input, true);
  };

  return (
    <div className="stack" data-testid="downloads">
      <SectionIntro title="Downloads" aside={`https only · at most ${formatBytes(source.maxBytes)}`}>
        Download a file from the internet into {workspace}. First check the URL: the sandbox asks the server through its own proxy and shows every redirect. Then approve exactly that chain; the sandbox downloads the file itself, with a one-time permission per URL.
      </SectionIntro>
      {error ? <Callout variant="danger" description={error} /> : null}
      <section className="card" aria-labelledby="dl-check-title">
        <div className="card-hd"><h3 id="dl-check-title">1. Check the URL</h3></div>
        <form className="card-bd" onSubmit={e => { e.preventDefault(); if (url && !urlError && !running) runCheck(url); }}>
          <div className="field">
            <label className="field-label" htmlFor="dl-url">URL</label>
            <input id="dl-url" className="text-input wide" type="url" inputMode="url" autoComplete="off" spellCheck={false} placeholder="https://example.com/releases/tool.tar.gz" value={url} onChange={e => { setUrl(e.target.value); setCheck(null); setReapprove(false); }} aria-invalid={Boolean(urlError) || undefined} aria-describedby="dl-url-help" />
            <small id="dl-url-help" className={urlError ? 'error-text' : 'subtle-text'}>{urlError || 'Checking sends a HEAD request through the sandbox proxy; nothing is downloaded yet.'}</small>
          </div>
          <div><Button variant={ready ? 'secondary' : 'primary'} disabled={!url || Boolean(urlError) || running || busy} onClick={() => runCheck(url)}>{check?.state === 'checking' ? 'Checking…' : 'Check'}</Button></div>
        </form>
      </section>
      {check ? (
        <section className="card" aria-labelledby="dl-chain-title" data-testid="download-chain" data-state={check.state}>
          <div className="card-hd">
            <h3 id="dl-chain-title">{check.state === 'checking' ? 'Following redirects…' : check.hops.length > 1 ? `Redirect chain (${check.hops.length - 1} redirect${check.hops.length === 2 ? '' : 's'})` : 'No redirects'}</h3>
            <span className="end"><label className="check"><input type="checkbox" checked={show} onChange={e => setShow(e.target.checked)} /> Show query strings</label></span>
          </div>
          <div className="card-bd">
            <Chain check={check} show={show} />
            {check.state === 'failed' ? <p className="error-text" role="alert">{check.error || 'The chain cannot be downloaded.'}</p> : null}
            {large ? <p className="error-text" role="alert">The server announces {formatBytes(final(check)!.contentLength)}; the limit is {formatBytes(source.maxBytes)}.</p> : null}
            {hostsOf(check.hops).length > 1 && check.state !== 'checking' ? <p className="warn-box">This download crosses hosts: {hostsOf(check.hops).join(' → ')}. Approving it approves these exact URLs only.</p> : null}
            {reapprove && ready ? <p className="hint">This is the new chain. {acceptQuery ? 'Query-only changes on these hosts and paths will be accepted during the download.' : ''}</p> : null}
          </div>
        </section>
      ) : null}
      {ready && check ? (
        <section className="card" aria-labelledby="dl-save-title">
          <div className="card-hd"><h3 id="dl-save-title">2. Save into the sandbox</h3></div>
          <div className="card-bd">
            <div className="field">
              <label className="field-label" htmlFor="dl-dest">Save as</label>
              <input id="dl-dest" className="text-input wide" autoComplete="off" spellCheck={false} value={destination} onChange={e => setDestination(e.target.value)} aria-invalid={Boolean(destError) || undefined} aria-describedby="dl-dest-help" />
              <small id="dl-dest-help" className={destError ? 'error-text' : 'subtle-text'}>{destError || 'Under ~/ (the sandbox home) or /workspace/. Folders are created; symlinks are refused.'}</small>
            </div>
            <div className="field">
              <label className="field-label" htmlFor="dl-sha">Expected SHA-256 (optional)</label>
              <input id="dl-sha" className="text-input wide mono" autoComplete="off" spellCheck={false} placeholder="64 hex characters" value={sha} onChange={e => setSha(e.target.value)} aria-invalid={Boolean(shaError) || undefined} aria-describedby="dl-sha-help" />
              <small id="dl-sha-help" className={shaError ? 'error-text' : 'subtle-text'}>{shaError || 'If it does not match, the file is deleted and the download reports the mismatch.'}</small>
            </div>
            <label className="check"><input type="checkbox" checked={overwrite} onChange={e => setOverwrite(e.target.checked)} /> Overwrite if the file exists</label>
            <label className="check"><input type="checkbox" checked={acceptQuery} onChange={e => setAcceptQuery(e.target.checked)} /> Accept a changed query string on the same host and path (signed links)</label>
          </div>
          <div className="card-ft">
            <Button variant="primary" disabled={busy || running || !destination || Boolean(destError) || Boolean(shaError) || Boolean(large)} onClick={start}>{reapprove ? 'Approve new chain and download' : 'Download'}</Button>
            <span className="subtle-text">You confirm the exact chain above before anything is downloaded.</span>
          </div>
        </section>
      ) : null}
      {job ? <JobCard job={job} onCancel={() => source.cancel(job.id).catch((e: Error) => setError(e.message))} onRecheck={recheck} /> : null}
      <section className="card" aria-labelledby="dl-recent-title">
        <div className="card-hd"><h3 id="dl-recent-title">Recent downloads</h3><span className="end subtle-text">Every check and download is also in History</span></div>
        <ol className="timeline pad dl-recent" aria-label="Recent downloads">
          {recent.length ? recent.map(j => (
            <li key={j.id}><time dateTime={j.started ? new Date(j.started).toISOString() : undefined}>{stamp(j.started) || '—'}</time><div><strong>{j.state} · {j.destination}</strong><p className="subtle-text">{[j.hops[0]?.redacted, j.state === 'done' ? formatBytes(j.bytes) + ' · sha256 ' + j.sha256.slice(0, 16) + '…' : j.error, j.requestedBy].filter(Boolean).join(' · ')}</p></div></li>
          )) : <li className="timeline-empty"><p>No downloads since the controller started.</p></li>}
        </ol>
      </section>
    </div>
  );
}
