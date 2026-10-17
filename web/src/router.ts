import {useEffect, useState} from 'react';

export type Tab = 'overview' | 'repositories' | 'access' | 'history';
export const TABS: [Tab, string][] = [['overview', 'Overview'], ['repositories', 'Repositories'], ['access', 'Access'], ['history', 'History']];
export type Route = {view: 'home'} | {view: 'new'} | {view: 'project'; name: string; tab: Tab};

export const projectHref = (name: string, tab?: Tab) => '#/projects/' + encodeURIComponent(name) + (tab && tab !== 'overview' ? '/' + tab : '');
export const navigate = (hash: string) => { location.hash = hash; };

function parse(): Route {
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  if (parts[0] === 'new') return {view: 'new'};
  if (parts[0] === 'projects' && parts[1]) return {view: 'project', name: parts[1], tab: TABS.find(([key]) => key === parts[2])?.[0] || 'overview'};
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
