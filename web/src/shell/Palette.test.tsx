// @vitest-environment jsdom
import {accessibleName, byRole, click, flush, press, render, type} from '../test/dom';
import {act} from 'react';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {openPalette, PaletteLauncher} from './launcher';

vi.mock('../data', () => ({
  useData: () => ({
    existing: [{id: 'orca-pilot', namespace: 'agent-workspace-orca-pilot'}, {id: 'coding-a', namespace: 'agent-workspace-coding-a'}],
    projects: [{metadata: {name: 'demo-project'}, spec: {repository: 'example/demo-project'}, status: {phase: 'Ready'}}],
    session: {runRecords: true},
  }),
}));
vi.mock('../access/standing', () => ({useStandingAccess: () => ({rules: {}, profiles: undefined})}));

const dialog = () => byRole('dialog')[0];
const input = () => document.querySelector<HTMLInputElement>('[role="combobox"]')!;
const options = () => byRole('option');
const highlighted = () => options().filter(o => o.hasAttribute('data-highlighted'));
const activeOption = () => document.getElementById(input().getAttribute('aria-activedescendant') ?? '');

// The first request also loads the palette's chunk (launcher.tsx); later ones reuse it.
// Wait on the dialog itself, not a fixed number of turns: on a busy CI pod the
// chunk can take longer to load than 50 flushes.
async function opened() {
  const deadline = Date.now() + 20_000;
  while (!dialog() && Date.now() < deadline) {
    await flush();
    if (!dialog()) await act(() => new Promise(resolve => setTimeout(resolve, 10)));
  }
  expect(dialog()).toBeTruthy();
}
async function openFrom(button: HTMLButtonElement) {
  act(() => button.focus());
  await press('k', {ctrlKey: true}, button);
  await opened();
}

function page() {
  render(<><button id="opener" onClick={openPalette}>Search</button><main id="view" tabIndex={-1} /><PaletteLauncher route={{view: 'home'} as never} /></>);
  return document.getElementById('opener') as HTMLButtonElement;
}

beforeEach(() => { location.hash = '#/'; localStorage.clear(); });
afterEach(() => { location.hash = ''; });

describe('command palette', () => {
  it('stays unloaded until the first request', async () => {
    page();
    await flush();
    expect(dialog()).toBeUndefined();
    expect(document.querySelector('[data-slot]')).toBeNull();
  });

  it('is a named modal dialog whose search field is a named combobox over a named listbox', async () => {
    await openFrom(page());
    expect(accessibleName(dialog())).toBe('Go to');
    expect(dialog().getAttribute('aria-modal')).toBe('true');
    expect(accessibleName(input())).toBe('Search pages, sandboxes, projects and sections');
    const list = byRole('listbox')[0];
    expect(input().getAttribute('aria-controls')).toBe(list.id);
    expect(accessibleName(list)).toBe('Results');
    expect(document.activeElement).toBe(input());
    expect(dialog().querySelector('button')!.textContent).toBe('Esc to close');
  });

  it('moves the highlight with the arrow keys, pages and jumps, and keeps focus in the field', async () => {
    await openFrom(page());
    expect(highlighted()).toEqual([options()[0]]);
    expect(activeOption()).toBe(options()[0]);
    await press('ArrowDown');
    expect(highlighted()).toEqual([options()[1]]);
    expect(activeOption()).toBe(options()[1]);
    await press('ArrowUp');
    await press('ArrowUp');
    expect(activeOption()).toBe(options().at(-1));
    await press('Home', {ctrlKey: true});
    expect(activeOption()).toBe(options()[0]);
    await press('PageDown');
    expect(activeOption()).toBe(options()[Math.min(10, options().length - 1)]);
    await press('PageUp');
    expect(activeOption()).toBe(options()[0]);
    await press('End', {ctrlKey: true});
    expect(activeOption()).toBe(options().at(-1));
    expect(document.activeElement).toBe(input());
  });

  it('ranks with the console search, highlights the best match and opens it with Enter', async () => {
    const opener = page();
    await openFrom(opener);
    await press('ArrowDown');
    await type(input(), 'orca internet');
    expect(options()[0].textContent).toContain('Internet');
    expect(options()[0].textContent).toContain('orca-pilot');
    expect(activeOption()).toBe(options()[0]);
    await press('Enter');
    expect(location.hash).toBe('#/sandboxes/orca-pilot/internet');
    expect(dialog()).toBeUndefined();
    expect(document.activeElement?.id).toBe('view');
    expect(JSON.parse(localStorage.getItem('console-palette-recent')!)).toContain('section:s:orca-pilot:internet');
  });

  it('puts focus in the search field when a tap opens it, so a phone shows the keyboard', async () => {
    const opener = page();
    await click(opener, 'touch');
    await opened();
    await press('Escape', {}, input());
    await click(opener, 'touch');
    await opened();
    await flush();
    expect(document.activeElement).toBe(input());
  });

  it('lets Escape end an input method composition without closing', async () => {
    await openFrom(page());
    await type(input(), 'にほ');
    await press('Escape', {isComposing: true, keyCode: 229} as KeyboardEventInit);
    expect(dialog()).toBeTruthy();
    expect(input().value).toBe('にほ');
    await press('Escape');
    expect(dialog()).toBeUndefined();
  });

  it('says when nothing matches', async () => {
    await openFrom(page());
    await type(input(), 'zzzzqqq');
    expect(options()).toHaveLength(0);
    expect(dialog().textContent).toContain('Nothing matches “zzzzqqq”');
    expect(dialog().querySelector('[role="status"]')!.textContent).toBe('No matches');
  });

  it('closes on Escape, even with text typed, and returns focus to what opened it', async () => {
    const opener = page();
    await openFrom(opener);
    await type(input(), 'runs');
    await press('Escape');
    expect(dialog()).toBeUndefined();
    expect(document.activeElement).toBe(opener);
    expect(location.hash).toBe('#/');
  });

  it('closes from its Esc button and from the shortcut, returning focus each time', async () => {
    const opener = page();
    await openFrom(opener);
    await click(dialog().querySelector('button')!);
    expect(dialog()).toBeUndefined();
    expect(document.activeElement).toBe(opener);
    await openFrom(opener);
    await press('k', {ctrlKey: true});
    expect(dialog()).toBeUndefined();
    expect(document.activeElement).toBe(opener);
  });

  it('opens from a button too, and keeps Tab inside the dialog', async () => {
    const opener = page();
    opener.focus();
    await click(opener);
    await opened();
    await flush();
    expect(document.activeElement).toBe(input());
    // jsdom does not move focus on Tab by itself; the wraps at either end are the trap.
    const close = dialog().querySelector('button')!;
    act(() => close.focus());
    await press('Tab');
    expect(document.activeElement).toBe(input());
    await press('Tab', {shiftKey: true});
    expect(document.activeElement).toBe(close);
    expect(opener.closest('[inert]')).toBeTruthy();
  });
});
