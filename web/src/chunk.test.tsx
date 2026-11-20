// @vitest-environment jsdom
import {byRole, render} from './test/dom';
import {act, Suspense} from 'react';
import {afterEach, describe, expect, it, vi} from 'vitest';
import {ChunkBoundary, claimReload, lazyChunk, RELOAD_KEY, RELOAD_WINDOW_MS, reloadOnFailure, type ReloadEnv} from './chunk';

// One browser tab: its sessionStorage survives reloads, and the clock moves on.
interface Tab { clock: number; reloads: number; env: ReloadEnv; stored: Map<string, string> }
function tab(start = 1_000_000): Tab {
  const stored = new Map<string, string>();
  const t: Tab = {
    clock: start,
    reloads: 0,
    env: {
      storage: () => ({getItem: (k: string) => stored.get(k) ?? null, setItem: (k: string, v: string) => { stored.set(k, v); }}),
      reload: () => { t.reloads++; },
      now: () => t.clock,
    },
    stored,
  };
  return t;
}

// What a 404 on an old chunk looks like to the page.
const gone = () => Promise.reject(new TypeError('Failed to fetch dynamically imported module: /assets/Palette-old.js'));
const settled = async (p: Promise<unknown>) => {
  let done = false;
  p.then(() => { done = true; }, () => { done = true; });
  for (let i = 0; i < 5; i++) await Promise.resolve();
  return done;
};

describe('reloadOnFailure', () => {
  it('reloads the page once when the import rejects, and leaves the import pending', async () => {
    const t = tab();
    const load = reloadOnFailure(gone, t.env);
    const p = load();
    expect(await settled(p)).toBe(false);
    expect(t.reloads).toBe(1);
    expect(t.stored.get(RELOAD_KEY)).toBe(String(t.clock));
  });

  it('does not reload in a loop when the chunk is still missing after the reload', async () => {
    const t = tab();
    const load = reloadOnFailure(gone, t.env);
    void load();
    expect(t.reloads).toBe(0); // the rejection is handled on a later microtask
    await settled(Promise.resolve());
    expect(t.reloads).toBe(1);
    // The page came back and asked again, several times, within the window.
    for (let i = 0; i < 5; i++) {
      t.clock += 1_000;
      await expect(reloadOnFailure(gone, t.env)()).rejects.toThrow(/dynamically imported module/);
    }
    expect(t.reloads).toBe(1);
  });

  it('allows another automatic reload once the window has passed', async () => {
    const t = tab();
    void reloadOnFailure(gone, t.env)();
    await settled(Promise.resolve());
    t.clock += RELOAD_WINDOW_MS;
    void reloadOnFailure(gone, t.env)();
    await settled(Promise.resolve());
    expect(t.reloads).toBe(2);
  });

  it('never reloads without sessionStorage, since nothing could stop a loop', async () => {
    const t = tab();
    t.env.storage = () => { throw new DOMException('denied', 'SecurityError'); };
    await expect(reloadOnFailure(gone, t.env)()).rejects.toThrow(/dynamically imported module/);
    expect(t.reloads).toBe(0);
  });

  it('passes a successful import through without touching the guard', async () => {
    const t = tab();
    await expect(reloadOnFailure(() => Promise.resolve('module'), t.env)()).resolves.toBe('module');
    expect(t.reloads).toBe(0);
    expect(t.stored.size).toBe(0);
  });

  it('does not let a stored time in the future (clock moved back) block reloads for good', () => {
    const t = tab();
    t.stored.set(RELOAD_KEY, String(t.clock + 3_600_000));
    expect(claimReload(t.env)).toBe(true);
    expect(claimReload(t.env)).toBe(false); // and the new mark guards the next one
  });
});

describe('lazyChunk in a ChunkBoundary', () => {
  afterEach(() => vi.restoreAllMocks());

  async function settle() {
    for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
  }
  function page(t: Tab) {
    const Gone = lazyChunk<(props: {label: string}) => null>(gone, t.env);
    return render(<><p id="rest">Rest of the console</p><ChunkBoundary what="command palette"><Suspense fallback={<p id="loading">Loading…</p>}><Gone label="x" /></Suspense></ChunkBoundary></>);
  }

  it('keeps the loading state while the page reloads', async () => {
    const t = tab();
    page(t);
    await settle();
    expect(t.reloads).toBe(1);
    expect(document.getElementById('loading')).toBeTruthy();
    expect(byRole('alert')).toHaveLength(0);
  });

  it('shows a notice instead of a blank console when the reload did not help', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const t = tab();
    t.stored.set(RELOAD_KEY, String(t.clock - 2_000)); // reloaded two seconds ago
    page(t);
    await settle();
    expect(t.reloads).toBe(0);
    const [alert] = byRole('alert');
    expect(alert?.textContent).toMatch(/command palette could not be loaded/);
    expect(alert?.querySelector('button')?.textContent).toBe('Reload the page');
    expect(document.getElementById('rest')).toBeTruthy();
  });
});
