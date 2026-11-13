import {useEffect, useState} from 'react';

// Sections of the sandbox workspace view (Option B). Runs and Settings keep
// pages the console already had; the rest follow the approved mockup.
export type Section = 'overview' | 'internet' | 'lan' | 'packages' | 'downloads' | 'previews' | 'credentials' | 'history' | 'runs' | 'settings';
export const SECTIONS: [Section, string][] = [
  ['overview', 'Overview'], ['internet', 'Internet'], ['lan', 'LAN devices'], ['packages', 'Packages'], ['downloads', 'Files'],
  ['previews', 'Previews & dev links'], ['credentials', 'Credentials'], ['history', 'History'], ['runs', 'Runs'], ['settings', 'Settings'],
];
export const sectionLabel = (section: Section) => SECTIONS.find(([key]) => key === section)?.[1] || 'Overview';
// Earlier tab names, and the words people type for a section, so bookmarks,
// links from older pages and hand-written hashes still land.
const LEGACY: Record<string, Section> = {access: 'internet', devlinks: 'previews', activity: 'history', repositories: 'credentials', files: 'downloads', uploads: 'downloads', web: 'internet'};
export const toSection = (value: string | undefined): Section => SECTIONS.find(([key]) => key === value)?.[0] || (value && LEGACY[value]) || 'overview';

// Sections each kind of workspace lists. Project sandboxes have no runs or
// settings page; Runs appears only when the console collects run records.
export type WorkspaceKind = 'sandbox' | 'project';
export const sectionsFor = (kind: WorkspaceKind, runs: boolean) => SECTIONS.filter(([key]) => kind === 'sandbox' ? key !== 'runs' || runs : key !== 'runs' && key !== 'settings');

export type Route = {view: 'home'} | {view: 'new'} | {view: 'attention'} | {view: 'sandboxes'} | {view: 'projects'}
  | {view: 'project'; name: string; section: Section} | {view: 'sandbox'; id: string; section: Section} | {view: 'runs'} | {view: 'run'; id: string} | {view: 'orca'} | {view: 'hub'};

export const homeHref = () => '#/';
export const attentionHref = () => '#/attention';
export const sandboxesHref = () => '#/sandboxes';
export const projectsHref = () => '#/projects';
export const newSandboxHref = () => '#/new';
export const projectHref = (name: string, section?: Section) => '#/projects/' + encodeURIComponent(name) + (section && section !== 'overview' ? '/' + section : '');
export const sandboxHref = (id: string, section?: Section) => '#/sandboxes/' + encodeURIComponent(id) + (section && section !== 'overview' ? '/' + section : '');
export const orcaHref = () => '#/orca';
export const hubHref = () => '#/hub';
export const navigate = (hash: string) => { location.hash = hash; };

// A section may keep view state (filter, page, page size) in a query after the
// route, as in #/sandboxes/orca-pilot/internet?q=github.com&size=50&page=3, so
// reloads and shared links keep it. The route ignores the query.
export const hashQuery = (hash = location.hash) => new URLSearchParams(hash.includes('?') ? hash.slice(hash.indexOf('?') + 1) : '');
export function withQuery(href: string, params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, String(v));
  const text = q.toString();
  return href.split('?')[0] + (text ? '?' + text : '');
}
// Rewrites the query without a hashchange (no scroll, no reload).
export const replaceHashQuery = (params: Record<string, string | number | undefined>) => history.replaceState(history.state, '', withQuery(location.hash || '#/', params));

export function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('?')[0].split('/').filter(Boolean).map(part => { try { return decodeURIComponent(part); } catch { return part; } });
  if (parts[0] === 'new') return {view: 'new'};
  if (parts[0] === 'orca') return {view: 'orca'};
  if (parts[0] === 'hub') return {view: 'hub'};
  if (parts[0] === 'attention' || parts[0] === 'inbox') return {view: 'attention'};
  if (parts[0] === 'projects') return parts[1] ? {view: 'project', name: parts[1], section: toSection(parts[2])} : {view: 'projects'};
  if (parts[0] === 'sandboxes') return parts[1] ? {view: 'sandbox', id: parts[1], section: toSection(parts[2])} : {view: 'sandboxes'};
  if (parts[0] === 'runs') return parts[1] ? {view: 'run', id: parts[1]} : {view: 'runs'};
  return {view: 'home'};
}

// The canonical hash of a route, without its query.
export function hrefOf(route: Route): string {
  switch (route.view) {
    case 'home': return homeHref();
    case 'new': return newSandboxHref();
    case 'attention': return attentionHref();
    case 'sandboxes': return sandboxesHref();
    case 'projects': return projectsHref();
    case 'sandbox': return sandboxHref(route.id, route.section);
    case 'project': return projectHref(route.name, route.section);
    case 'runs': return '#/runs';
    case 'run': return '#/runs/' + encodeURIComponent(route.id);
    case 'orca': return orcaHref();
    case 'hub': return hubHref();
  }
}

// The trail above a page. The last crumb is the page itself and has no link.
export interface Crumb { label: string; href?: string }
export function breadcrumbs(route: Route): Crumb[] {
  const home: Crumb = {label: 'Home', href: homeHref()};
  switch (route.view) {
    case 'home': return [{label: 'Home'}];
    case 'new': return [home, {label: 'New sandbox'}];
    case 'attention': return [home, {label: 'Needs attention'}];
    case 'sandboxes': return [home, {label: 'Sandboxes'}];
    case 'projects': return [home, {label: 'Projects'}];
    case 'sandbox': return [home, {label: 'Sandboxes', href: sandboxesHref()}, {label: route.id, href: sandboxHref(route.id)}, {label: sectionLabel(route.section)}];
    case 'project': return [home, {label: 'Projects', href: projectsHref()}, {label: route.name, href: projectHref(route.name)}, {label: sectionLabel(route.section)}];
    case 'runs': return [home, {label: 'Runs'}];
    case 'run': return [home, {label: 'Runs', href: '#/runs'}, {label: route.id}];
    case 'orca': return [home, {label: 'Orca projects'}];
    case 'hub': return [home, {label: 'Hub'}];
  }
}

export function useRoute() {
  const [route, setRoute] = useState(() => parse(location.hash));
  useEffect(() => {
    const change = () => { setRoute(parse(location.hash)); window.scrollTo(0, 0); document.querySelector<HTMLElement>('#view')?.focus({preventScroll: true}); };
    window.addEventListener('hashchange', change);
    return () => window.removeEventListener('hashchange', change);
  }, []);
  return route;
}
