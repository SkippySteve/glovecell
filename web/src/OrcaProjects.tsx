import {useCallback, useEffect, useRef, useState, type FormEvent} from 'react';
import {orcaProjects, publishOrcaProject, type OrcaProject} from './api';
import {useData} from './data';
import {orcaRuntimeId, POLL_LIMIT, POLL_MS, preparing, projectCounts, projectStatus, REPO} from './orca';
import {sandboxHref} from './router';
import {Button, Callout, Card, CardHeader, Input, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Tag} from './ui';
import './workspace/previews.css';

// Publishing clones a repository into the Orca sandbox and registers it there.
// Every Orca client paired with that sandbox lists it; nothing else changes.
export function OrcaProjects() {
  const {session, repositories, existing, toast} = useData();
  const [repo, setRepo] = useState('');
  const [records, setRecords] = useState<OrcaProject[] | null>(null);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const polls = useRef(0);
  useEffect(() => { document.title = 'Orca projects · Sandboxes'; }, []);

  const load = useCallback(async () => {
    try { setRecords(await orcaProjects()); setError(''); } catch (e) { setError((e as Error).message); }
  }, []);
  useEffect(() => { if (session.orcaProjects) load(); }, [load, session.orcaProjects]);
  // Refresh while a publish is in progress, for a bounded time.
  useEffect(() => {
    if (!records || !preparing(records) || polls.current >= POLL_LIMIT) return;
    const timer = window.setTimeout(() => { polls.current++; load(); }, POLL_MS);
    return () => window.clearTimeout(timer);
  }, [records, load]);

  if (!session.orcaProjects) return <><h1>Orca projects</h1><p className="muted">This console has no Orca sandbox intake configured.</p></>;
  const valid = REPO.test(repo.trim());
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!valid) { toast('Enter a repository as OWNER/NAME.', true); return; }
    setSaving(true);
    try {
      const record = await publishOrcaProject(repo.trim());
      toast(record.repo + ': publishing to the Orca sandbox.');
      polls.current = 0; setRepo(''); await load();
    } catch (e) { toast((e as Error).message, true); }
    finally { setSaving(false); }
  };
  const runtime = orcaRuntimeId(session, existing);
  const counts = records ? projectCounts(records) : null;
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb"><a href="#/">Sandboxes</a><span aria-hidden="true">/</span><span>Orca projects</span></nav>
      <div className="page-head"><div><h1>Orca projects</h1><p className="muted">Publish a repository to the Orca sandbox. It appears in every Orca app paired with that sandbox, with no further steps. Each project is a repository inside that one sandbox.</p></div></div>

      <section className="set-group" aria-labelledby="orca-publish">
        <div className="set-group-head">
          <h2 id="orca-publish">Publish</h2>
          <p className="muted">Add a repository to Orca, or refresh a checkout that is already there.</p>
        </div>
        <form onSubmit={submit}>
          <Card variant="sunk" width="full">
            <section className="form-section">
              <CardHeader title="Publish a repository" subtitle="The sandbox clones it with its own Forgejo access, then registers it as a project. No agent starts." />
              <Callout variant="warning" description="Every phone and desktop paired with the Orca sandbox sees published projects and has full access to that sandbox: terminals, files and agents. Projects there share one trust boundary." />
              <div className="field"><label className="field-label" htmlFor="orca-repo">Repository</label>
                <Input id="orca-repo" list="orca-repositories" required placeholder="owner/repository" autoCapitalize="off" spellCheck={false} aria-describedby="orca-repo-help" value={repo} onChange={event => setRepo(event.target.value)} />
                <datalist id="orca-repositories">{(repositories || []).map(r => <option key={r.full_name} value={r.full_name} />)}</datalist>
                <small id="orca-repo-help" className="subtle">The sandbox's Forgejo account must already have access to it.</small></div>
            </section>
            <div className="card-foot"><span className="subtle">Publishing again refreshes the sandbox's checkout.</span>
              <div className="actions"><Button type="submit" variant="primary" disabled={!valid || saving}>{saving ? 'Publishing…' : 'Publish to Orca'}</Button></div></div>
          </Card>
        </form>
      </section>

      <section className="set-group" aria-labelledby="orca-list">
        <div className="set-group-head">
          <h2 id="orca-list">Projects in Orca</h2>
          <p className="muted">{counts ? `${counts.total} project${counts.total === 1 ? '' : 's'}` + (counts.published ? ` · ${counts.published} published from this console` : '') + (counts.registered ? ` · ${counts.registered} added in Orca` : '') + (counts.preparing ? ` · ${counts.preparing} preparing` : '') + (counts.failed ? ` · ${counts.failed} failed` : '') : 'Repositories this console published and every other repository the Orca sandbox holds.'}</p>
        </div>
        {error ? <Callout variant="danger" title="Project records unavailable" description={error} /> : null}
        {records === null ? (error ? null : <p className="empty">Loading projects…</p>) : records.length ? (
          <Table aria-label="Orca projects" containerClassName="sandbox-table">
            <TableHeader><TableRow><TableHead>Project</TableHead><TableHead>Status</TableHead><TableHead>Requested</TableHead></TableRow></TableHeader>
            <TableBody>{records.map(p => {
              const state = projectStatus(p);
              return (
                <TableRow key={p.name} data-orca-project={p.name}>
                  <TableCell data-label="Project"><span className="row-title">{p.repo || p.name}</span><span className="row-sub">{p.clone || p.name}</span></TableCell>
                  <TableCell data-label="Status"><Tag size="sm" variant={state.variant} text={state.label} className={state.busy ? 'busy' : undefined} />{p.error ? <span className="row-sub">{p.error}</span> : null}</TableCell>
                  <TableCell data-label="Requested">{p.requestedAt ? <><span>{new Date(p.requestedAt).toLocaleString()}</span><span className="row-sub">{p.actor}</span></> : <span className="subtle">Added in Orca</span>}</TableCell>
                </TableRow>);
            })}</TableBody>
          </Table>
        ) : <p className="empty">No projects in Orca yet.</p>}
      </section>

      <section className="set-group" aria-labelledby="orca-about">
        <div className="set-group-head">
          <h2 id="orca-about">How it works</h2>
        </div>
        <ul className="orca-facts">
          <li>Publishing clones the repository inside the Orca sandbox and registers it there. Nothing else changes and no agent starts.</li>
          <li>Orca shows the project on every phone and desktop paired with that sandbox. Use a separate sandbox and pairing when projects need separate trust boundaries.</li>
          <li>{runtime ? <>The Orca sandbox is <a href={sandboxHref(runtime)}>{runtime}</a>. Its internet, LAN and package settings apply to every project in it.</> : 'This console could not tell which sandbox runs Orca, so no sandbox is linked here.'}</li>
        </ul>
      </section>
    </>
  );
}
