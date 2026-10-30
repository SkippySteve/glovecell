import {describe, expect, it} from 'vitest';
import {breadcrumbs, hrefOf, parse, projectHref, sandboxHref, sectionsFor, toSection, withQuery, type Route} from './router';

describe('parse', () => {
  it('reads the top-level pages', () => {
    expect(parse('')).toEqual({view: 'home'});
    expect(parse('#/')).toEqual({view: 'home'});
    expect(parse('#/new')).toEqual({view: 'new'});
    expect(parse('#/orca')).toEqual({view: 'orca'});
    expect(parse('#/runs')).toEqual({view: 'runs'});
    expect(parse('#/runs/r-1')).toEqual({view: 'run', id: 'r-1'});
  });

  it('adds the attention inbox and the sandbox and project lists', () => {
    expect(parse('#/attention')).toEqual({view: 'attention'});
    expect(parse('#/inbox')).toEqual({view: 'attention'});
    expect(parse('#/sandboxes')).toEqual({view: 'sandboxes'});
    expect(parse('#/sandboxes/')).toEqual({view: 'sandboxes'});
    expect(parse('#/projects')).toEqual({view: 'projects'});
  });

  it('reads workspaces and their sections, ignoring the query', () => {
    expect(parse('#/sandboxes/orca-pilot/internet?q=github.com&page=2')).toEqual({view: 'sandbox', id: 'orca-pilot', section: 'internet'});
    expect(parse('#/projects/demo/lan')).toEqual({view: 'project', name: 'demo', section: 'lan'});
    expect(parse('#/sandboxes/a%20b')).toEqual({view: 'sandbox', id: 'a b', section: 'overview'});
    expect(parse('#/sandboxes/%E0%A4%A')).toEqual({view: 'sandbox', id: '%E0%A4%A', section: 'overview'});
  });

  it('keeps legacy tab names landing on their sections', () => {
    expect(parse('#/sandboxes/x/access')).toMatchObject({section: 'internet'});
    expect(parse('#/sandboxes/x/devlinks')).toMatchObject({section: 'previews'});
    expect(parse('#/sandboxes/x/activity')).toMatchObject({section: 'history'});
    expect(parse('#/projects/x/repositories')).toMatchObject({section: 'credentials'});
    expect(parse('#/projects/x/files')).toMatchObject({section: 'downloads'});
    expect(toSection('nonsense')).toBe('overview');
    expect(toSection(undefined)).toBe('overview');
  });

  it('sends unknown hashes home', () => {
    expect(parse('#/nowhere/at/all')).toEqual({view: 'home'});
  });
});

describe('hrefOf', () => {
  const routes: Route[] = [
    {view: 'home'}, {view: 'new'}, {view: 'attention'}, {view: 'sandboxes'}, {view: 'projects'}, {view: 'runs'}, {view: 'run', id: 'r/1'}, {view: 'orca'},
    {view: 'sandbox', id: 'orca-pilot', section: 'overview'}, {view: 'sandbox', id: 'orca-pilot', section: 'lan'}, {view: 'project', name: 'demo', section: 'history'},
  ];
  it('round-trips through parse', () => {
    for (const route of routes) expect(parse(hrefOf(route))).toEqual(route);
  });
  it('omits the overview section', () => {
    expect(sandboxHref('a', 'overview')).toBe('#/sandboxes/a');
    expect(projectHref('a b', 'internet')).toBe('#/projects/a%20b/internet');
    expect(withQuery('#/?q=1', {show: 'open', q: ''})).toBe('#/?show=open');
  });
});

describe('sectionsFor', () => {
  const keys = (list: [string, string][]) => list.map(([k]) => k);
  it('lists Runs only with run records and never for projects', () => {
    expect(keys(sectionsFor('sandbox', true))).toContain('runs');
    expect(keys(sectionsFor('sandbox', false))).not.toContain('runs');
    expect(keys(sectionsFor('project', true))).not.toContain('runs');
    expect(keys(sectionsFor('project', true))).not.toContain('settings');
    expect(sectionsFor('sandbox', true)).toHaveLength(10);
  });
});

describe('breadcrumbs', () => {
  it('has no trail on Home', () => {
    expect(breadcrumbs({view: 'home'})).toEqual([{label: 'Home'}]);
  });
  it('walks from Home to a sandbox section', () => {
    expect(breadcrumbs({view: 'sandbox', id: 'orca-pilot', section: 'internet'})).toEqual([
      {label: 'Home', href: '#/'}, {label: 'Sandboxes', href: '#/sandboxes'}, {label: 'orca-pilot', href: '#/sandboxes/orca-pilot'}, {label: 'Internet'},
    ]);
    expect(breadcrumbs({view: 'project', name: 'demo', section: 'downloads'}).map(c => c.label)).toEqual(['Home', 'Projects', 'demo', 'Files']);
    expect(breadcrumbs({view: 'run', id: 'r1'}).map(c => c.label)).toEqual(['Home', 'Runs', 'r1']);
  });
  it('never links the current page', () => {
    const routes: Route[] = [{view: 'attention'}, {view: 'new'}, {view: 'orca'}, {view: 'sandboxes'}, {view: 'sandbox', id: 'a', section: 'lan'}];
    for (const route of routes) expect(breadcrumbs(route).at(-1)?.href).toBeUndefined();
  });
});
