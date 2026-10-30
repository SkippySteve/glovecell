// Placeholder for #/attention until the "Needs attention" inbox lands. It lists
// only what the console already knows: requests waiting per sandbox and
// projects whose preparation is blocked. Replace the import in main.tsx with
// the inbox component; nothing else depends on this file.
import {useEffect} from 'react';
import {egressControllable} from '../api';
import {useData} from '../data';
import {projectHref, sandboxHref} from '../router';
import {projectState} from '../state';
import {Card} from '../ui';

export function AttentionPlaceholder() {
  const {loaded, existing, projects} = useData();
  useEffect(() => { document.title = 'Needs attention · Sandboxes'; }, []);
  const waiting = existing.filter(s => (s.pending || 0) + (s.approvals || 0) > 0);
  const blocked = projects.filter(p => p.status?.phase !== 'Removed' && projectState(p).attention);
  return (
    <>
      <div className="page-head"><div><h1>Needs attention</h1><p className="muted">Requests waiting on you and sandboxes that cannot continue on their own.</p></div></div>
      {!loaded ? <p className="empty">Loading…</p> : waiting.length || blocked.length ? (
        <Card variant="sunk" width="full">
          <ul className="items">
            {waiting.map(s => {
              const n = (s.pending || 0) + (s.approvals || 0);
              return <li key={'s:' + s.id}><div className="item-main"><strong>{s.id}</strong><span className="subtle-text">{n} request{n === 1 ? '' : 's'} waiting on you</span></div>
                <a className="link-button" href={sandboxHref(s.id, egressControllable(s) ? 'internet' : 'overview')}>Review<span className="visually-hidden"> {s.id}</span></a></li>;
            })}
            {blocked.map(p => <li key={'p:' + p.metadata.name}><div className="item-main"><strong>{p.metadata.name}</strong><span className="subtle-text">{projectState(p).attention}</span></div>
              <a className="link-button" href={projectHref(p.metadata.name)}>Open<span className="visually-hidden"> {p.metadata.name}</span></a></li>)}
          </ul>
        </Card>
      ) : <Card variant="sunk" width="full" className="empty-card"><h2>Nothing needs you</h2><p className="muted">Waiting requests and blocked sandboxes appear here.</p></Card>}
    </>
  );
}
