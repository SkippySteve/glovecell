import {useEffect, useState} from 'react';
import {groupReference, referenceRules, type ReferenceList} from './reference';
import {Card, CardHeader, Input, Tag} from './ui';

// Read-only list of the deployment's reference rules, shown with every
// project's own rules. They change only in Git and apply without a pause.
export function ReferenceRules() {
  const [list, setList] = useState<ReferenceList | null>(null);
  const [filter, setFilter] = useState('');
  useEffect(() => {
    let live = true;
    referenceRules().then(next => { if (live) setList(next); }).catch(() => { if (live) setList({digest: '', rules: []}); });
    return () => { live = false; };
  }, []);
  if (!list || !list.rules.length) return null;
  const groups = groupReference(list.rules, filter);
  const projects = groupReference(list.rules, '').length;
  return (
    <Card variant="sunk" width="full">
      <CardHeader title={'From Git: reference sources (' + projects + ' projects)'}
        subtitle="Read-only access to the source and official documentation of the open-source projects this deployment uses, for every project. GET and HEAD only (Git fetch may POST to git-upload-pack); never push. Permanent, managed in Git, applied without pausing." />
      <div className="field">
        <label className="field-label" htmlFor="reference-filter">Filter by project, host or path</label>
        <Input id="reference-filter" value={filter} placeholder="cilium" autoCapitalize="off" spellCheck={false} onChange={event => setFilter(event.target.value)} />
      </div>
      <ul className="reference-rules">
        {groups.map(g => (
          <li key={g.source}>
            <details>
              <summary><strong>{g.project}</strong> <Tag size="sm" variant="info" text="from Git" /> <span className="subtle">{g.rules.length} rule{g.rules.length === 1 ? '' : 's'} · {g.source}</span></summary>
              <ul>{g.rules.map((r, i) => <li key={i}><code>{r.methods.join(', ')} {r.host} {r.paths.join(' ')}</code></li>)}</ul>
            </details>
          </li>
        ))}
        {groups.length ? null : <li className="subtle">No reference project matches.</li>}
      </ul>
      {list.digest ? <p className="subtle">List {list.digest}</p> : null}
    </Card>
  );
}
