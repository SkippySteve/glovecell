// "Upload into sandbox": pick a file on this computer (for example one you
// downloaded with your own logged-in browser session) and send it into the
// sandbox. Data and actions come only from an UploadSource (../sources/uploads).
import {useCallback, useEffect, useRef, useState} from 'react';
import {Button, Callout, Tag} from '../../ui';
import {NotAvailable, SectionIntro} from '../bits';
import {useConfirm} from '../Confirm';
import {checkDestination, checkSha256, formatBytes, normalSha256} from '../sources/downloads';
import {defaultUploadDestination, sendFile, tooOld, uploadSummary, UPLOAD_TERMINAL, type UploadAvailability, type UploadJob, type UploadSource} from '../sources/uploads';
import {stamp} from '../time';

interface Live { job: UploadJob; sent: number; total: number }

function ResultCard({job, browserSha}: {job: UploadJob; browserSha: string}) {
  const done = job.state === 'done';
  const match = done && browserSha !== '' && browserSha === job.sha256;
  return (
    <section className={'card dl-job ' + job.state} aria-label="Upload result" data-upload={job.id} data-state={job.state}>
      <div className="card-hd">
        <h3>{done ? 'Uploaded' : job.state === 'cancelled' ? 'Upload cancelled' : 'Upload failed'}</h3>
        <span className="end"><Tag size="sm" variant={done ? (match || !browserSha ? 'success' : 'danger') : 'danger'} text={job.state} /></span>
      </div>
      <div className="card-bd">
        <p role="status">{uploadSummary(job, browserSha)}</p>
        {tooOld(job) ? <p className="warn-box">Image too old: this sandbox runs a workspace image from before uploads. Restart it on the current image (or wait for the next image promotion), then try again.</p> : null}
        {done ? (
          <dl className="dl-result">
            <dt>Saved to</dt><dd><code>{job.path || job.destination}</code></dd>
            <dt>Size</dt><dd>{formatBytes(job.written)} ({job.written.toLocaleString()} bytes)</dd>
            <dt>SHA-256 in the sandbox</dt><dd><code className="dl-sha" data-testid="upload-sandbox-sha">{job.sha256}</code>{job.expectedSha256 ? <Tag size="sm" variant="success" text="matches the expected hash" /> : null}</dd>
            {browserSha ? <><dt>SHA-256 your browser read</dt><dd><code className="dl-sha">{browserSha}</code><Tag size="sm" variant={match ? 'success' : 'danger'} text={match ? 'same file' : 'different: do not trust this copy'} /></dd></> : null}
          </dl>
        ) : null}
      </div>
    </section>
  );
}

export function Uploads({source, workspace}: {source: UploadSource; workspace: string}) {
  const ask = useConfirm();
  const [file, setFile] = useState<File | null>(null);
  const [destination, setDestination] = useState('');
  const [sha, setSha] = useState('');
  const [overwrite, setOverwrite] = useState(false);
  const [error, setError] = useState('');
  const [live, setLive] = useState<Live | null>(null);
  const [result, setResult] = useState<{job: UploadJob; browserSha: string} | null>(null);
  const [recent, setRecent] = useState<UploadJob[]>([]);
  const [dragging, setDragging] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const lastDefault = useRef('');
  const [avail, setAvail] = useState<UploadAvailability | null>(source.status ? null : {available: source.available, reason: source.reason, maxBytes: source.maxBytes});
  useEffect(() => {
    if (!source.status) return;
    let on = true;
    source.status().then(a => { if (on) setAvail(a); }, (e: Error) => { if (on) setAvail({available: false, reason: e.message, maxBytes: 0}); });
    return () => { on = false; };
  }, [source]);
  const maxBytes = avail?.maxBytes || source.maxBytes;
  const refreshRecent = useCallback(() => { if (avail?.available) source.list().then(setRecent, () => undefined); }, [source, avail?.available]);
  useEffect(refreshRecent, [refreshRecent]);
  // Leaving the page mid-upload stops sending; the sandbox then keeps nothing.
  useEffect(() => () => abort.current?.abort(), []);

  if (!avail) return <div className="stack"><SectionIntro title="Upload from this computer" /><p className="empty">Loading…</p></div>;
  if (!avail.available) return (
    <div className="stack">
      <SectionIntro title="Upload from this computer">Send a file from this computer into {workspace}.</SectionIntro>
      <NotAvailable what="Upload into sandbox" needs={avail.reason} />
    </div>
  );

  const choose = (f: File | null) => {
    setFile(f); setResult(null); setError('');
    if (f && (!destination || destination === lastDefault.current)) {
      const d = defaultUploadDestination(f.name);
      setDestination(d); lastDefault.current = d;
    }
  };
  const big = Boolean(file && maxBytes > 0 && file.size > maxBytes);
  const destError = destination ? checkDestination(destination) : null;
  const shaError = checkSha256(sha);
  const running = live !== null;

  const upload = async () => {
    if (!file) return;
    const ok = await ask('Upload into ' + workspace + '?', <>
      <p>Your browser sends <strong>{file.name}</strong> ({formatBytes(file.size)}) into the sandbox. Only the file's bytes leave this computer; no cookies or passwords for other sites are sent.</p>
      <p>Saved as <code>{destination}</code>{overwrite ? <strong> (replacing an existing file)</strong> : ' (refused if a file is already there)'}.{normalSha256(sha) ? ' The file is deleted unless its SHA-256 matches.' : ''}</p>
      <p className="subtle-text">Anything running in the sandbox can read the file once it is there.</p>
    </>, 'Upload');
    if (!ok) return;
    setError(''); setResult(null);
    const ctl = new AbortController();
    abort.current = ctl;
    let job: UploadJob | null = null;
    try {
      job = await source.start({name: file.name, destination, size: file.size, sha256: normalSha256(sha), overwrite});
      setLive({job, sent: 0, total: file.size});
      const r = await sendFile(source, file, job, {signal: ctl.signal, onProgress: p => setLive({job: p.job, sent: p.sent, total: p.total})});
      setResult({job: r.job, browserSha: r.browserSha256});
    } catch (e) {
      const msg = (e as Error).message;
      if (job) {
        if (ctl.signal.aborted) await source.cancel(job.id).catch(() => undefined);
        // The backend's record says what happened (for example the sandbox refused the file).
        const latest = await source.get(job.id).catch(() => null);
        if (latest && UPLOAD_TERMINAL.includes(latest.state)) setResult({job: latest, browserSha: ''});
        else setError(msg);
      } else setError(tooOld(null, msg) ? 'Image too old: ' + msg : msg);
    } finally {
      abort.current = null;
      setLive(null);
      refreshRecent();
    }
  };

  const pct = live && live.total > 0 ? Math.min(100, Math.round(live.sent / live.total * 100)) : live ? 100 : undefined;
  return (
    <div className="stack" data-testid="uploads">
      <SectionIntro title="Upload from this computer" aside={`at most ${formatBytes(maxBytes)}`}>
        Send a file from this computer into {workspace}, for example one you downloaded with your own login. Your browser reads the file and sends only its bytes; your sign-ins and cookies for other sites never leave the browser.
      </SectionIntro>
      {error ? <Callout variant="danger" description={error} /> : null}
      <section className="card" aria-labelledby="up-file-title">
        <div className="card-hd"><h3 id="up-file-title">1. Choose a file</h3></div>
        <div className={'card-bd up-drop' + (dragging ? ' dragging' : '')} data-testid="upload-drop"
          onDragOver={e => { e.preventDefault(); setDragging(true); }} onDragLeave={() => setDragging(false)}
          onDrop={e => { e.preventDefault(); setDragging(false); if (!running && e.dataTransfer.files.length) choose(e.dataTransfer.files[0]); }}>
          <div className="field">
            <label className="field-label" htmlFor="up-file">File</label>
            <input id="up-file" type="file" disabled={running} onChange={e => choose(e.target.files?.[0] || null)} aria-describedby="up-file-help" />
            <small id="up-file-help" className={big ? 'error-text' : 'subtle-text'}>{big && file ? `${file.name} is ${formatBytes(file.size)}; the limit is ${formatBytes(maxBytes)}.` : file ? `${file.name} · ${formatBytes(file.size)}` : 'Or drop a file here.'}</small>
          </div>
        </div>
      </section>
      {file && !big ? (
        <section className="card" aria-labelledby="up-save-title">
          <div className="card-hd"><h3 id="up-save-title">2. Save into the sandbox</h3></div>
          <div className="card-bd">
            <div className="field">
              <label className="field-label" htmlFor="up-dest">Save as</label>
              <input id="up-dest" className="text-input wide" autoComplete="off" spellCheck={false} value={destination} disabled={running} onChange={e => setDestination(e.target.value)} aria-invalid={Boolean(destError) || undefined} aria-describedby="up-dest-help" />
              <small id="up-dest-help" className={destError ? 'error-text' : 'subtle-text'}>{destError || 'Under ~/ (the sandbox home) or /workspace/. Folders are created; symlinks are refused.'}</small>
            </div>
            <div className="field">
              <label className="field-label" htmlFor="up-sha">Expected SHA-256 (optional)</label>
              <input id="up-sha" className="text-input wide mono" autoComplete="off" spellCheck={false} placeholder="64 hex characters" value={sha} disabled={running} onChange={e => setSha(e.target.value)} aria-invalid={Boolean(shaError) || undefined} aria-describedby="up-sha-help" />
              <small id="up-sha-help" className={shaError ? 'error-text' : 'subtle-text'}>{shaError || 'If the sandbox computes a different hash, the file is deleted. Your browser also hashes what it sends, for comparison.'}</small>
            </div>
            <label className="check"><input type="checkbox" checked={overwrite} disabled={running} onChange={e => setOverwrite(e.target.checked)} /> Overwrite if the file exists</label>
          </div>
          <div className="card-ft">
            <Button variant="primary" disabled={running || !destination || Boolean(destError) || Boolean(shaError)} onClick={upload}>Upload</Button>
            <span className="subtle-text">You confirm before anything is sent.</span>
          </div>
        </section>
      ) : null}
      {live ? (
        <section className="card dl-job" aria-label="Upload" data-upload={live.job.id} data-state={live.job.state}>
          <div className="card-hd"><h3>Uploading into the sandbox</h3></div>
          <div className="card-bd">
            <p role="status">{live.sent < live.total ? `Sent ${formatBytes(live.sent)} of ${formatBytes(live.total)}` : 'The sandbox is checking and saving the file…'}</p>
            <progress className="dl-progress" max={100} value={pct} aria-label="Upload progress">{pct === undefined ? '' : pct + '%'}</progress>
          </div>
          <div className="card-ft"><Button variant="secondary" onClick={() => abort.current?.abort()}>Cancel upload</Button><span className="subtle-text end">{live.job.destination}</span></div>
        </section>
      ) : null}
      {result ? <ResultCard job={result.job} browserSha={result.browserSha} /> : null}
      <section className="card" aria-labelledby="up-recent-title">
        <div className="card-hd"><h3 id="up-recent-title">Recent uploads</h3><span className="end subtle-text">Every upload is also in History</span></div>
        <ol className="timeline pad dl-recent" aria-label="Recent uploads">
          {recent.length ? recent.map(u => (
            <li key={u.id}><time dateTime={u.started ? new Date(u.started).toISOString() : undefined}>{stamp(u.started) || '—'}</time><div><strong>{u.state} · {u.destination}</strong><p className="subtle-text">{[u.name, u.state === 'done' ? formatBytes(u.written) + ' · sha256 ' + u.sha256.slice(0, 16) + '…' : u.error, u.requestedBy].filter(Boolean).join(' · ')}</p></div></li>
          )) : <li className="timeline-empty"><p>No uploads since the controller started.</p></li>}
        </ol>
      </section>
    </div>
  );
}
