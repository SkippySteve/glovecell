import {lazy, StrictMode, Suspense} from 'react';
import {createRoot} from 'react-dom/client';
import '@openuidev/react-ui/components.css';
import './theme.css';
import './base.css';
import './console.css';
import './factory/factory.css';
import './workspace/workspace.css';
import './shell/shell.css';
import {DataProvider, useData} from './data';
import {Home} from './Home';
import {Attention} from './attention/Attention';
import {NewSandbox} from './NewSandbox';
import {OrcaProjects} from './OrcaProjects';
import {ConfirmProvider} from './workspace/Confirm';
import {ProjectWorkspace, SandboxWorkspace} from './workspace/Workspace';
import {useRoute} from './router';
import {AppShell} from './shell/Shell';
import {ShellProvider} from './shell/state';
import {Callout} from './ui';

// Reports pull in the OpenUI Lang parser and renderer, so they load on first use.
const RunList = lazy(() => import('./factory/Reports').then(m => ({default: m.RunList})));
const RunReport = lazy(() => import('./factory/Reports').then(m => ({default: m.RunReport})));

function Toasts() {
  const {toasts} = useData();
  return <div className="toasts" role="status" aria-live="polite">{toasts.map(t => <Callout key={t.id} variant={t.error ? 'danger' : 'success'} description={t.text} />)}</div>;
}

function App() {
  const route = useRoute();
  const workspace = route.view === 'project' || route.view === 'sandbox';
  return (
    <>
      <AppShell route={route} workspace={workspace}>
        {route.view === 'new' ? <NewSandbox />
          : route.view === 'orca' ? <OrcaProjects />
          : route.view === 'attention' ? <Attention />
          : route.view === 'project' ? <ProjectWorkspace name={route.name} section={route.section} />
          : route.view === 'sandbox' ? <SandboxWorkspace id={route.id} section={route.section} />
          : route.view === 'runs' || route.view === 'run' ? <Suspense fallback={<p className="empty">Loading…</p>}>{route.view === 'run' ? <RunReport id={route.id} /> : <RunList />}</Suspense>
          : <Home scope={route.view === 'sandboxes' ? 'sandbox' : route.view === 'projects' ? 'project' : 'all'} />}
      </AppShell>
      <Toasts />
    </>
  );
}

createRoot(document.getElementById('root')!).render(<StrictMode><DataProvider><ShellProvider><ConfirmProvider><App /></ConfirmProvider></ShellProvider></DataProvider></StrictMode>);
