import {StrictMode, useEffect} from 'react';
import {createRoot} from 'react-dom/client';
import '@openuidev/react-ui/components.css';
import './theme.css';
import './console.css';
import {DataProvider, useData} from './data';
import {Home} from './Home';
import {NewSandbox} from './NewSandbox';
import {Project} from './Project';
import {Sandbox} from './sandbox/Sandbox';
import {useRoute} from './router';
import {Button, Callout} from './ui';

function Bar() {
  const {session, toast} = useData();
  const logout = async () => {
    const response = await fetch('/auth/logout', {method: 'POST', headers: {'Content-Type': 'application/json'}, credentials: 'same-origin'});
    if (response.ok) location.assign('/auth/signed-out'); else toast('Sign-out failed; try again.', true);
  };
  return (
    <header className="console-bar">
      <a className="console-brand" href="/"><span className="console-logo" aria-hidden="true" />Sandboxes</a>
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
      <main id="view" className="page" tabIndex={-1}>
        {route.view === 'new' ? <NewSandbox /> : route.view === 'project' ? <Project name={route.name} tab={route.tab} /> : route.view === 'sandbox' ? <Sandbox id={route.id} tab={route.tab} /> : <Home />}
      </main>
      <Toasts />
    </>
  );
}

createRoot(document.getElementById('root')!).render(<StrictMode><DataProvider><App /></DataProvider></StrictMode>);
