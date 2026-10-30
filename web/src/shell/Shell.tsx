// The app shell: a top bar (brand, search, sign-in), a persistent sidebar on
// desktop that becomes a collapsible menu on phones, and breadcrumbs.
// The sidebar lists every sandbox and project and, for the one on screen, its sections.
import {useEffect, useRef, useState, type MouseEvent, type ReactNode, type RefObject} from 'react';
import type {ExistingSandbox, Project} from '../api';
import {useData} from '../data';
import {attentionHref, breadcrumbs, homeHref, newSandboxHref, orcaHref, projectHref, projectsHref, sandboxesHref, sandboxHref, sectionsFor, type Route, type Section} from '../router';
import {existingState, projectState} from '../state';
import {ThemeSwitcher} from '../ThemeSwitcher';
import {Button} from '../ui';
import {CommandPalette, openPalette, paletteShortcut} from './Palette';
import {useShell} from './state';

// How many workspaces each sidebar group lists before linking to the full list.
const SIDEBAR_LIMIT = 8;

// What the Attention link counts until the inbox reports its own total:
// requests waiting on the operator and projects whose preparation is blocked.
export function attentionCount(existing: ExistingSandbox[], projects: Project[]): number {
  return existing.reduce((n, s) => n + (s.pending || 0) + (s.approvals || 0), 0)
    + projects.filter(p => p.status?.phase !== 'Removed' && projectState(p).attention).length;
}

function Count({n, label, tone}: {n: number; label: string; tone?: 'warn' | 'live' | 'plain'}) {
  if (!n) return null;
  return <span className={'side-badge' + (tone ? ' ' + tone : '')}>{n}<span className="visually-hidden"> {label}</span></span>;
}

interface Item { key: string; kind: 'sandbox' | 'project'; name: string; stopped: boolean; status?: string; waiting: number; attention: boolean; lan: number; href: (s: Section) => string }

function useItems(): {sandboxes: Item[]; projects: Item[]} {
  const {existing, projects} = useData();
  const {lanOpen} = useShell();
  return {
    sandboxes: existing.map(s => ({key: 's:' + s.id, kind: 'sandbox' as const, name: s.id, stopped: existingState(s).label !== 'Running', waiting: (s.pending || 0) + (s.approvals || 0), attention: false, lan: lanOpen[s.id] || 0, href: (sec: Section) => sandboxHref(s.id, sec)}))
      .sort((a, b) => a.name.localeCompare(b.name)),
    projects: projects.filter(p => p.status?.phase !== 'Removed').map(p => {
      const state = projectState(p);
      return {key: 'p:' + p.metadata.name, kind: 'project' as const, name: p.metadata.name, stopped: state.label !== 'Ready', status: state.label.toLowerCase(), waiting: 0, attention: Boolean(state.attention), lan: 0,
        href: (sec: Section) => projectHref(p.metadata.name, sec === 'runs' || sec === 'settings' ? 'overview' : sec)};
    }).sort((a, b) => a.name.localeCompare(b.name)),
  };
}

function WorkspaceGroup({label, href, items, current, section, here, labelId}: {label: string; href: string; items: Item[]; current: string; section: Section; here: boolean; labelId: string}) {
  const {session} = useData();
  const {sectionNav, lanOpen} = useShell();
  const index = items.findIndex(i => i.key === current);
  const shown = items.length <= SIDEBAR_LIMIT ? items : items.filter((_, i) => i < SIDEBAR_LIMIT - 1 || i === index);
  return (
    <li>
      <a className="side-link side-group" href={href} id={labelId} aria-current={here ? 'page' : undefined}>{label}<span className="side-end"><span className="side-total">{items.length}<span className="visually-hidden"> total</span></span></span></a>
      {items.length ? <ul className="side-items" aria-labelledby={labelId}>
        {shown.map(item => {
          const open = item.key === current;
          const badges = open && sectionNav?.key === item.key ? sectionNav.badges : {};
          const lan = item.kind === 'sandbox' ? (open && badges.lan ? badges.lan.n : lanOpen[item.name] || 0) : 0;
          return (
            <li key={item.key} className={open ? 'is-open' : undefined}>
              <a className="side-link side-ws" href={item.href(open ? 'overview' : section)}>
                <span className="side-name">{item.name}{item.stopped ? <span className="side-sub">{item.status || 'stopped'}</span> : null}</span>
                <span className="side-end">
                  {lan ? <span className="side-badge live">LAN<span className="visually-hidden"> access open</span></span> : null}
                  <Count n={item.waiting} label="waiting on you" tone="warn" />
                  {item.attention ? <span className="side-badge warn">!<span className="visually-hidden"> needs attention</span></span> : null}
                </span>
              </a>
              {open ? (
                <ul className="side-sections" aria-label={'Sections of ' + item.name}>
                  {sectionsFor(item.kind, Boolean(session.runRecords)).map(([key, text]) => {
                    const b = badges[key];
                    return <li key={key}><a className="side-link side-section" href={item.href(key)} aria-current={key === section ? 'page' : undefined} data-section={key}>
                      {text}{b?.n ? <span className="side-end"><span className={'side-badge ' + (b.live ? 'live' : 'warn')}>{b.label || b.n}<span className="visually-hidden"> {b.live ? 'open' : 'waiting'}</span></span></span> : null}
                    </a></li>;
                  })}
                </ul>
              ) : null}
            </li>
          );
        })}
        {shown.length < items.length ? <li><a className="side-link side-more" href={href}>All {items.length} {label.toLowerCase()}</a></li> : null}
      </ul> : null}
    </li>
  );
}

export function SideNav({route}: {route: Route}) {
  const {existing, projects, session} = useData();
  const {sandboxes, projects: projectItems} = useItems();
  const current = route.view === 'sandbox' ? 's:' + route.id : route.view === 'project' ? 'p:' + route.name : '';
  const section: Section = route.view === 'sandbox' || route.view === 'project' ? route.section : 'overview';
  const attention = attentionCount(existing, projects);
  const page = (view: Route['view']) => route.view === view ? 'page' as const : undefined;
  return (
    <nav className="side-nav" aria-label="Main">
      <ul className="side-list">
        <li><a className="side-link" href={homeHref()} aria-current={page('home')}>Home</a></li>
        <li><a className="side-link" href={attentionHref()} aria-current={page('attention')}>Needs attention<span className="side-end"><Count n={attention} label="items" tone="warn" /></span></a></li>
      </ul>
      <ul className="side-list">
        {session.sandboxControl || existing.length ? <WorkspaceGroup label="Sandboxes" href={sandboxesHref()} items={sandboxes} current={current} section={section} here={route.view === 'sandboxes'} labelId="side-sandboxes" /> : null}
        <WorkspaceGroup label="Projects" href={projectsHref()} items={projectItems} current={current} section={section} here={route.view === 'projects'} labelId="side-projects" />
      </ul>
      <ul className="side-list">
        {session.runRecords ? <li><a className="side-link" href="#/runs" aria-current={route.view === 'runs' || route.view === 'run' ? 'page' : undefined}>Runs</a></li> : null}
        {session.orcaProjects ? <li><a className="side-link" href={orcaHref()} aria-current={page('orca')}>Orca projects</a></li> : null}
        <li><a className="side-link" href={newSandboxHref()} aria-current={page('new')}>New sandbox</a></li>
      </ul>
    </nav>
  );
}

export function Breadcrumbs({route}: {route: Route}) {
  const crumbs = breadcrumbs(route);
  if (crumbs.length < 2) return null;
  return (
    <nav className="shell-crumbs" aria-label="Breadcrumb">
      <ol>{crumbs.map((c, i) => (
        <li key={i}>{i ? <span className="crumb-sep" aria-hidden="true">/</span> : null}{c.href && i < crumbs.length - 1 ? <a href={c.href}>{c.label}</a> : <span aria-current="page">{c.label}</span>}</li>
      ))}</ol>
    </nav>
  );
}

function TopBar({menuOpen, toggleMenu, menuButton}: {menuOpen: boolean; toggleMenu: () => void; menuButton: RefObject<HTMLButtonElement | null>}) {
  const {session, toast} = useData();
  const logout = async () => {
    const response = await fetch('/auth/logout', {method: 'POST', headers: {'Content-Type': 'application/json'}, credentials: 'same-origin'});
    if (response.ok) location.assign('/auth/signed-out'); else toast('Sign-out failed; try again.', true);
  };
  const shortcut = paletteShortcut();
  return (
    <header className="console-bar">
      <button ref={menuButton} type="button" className="shell-menu-button" aria-expanded={menuOpen} aria-controls="shell-side" onClick={toggleMenu}>
        <span className="shell-menu-icon" aria-hidden="true" />{menuOpen ? 'Close' : 'Menu'}
      </button>
      <a className="console-brand" href={homeHref()}><span className="console-logo" aria-hidden="true" />Sandboxes</a>
      <button type="button" className="shell-search" onClick={openPalette} aria-keyshortcuts="Control+K Meta+K">
        <span className="shell-search-text">Search or jump to…</span><span className="shell-search-short" aria-hidden="true">Search</span><kbd aria-hidden="true">{shortcut}</kbd>
      </button>
      <span className="console-actor subtle">{session.actor || ''}</span>
      <ThemeSwitcher />
      {session.oidc ? <Button variant="tertiary" size="small" onClick={logout}>Sign out</Button> : null}
    </header>
  );
}

export function AppShell({route, workspace, children}: {route: Route; workspace: boolean; children: ReactNode}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const side = useRef<HTMLDivElement>(null);
  // A pick from the phone menu closes it.
  useEffect(() => { setMenuOpen(false); }, [route]);
  useEffect(() => {
    if (!menuOpen) return;
    side.current?.querySelector<HTMLElement>('a')?.focus();
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { setMenuOpen(false); menuButton.current?.focus(); } };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [menuOpen]);
  const skip = (e: MouseEvent) => { e.preventDefault(); document.querySelector<HTMLElement>('#view')?.focus(); };
  return (
    <>
      <a className="skip-link" href="#view" onClick={skip}>Skip to content</a>
      <TopBar menuOpen={menuOpen} toggleMenu={() => setMenuOpen(o => !o)} menuButton={menuButton} />
      <div className="shell">
        <div ref={side} id="shell-side" className={'shell-side' + (menuOpen ? ' is-open' : '')}>
          <SideNav route={route} />
        </div>
        <main id="view" className={workspace ? 'page workspace-page' : 'page'} tabIndex={-1}>
          <Breadcrumbs route={route} />
          {children}
        </main>
      </div>
      <CommandPalette route={route} />
    </>
  );
}

