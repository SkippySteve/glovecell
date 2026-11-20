// Ctrl-K (⌘K on a Mac) command palette: jump to any page, sandbox, project or
// section. UI SDK's Dialog keeps focus inside and makes the page inert; its
// Command is the combobox over the results listbox. Entries only navigate.
// Ranking stays ours (palette.ts): Command gets filter={false} and keeps that order.
import {Command, CommandInput, CommandItem, CommandList, createCommandController} from '@ui-sdk/react/command';
import {Dialog, DialogClose, DialogContent, DialogTitle} from '@ui-sdk/react/dialog';
import {useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent} from 'react';
import {useStandingAccess} from '../access/standing';
import {levelWord, projectAccess, sandboxAccess} from '../access/summary';
import {useData} from '../data';
import {existingKind, orcaRuntimeId} from '../orca';
import {navigate, type Route} from '../router';
import {buildCommands, isPaletteShortcut, KIND_LABEL, parseRecent, pushRecent, searchCommands, shortcutLabel, type Command as PaletteCommand} from './palette';

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

export function CommandPalette({route}: {route: Route}) {
  const {existing, projects, session} = useData();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [recent, setRecent] = useState<string[]>([]);
  const controller = useMemo(() => createCommandController(), []);
  // Where focus goes on close: the page a pick opened, or back where the operator was.
  const opener = useRef<HTMLElement | null>(null);
  const picked = useRef(false);
  const isOpen = useRef(false);
  useEffect(() => { isOpen.current = open; }, [open]);

  const runtime = orcaRuntimeId(session, existing);
  // Each workspace's access level, read only while the palette is open.
  const levels = useStandingAccess(open);
  const commands = useMemo(() => buildCommands({
    sandboxes: existing.map(s => ({id: s.id, kind: existingKind(s, runtime), level: levelWord(sandboxAccess(s, levels.rules[s.id]).state)})),
    projects: projects.filter(p => p.status?.phase !== 'Removed').map(p => ({name: p.metadata.name, repository: p.spec.repository, level: levelWord(projectAccess(p, levels.profiles).state)})),
    runs: Boolean(session.runRecords), orca: Boolean(session.orcaProjects), sandboxControl: Boolean(session.sandboxControl),
  }), [existing, projects, session, runtime, levels]);
  const current = route.view === 'sandbox' ? 's:' + route.id : route.view === 'project' ? 'p:' + route.name : undefined;
  const results = useMemo(() => searchCommands(query, commands, {current, recent}), [query, commands, current, recent]);
  const byId = useMemo(() => new Map(results.map(c => [c.id, c])), [results]);
  const recentSet = useMemo(() => new Set(recent), [recent]);

  const show = useCallback(() => {
    opener.current = document.activeElement as HTMLElement | null;
    picked.current = false;
    setRecent(readRecent());
    setQuery(''); setOpen(true);
  }, []);
  // A pick navigates; the router then moves focus to the new page.
  const choose = useCallback((command: PaletteCommand | undefined) => {
    if (!command) return;
    saveRecent(command.id);
    picked.current = true;
    setOpen(false);
    if (location.hash === command.href) document.querySelector<HTMLElement>('#view')?.focus();
    else navigate(command.href);
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isPaletteShortcut(e)) return;
      e.preventDefault();
      if (isOpen.current) setOpen(false); else show();
    };
    window.addEventListener('keydown', onKey);
    window.addEventListener(OPEN_EVENT, show);
    return () => { window.removeEventListener('keydown', onKey); window.removeEventListener(OPEN_EVENT, show); };
  }, [show]);

  // Command pages with PageUp/PageDown but leaves Home and End to the caret; the
  // palette also jumps with Ctrl+Home/End, as before. Esc closes at once rather
  // than first clearing the search (Command's default).
  const onKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') { e.preventDefault(); setOpen(false); return; }
    if ((e.key === 'Home' || e.key === 'End') && e.ctrlKey && results.length) {
      e.preventDefault();
      controller.setValue(results[e.key === 'Home' ? 0 : results.length - 1].id);
    }
  };

  const status = !query.trim() ? 'Type to search pages, sandboxes, projects and sections.' : results.length ? `${results.length} result${results.length === 1 ? '' : 's'}` : 'No matches';
  return (
    <Dialog open={open} onOpenChange={next => setOpen(next)}>
      <DialogContent className="palette" finalFocus={() => picked.current ? document.querySelector<HTMLElement>('#view') : opener.current}>
        <Command className="palette-body" controller={controller} filter={false} loop
          query={query} onQueryChange={next => setQuery(next)}
          onAction={id => choose(byId.get(id))}
          announcement={() => null}>
          <DialogTitle className="visually-hidden">Go to</DialogTitle>
          <div className="palette-field">
            <CommandInput className="palette-input" type="text" autoComplete="off" spellCheck={false}
              aria-label="Search pages, sandboxes, projects and sections" placeholder="Search or jump to…" onKeyDown={onKeyDown} />
            <DialogClose className="palette-close">Esc<span className="visually-hidden"> to close</span></DialogClose>
          </div>
          <CommandList className="palette-list" aria-label="Results">
            {results.map(c => (
              <CommandItem key={c.id} value={c.id} label={c.title} className="palette-option">
                <span className="palette-title">{c.title}{c.kind === 'section' ? <span className="palette-in"> in {c.context}</span> : null}</span>
                {c.kind !== 'section' && c.context ? <span className="palette-context">{c.context}</span> : null}
                <span className="palette-kind">{!query.trim() && recentSet.has(c.id) ? 'Recent' : KIND_LABEL[c.kind]}</span>
              </CommandItem>
            ))}
          </CommandList>
          {query.trim() && !results.length ? <p className="palette-empty">Nothing matches “{query.trim()}”. Try a sandbox name, a section such as internet or files, or a page such as runs.</p> : null}
          <p className="palette-hint" aria-hidden="true"><kbd>↑</kbd><kbd>↓</kbd> move <kbd>Enter</kbd> open <kbd>Esc</kbd> close</p>
          <p className="visually-hidden" role="status" aria-live="polite">{status}</p>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
