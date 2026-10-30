import {lazy, StrictMode, Suspense, useEffect} from 'react';
import {createRoot} from 'react-dom/client';
import '@openuidev/react-ui/components.css';
import './theme.css';
import './console.css';
import './factory/factory.css';
import './workspace/workspace.css';
import {DataProvider, useData} from './data';
import {runHref} from './factory/href';
import {Home} from './Home';
import {Attention} from './attention/Attention';
import {NewSandbox} from './NewSandbox';
import {OrcaProjects} from './OrcaProjects';
import {ConfirmProvider} from './workspace/Confirm';
import {ProjectWorkspace, SandboxWorkspace} from './workspace/Workspace';
import {orcaHref, useRoute} from './router';
import {Button, Callout} from './ui';

// Reports pull in the OpenUI Lang parser and renderer, so they load on first use.
const RunList = lazy(() => import('./factory/Reports').then(m => ({default: m.RunList})));
const RunReport = lazy(() => import('./factory/Reports').then(m => ({default: m.RunReport})));

function Bar() {
  const {session, toast} = useData();
  const logout = async () => {
    const response = await fetch('/auth/logout', {method: 'POST', headers: {'Content-Type': 'application/json'}, credentials: 'same-origin'});
    if (response.ok) location.assign('/auth/signed-out'); else toast('Sign-out failed; try again.', true);
  };
  return (
    <header className="console-bar">
      <a className="console-brand" href="/"><span className="console-logo" aria-hidden="true" />Sandboxes</a>
      {session.runRecords ? <a className="console-nav-link" href={runHref()}>Runs</a> : null}
      {session.orcaProjects ? <a className="console-nav-link" href={orcaHref()}>Orca projects</a> : null}
      <span className="console-actor subtle">{session.actor || ''}</span>
      {session.oidc ? <Button variant="tertiary" size="small" onClick={logout}>Sign out</Button> : null}
    </header>
  );
}

function Toasts() {
  const {toasts} = useData();
  return <div className="toasts" role="status" aria-live="polite">{toasts.map(t => <Callout key={t.id} variant={t.error ? 'danger' : 'success'} description={t.text} />)}</div>;
}

function App() {
  const route = useRoute();
  useEffect(() => { if (route.view === 'home') document.title = 'Sandboxes'; }, [route]);
  return (
    <>
      <Bar />
      <main id="view" className={route.view === 'project' || route.view === 'sandbox' ? 'page workspace-page' : 'page'} tabIndex={-1}>
        {route.view === 'new' ? <NewSandbox /> : route.view === 'attention' ? <Attention /> : route.view === 'orca' ? <OrcaProjects /> : route.view === 'project' ? <ProjectWorkspace name={route.name} section={route.section} /> : route.view === 'sandbox' ? <SandboxWorkspace id={route.id} section={route.section} />
          : route.view === 'runs' || route.view === 'run' ? <Suspense fallback={<p className="empty">Loading…</p>}>{route.view === 'run' ? <RunReport id={route.id} /> : <RunList />}</Suspense> : <Home />}
      </main>
      <Toasts />
    </>
  );
}

createRoot(document.getElementById('root')!).render(<StrictMode><DataProvider><ConfirmProvider><App /></ConfirmProvider></DataProvider></StrictMode>);
