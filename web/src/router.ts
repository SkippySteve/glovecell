import {useEffect, useState} from 'react';

// Sidebar sections of the sandbox workspace view (Option B). Runs and Settings
// keep pages the console already had; the rest follow the approved mockup.
export type Section = 'overview' | 'internet' | 'lan' | 'packages' | 'previews' | 'credentials' | 'history' | 'runs' | 'settings';
export const SECTIONS: [Section, string][] = [
  ['overview', 'Overview'], ['internet', 'Internet'], ['lan', 'LAN devices'], ['packages', 'Packages'],
  ['previews', 'Previews & dev links'], ['credentials', 'Credentials'], ['history', 'History'], ['runs', 'Runs'], ['settings', 'Settings'],
];
// Earlier tab names, so bookmarks and links from older pages still land.
const LEGACY: Record<string, Section> = {access: 'internet', devlinks: 'previews', activity: 'history', repositories: 'credentials'};
export const toSection = (value: string | undefined): Section => SECTIONS.find(([key]) => key === value)?.[0] || (value && LEGACY[value]) || 'overview';

export type Route = {view: 'home'} | {view: 'new'} | {view: 'project'; name: string; section: Section} | {view: 'sandbox'; id: string; section: Section} | {view: 'runs'} | {view: 'run'; id: string} | {view: 'orca'};

export const projectHref = (name: string, section?: Section) => '#/projects/' + encodeURIComponent(name) + (section && section !== 'overview' ? '/' + section : '');
export const sandboxHref = (id: string, section?: Section) => '#/sandboxes/' + encodeURIComponent(id) + (section && section !== 'overview' ? '/' + section : '');
export const orcaHref = () => '#/orca';
export const navigate = (hash: string) => { location.hash = hash; };

export function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(part => { try { return decodeURIComponent(part); } catch { return part; } });
  if (parts[0] === 'new') return {view: 'new'};
  if (parts[0] === 'orca') return {view: 'orca'};
  if (parts[0] === 'projects' && parts[1]) return {view: 'project', name: parts[1], section: toSection(parts[2])};
  if (parts[0] === 'sandboxes' && parts[1]) return {view: 'sandbox', id: parts[1], section: toSection(parts[2])};
  if (parts[0] === 'runs') return parts[1] ? {view: 'run', id: parts[1]} : {view: 'runs'};
  return {view: 'home'};
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
