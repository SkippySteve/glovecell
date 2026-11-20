// Lazy chunks that survive a controller upgrade. Each build names its chunks by
// content hash, and the server only has the current build's files. A tab opened
// before an upgrade asks for the old Palette-<hash>.js, gets a 404 and the
// import() rejects; React's lazy() would then throw on every render and, with
// nothing to catch it, unmount the whole console.
//
// lazyChunk reloads the page once instead, which fetches the new index.html and
// its chunk names. A sessionStorage timestamp stops a reload loop: within
// RELOAD_WINDOW_MS of the last automatic reload the error goes on to the
// ChunkBoundary around the lazy component, which offers a manual reload.
import {Component, lazy, type ComponentType, type ReactNode} from 'react';
import {Button, Callout} from './ui';

export const RELOAD_KEY = 'console-chunk-reload';
export const RELOAD_WINDOW_MS = 60_000;

export interface ReloadEnv {
  storage: () => Pick<Storage, 'getItem' | 'setItem'>;
  reload: () => void;
  now: () => number;
}

const browser: ReloadEnv = {
  storage: () => window.sessionStorage,
  reload: () => window.location.reload(),
  now: () => Date.now(),
};

// True when this failure may reload the page: no automatic reload in this tab
// within the window. Without sessionStorage there is no guard, so no reload.
export function claimReload(env: ReloadEnv = browser): boolean {
  try {
    const store = env.storage();
    const last = Number(store.getItem(RELOAD_KEY));
    const now = env.now();
    if (last && now - last >= 0 && now - last < RELOAD_WINDOW_MS) return false;
    store.setItem(RELOAD_KEY, String(now));
    return true;
  } catch {
    return false;
  }
}

// Wraps a dynamic import: on failure, reload once (the returned promise then
// never settles, so Suspense keeps its fallback until the page goes), or
// reject with the original error for the ChunkBoundary.
export function reloadOnFailure<T>(load: () => Promise<T>, env: ReloadEnv = browser): () => Promise<T> {
  return () => load().catch((err: unknown) => {
    if (!claimReload(env)) throw err;
    env.reload();
    return new Promise<T>(() => {});
  });
}

// React's lazy(), with reloadOnFailure around the import (and lazy()'s own type bound).
export function lazyChunk<T extends ComponentType<any>>(load: () => Promise<{default: T}>, env?: ReloadEnv) {
  return lazy(reloadOnFailure(load, env));
}

// Catches a lazy chunk that failed to load (after the one automatic reload), so
// the rest of the console stays usable. `floating` places the notice like a
// toast, for a component that has no place in the page (the command palette).
export class ChunkBoundary extends Component<{children: ReactNode; what: string; floating?: boolean}, {failed: boolean}> {
  state = {failed: false};
  static getDerivedStateFromError() { return {failed: true}; }
  componentDidCatch(err: unknown) { console.error(`console: ${this.props.what} failed to load`, err); }
  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div role="alert" className={this.props.floating ? 'chunk-failed is-floating' : 'chunk-failed'}>
        <Callout variant="danger" description={`The ${this.props.what} could not be loaded. The console may have been upgraded since this page was opened.`} />
        <Button variant="secondary" onClick={() => browser.reload()}>Reload the page</Button>
      </div>
    );
  }
}
