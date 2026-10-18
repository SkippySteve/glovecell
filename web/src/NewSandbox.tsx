import {useEffect, useState, type FormEvent} from 'react';
import {api} from './api';
import {useData} from './data';
import {navigate, projectHref} from './router';
import {newRule, RuleList, rulesError, toRule, type RuleDraft} from './Rules';
import {slugFromRepository} from './state';
import {Button, Callout, Card, CardHeader, CheckBoxItem, Input} from './ui';

const REGISTRIES: [string, string, string][] = [['go', 'Go', 'Modules'], ['python', 'Python', 'PyPI'], ['rust', 'Rust', 'crates.io'], ['npm', 'npm', 'Packages']];
const NAME = /^[a-z][a-z0-9-]{0,39}$/;

export function NewSandbox() {
  const {repositories, reload, toast} = useData();
  const [repository, setRepository] = useState('');
  const [name, setName] = useState('');
  const [automatic, setAutomatic] = useState(true);
  const [registries, setRegistries] = useState<string[]>([]);
  const [rules, setRules] = useState<RuleDraft[]>([]);
  const [saving, setSaving] = useState(false);
  useEffect(() => { document.title = 'New sandbox · Sandboxes'; }, []);
  useEffect(() => { if (!repository && repositories?.length) setRepository(repositories[0].full_name); }, [repositories, repository]);
  useEffect(() => { if (automatic && NAME.test(slugFromRepository(repository))) setName(slugFromRepository(repository)); }, [automatic, repository]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const problem = rulesError(rules);
    if (problem) { toast(problem, true); return; }
    setSaving(true);
    try {
      await api('workspaces', 'POST', {name, repository, registries, https: rules.map(toRule)});
      toast(name + ': sandbox requested. Preparation status appears here.');
      await reload(); navigate(projectHref(name));
    } catch (error) { toast((error as Error).message, true); setSaving(false); }
  };
  const available = Boolean(repositories?.length);
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb"><a href="#/">Sandboxes</a><span aria-hidden="true">/</span><span>New</span></nav>
      <div className="page-head"><div><h1>New sandbox</h1><p className="muted">Pick a repository and decide what the sandbox can reach.</p></div></div>
      <form id="create" onSubmit={submit}>
        <Card variant="sunk" width="full">
          <section className="form-section">
            <CardHeader title="Repository" subtitle="The sandbox checks this repository out and gets project-scoped Git and pull request access." />
            <div className="grid-2">
              <div className="field"><label className="field-label" htmlFor="repositories">Repository</label>
                <select id="repositories" className="select" required disabled={!available} value={repository} onChange={event => setRepository(event.target.value)}>
                  {repositories === null ? <option value="">Loading repositories…</option> : available ? repositories.map(r => <option key={r.full_name} value={r.full_name}>{r.full_name}</option>) : <option value="">No repositories available</option>}
                </select></div>
              <div className="field"><label className="field-label" htmlFor="name">Sandbox name</label>
                <Input id="name" required pattern="[a-z][a-z0-9-]{0,39}" maxLength={40} placeholder="my-project" autoCapitalize="off" spellCheck={false} aria-describedby="name-help" value={name} onChange={event => { setAutomatic(false); setName(event.target.value); }} />
                <small id="name-help" className="inline-hint"><span className="subtle">Lowercase letters, numbers and dashes.</span>{automatic ? null : <Button type="button" variant="tertiary" size="extra-small" onClick={() => setAutomatic(true)}>Use repository name</Button>}</small></div>
            </div>
          </section>
          <section className="form-section">
            <CardHeader title="Package registries" subtitle="Allow package downloads (GET and HEAD) from reviewed registry hosts." />
            <div className="chips" role="group" aria-label="Package registries">
              {REGISTRIES.map(([value, label, detail]) => <CheckBoxItem key={value} label={label} description={detail} checked={registries.includes(value)} onChange={checked => setRegistries(checked ? [...registries, value] : registries.filter(r => r !== value))} />)}
            </div>
          </section>
          <section className="form-section">
            <CardHeader title="Outbound access" subtitle="Add HTTPS destinations your tools need. A path of / allows the whole host." />
            <Callout variant="neutral" description="Private networks, localhost and cloud metadata are always blocked. Approved gateways provide scoped access to private services." />
            <RuleList rules={rules} onChange={setRules} empty="No additional destinations. Requests beyond repository and registry access are denied." />
            <div><Button type="button" variant="secondary" size="small" onClick={() => setRules([...rules, newRule()])}>+ Add destination</Button></div>
          </section>
          <div className="card-foot">
            <span className="subtle">Access can be changed later while the sandbox is paused.</span>
            <div className="actions"><Button type="button" variant="tertiary" onClick={() => navigate('#/')}>Cancel</Button><Button type="submit" variant="primary" disabled={!available || saving}>Create sandbox</Button></div>
          </div>
        </Card>
      </form>
    </>
  );
}
