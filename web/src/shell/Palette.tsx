// Ctrl-K (⌘K on a Mac) command palette: jump to any page, sandbox, project or
// section. A native modal <dialog> keeps focus inside and makes the page inert;
// the input is an ARIA combobox over a listbox of results. Entries only navigate.
import {useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent} from 'react';
import {useData} from '../data';
import {existingKind, orcaRuntimeId} from '../orca';
import {navigate, type Route} from '../router';
import {buildCommands, isPaletteShortcut, KIND_LABEL, parseRecent, pushRecent, searchCommands, shortcutLabel, type Command} from './palette';

const OPEN_EVENT = 'console-palette-open';
const RECENT_KEY = 'console-palette-recent';
export const openPalette = () => window.dispatchEvent(new Event(OPEN_EVENT));
export const paletteShortcut = () => shortcutLabel(typeof navigator === 'undefined' ? '' : navigator.platform || navigator.userAgent);

function readRecent(): string[] {
  try { return parseRecent(localStorage.getItem(RECENT_KEY)); } catch { return []; }
}
function saveRecent(id: string) {
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(pushRecent(readRecent(), id))); } catch { /* private mode: no recents */ }
}

const FOCUSABLE = 'input, button, a[href], [tabindex]:not([tabindex="-1"])';

export function CommandPalette({route}: {route: Route}) {
  const {existing, projects, session} = useData();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const [recent, setRecent] = useState<string[]>([]);
  const dialog = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const id = useId();
  const listId = id + '-list', optionId = (i: number) => `${id}-opt-${i}`;

  const runtime = orcaRuntimeId(session, existing);
  const commands = useMemo(() => buildCommands({
    sandboxes: existing.map(s => ({id: s.id, kind: existingKind(s, runtime)})),
    projects: projects.filter(p => p.status?.phase !== 'Removed').map(p => ({name: p.metadata.name, repository: p.spec.repository})),
    runs: Boolean(session.runRecords), orca: Boolean(session.orcaProjects), sandboxControl: Boolean(session.sandboxControl),
  }), [existing, projects, session, runtime]);
  const current = route.view === 'sandbox' ? 's:' + route.id : route.view === 'project' ? 'p:' + route.name : undefined;
  const results = useMemo(() => searchCommands(query, commands, {current, recent}), [query, commands, current, recent]);
  const recentSet = useMemo(() => new Set(recent), [recent]);

  const show = useCallback(() => {
    opener.current = document.activeElement as HTMLElement | null;
    setRecent(readRecent());
    setQuery(''); setActive(0); setOpen(true);
  }, []);
  // Esc or a click outside: back to where the operator was.
  const dismiss = useCallback(() => {
    setOpen(false);
    dialog.current?.close();
    opener.current?.focus?.();
  }, []);
  // A pick navigates; the router then moves focus to the new page.
  const choose = useCallback((command: Command | undefined) => {
    if (!command) return;
    saveRecent(command.id);
    setOpen(false);
    dialog.current?.close();
    if (location.hash === command.href) document.querySelector<HTMLElement>('#view')?.focus();
    else navigate(command.href);
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isPaletteShortcut(e)) return;
      e.preventDefault();
      if (dialog.current?.open) dismiss(); else show();
    };
    window.addEventListener('keydown', onKey);
    window.addEventListener(OPEN_EVENT, show);
    return () => { window.removeEventListener('keydown', onKey); window.removeEventListener(OPEN_EVENT, show); };
  }, [show, dismiss]);

  useEffect(() => {
    const d = dialog.current;
    if (!open || !d) return;
    if (!d.open) d.showModal();
    input.current?.focus();
  }, [open]);

  // Keep the active option in view as the arrow keys move it.
  useEffect(() => {
    if (!open) return;
    list.current?.querySelector<HTMLElement>(`[id="${optionId(active)}"]`)?.scrollIntoView({block: 'nearest'});
  }, [active, open]); // optionId is stable for this component

  const onKeyDown = (e: ReactKeyboardEvent<HTMLDialogElement>) => {
    const last = results.length - 1;
    if (e.key === 'ArrowDown') { e.preventDefault(); setActive(i => i >= last ? 0 : i + 1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive(i => i <= 0 ? Math.max(last, 0) : i - 1); }
    else if (e.key === 'PageDown') { e.preventDefault(); setActive(i => Math.min(last, i + 8)); }
    else if (e.key === 'PageUp') { e.preventDefault(); setActive(i => Math.max(0, i - 8)); }
    else if ((e.key === 'Home' || e.key === 'End') && e.ctrlKey) { e.preventDefault(); setActive(e.key === 'Home' ? 0 : Math.max(last, 0)); }
    else if (e.key === 'Enter' && e.target === input.current) { e.preventDefault(); choose(results[active]); }
    else if (e.key === 'Tab') {
      // Focus stays inside the palette.
      const items = [...(dialog.current?.querySelectorAll<HTMLElement>(FOCUSABLE) || [])];
      if (!items.length) return;
      const at = items.indexOf(document.activeElement as HTMLElement);
      const next = e.shiftKey ? (at <= 0 ? items.length - 1 : at - 1) : (at === items.length - 1 ? 0 : at + 1);
      e.preventDefault();
      items[next].focus();
    }
  };

  const status = !query.trim() ? 'Type to search pages, sandboxes, projects and sections.' : results.length ? `${results.length} result${results.length === 1 ? '' : 's'}` : 'No matches';
  return (
    <dialog ref={dialog} className="palette" aria-labelledby={id + '-title'} onKeyDown={onKeyDown}
      onCancel={e => { e.preventDefault(); dismiss(); }}
      onClick={e => { if (e.target === dialog.current) dismiss(); }}>
      {open ? <div className="palette-body">
        <h2 id={id + '-title'} className="visually-hidden">Go to</h2>
        <div className="palette-field">
          <label htmlFor={id + '-input'} className="visually-hidden">Search pages, sandboxes, projects and sections</label>
          <input ref={input} id={id + '-input'} className="palette-input" type="text" autoComplete="off" spellCheck={false}
            placeholder="Search or jump to…" value={query}
            role="combobox" aria-expanded={results.length > 0} aria-controls={listId} aria-autocomplete="list"
            aria-activedescendant={results.length ? optionId(Math.min(active, results.length - 1)) : undefined}
            onChange={e => { setQuery(e.target.value); setActive(0); }} />
          <button type="button" className="palette-close" onClick={dismiss}>Esc<span className="visually-hidden"> to close</span></button>
        </div>
        <ul ref={list} id={listId} className="palette-list" role="listbox" aria-label="Results">
          {results.map((c, i) => (
            <li key={c.id} id={optionId(i)} role="option" aria-selected={i === active} className="palette-option"
              onMouseMove={() => { if (i !== active) setActive(i); }}
              onMouseDown={e => e.preventDefault()}
              onClick={() => choose(c)}>
              <span className="palette-title">{c.title}{c.kind === 'section' ? <span className="palette-in"> in {c.context}</span> : null}</span>
              {c.kind !== 'section' && c.context ? <span className="palette-context">{c.context}</span> : null}
              <span className="palette-kind">{!query.trim() && recentSet.has(c.id) ? 'Recent' : KIND_LABEL[c.kind]}</span>
            </li>
          ))}
        </ul>
        {query.trim() && !results.length ? <p className="palette-empty">Nothing matches “{query.trim()}”. Try a sandbox name, a section such as internet or files, or a page such as runs.</p> : null}
        <p className="palette-hint" aria-hidden="true"><kbd>↑</kbd><kbd>↓</kbd> move <kbd>Enter</kbd> open <kbd>Esc</kbd> close</p>
        <p className="visually-hidden" role="status" aria-live="polite">{status}</p>
      </div> : null}
    </dialog>
  );
}
