import {useEffect, useState} from 'react';

export type Tab = 'overview' | 'repositories' | 'access' | 'history';
export const TABS: [Tab, string][] = [['overview', 'Overview'], ['repositories', 'Repositories'], ['access', 'Access'], ['history', 'History']];
export type SandboxTab = 'overview' | 'access' | 'devlinks' | 'previews' | 'activity' | 'runs' | 'settings';
export const SANDBOX_TABS: [SandboxTab, string][] = [['overview', 'Overview'], ['access', 'Web access'], ['devlinks', 'Dev links'], ['previews', 'Previews'], ['activity', 'Activity'], ['runs', 'Runs'], ['settings', 'Settings']];
export type Route = {view: 'home'} | {view: 'new'} | {view: 'project'; name: string; tab: Tab} | {view: 'sandbox'; id: string; tab: SandboxTab} | {view: 'runs'} | {view: 'run'; id: string};

export const projectHref = (name: string, tab?: Tab) => '#/projects/' + encodeURIComponent(name) + (tab && tab !== 'overview' ? '/' + tab : '');
export const sandboxHref = (id: string, tab?: SandboxTab) => '#/sandboxes/' + encodeURIComponent(id) + (tab && tab !== 'overview' ? '/' + tab : '');
export const navigate = (hash: string) => { location.hash = hash; };

function parse(): Route {
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  if (parts[0] === 'new') return {view: 'new'};
  if (parts[0] === 'projects' && parts[1]) return {view: 'project', name: parts[1], tab: TABS.find(([key]) => key === parts[2])?.[0] || 'overview'};
  if (parts[0] === 'sandboxes' && parts[1]) return {view: 'sandbox', id: parts[1], tab: SANDBOX_TABS.find(([key]) => key === parts[2])?.[0] || 'overview'};
  if (parts[0] === 'runs') return parts[1] ? {view: 'run', id: parts[1]} : {view: 'runs'};
  return {view: 'home'};
}

export function useRoute() {
  const [route, setRoute] = useState(parse);
  useEffect(() => {
    const change = () => { setRoute(parse()); window.scrollTo(0, 0); document.querySelector<HTMLElement>('#view')?.focus({preventScroll: true}); };
    window.addEventListener('hashchange', change);
    return () => window.removeEventListener('hashchange', change);
  }, []);
  return route;
}
