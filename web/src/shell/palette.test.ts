import {describe, expect, it} from 'vitest';
import {buildCommands, isPaletteShortcut, parseRecent, pushRecent, scoreCommand, searchCommands, shortcutLabel, tokenScore, type Command} from './palette';

const commands = buildCommands({
  sandboxes: [{id: 'orca-pilot', kind: 'Orca runtime'}, {id: 'coding-a', kind: 'Coding sandbox'}, {id: 'research', kind: 'Coding sandbox'}],
  projects: [{name: 'demo-project', repository: 'example/demo-project'}],
  runs: true, orca: true, sandboxControl: true,
});
const top = (query: string, current?: string) => searchCommands(query, commands, {current})[0];
const ids = (list: Command[]) => list.map(c => c.id);

describe('buildCommands', () => {
  it('lists pages, workspaces and every section of each', () => {
    expect(ids(commands)).toContain('page:attention');
    expect(ids(commands)).toContain('sandbox:orca-pilot');
    expect(ids(commands)).toContain('section:s:orca-pilot:internet');
    expect(ids(commands)).toContain('section:s:coding-a:runs');
    expect(ids(commands)).toContain('section:p:demo-project:downloads');
    // Projects have no runs or settings pages.
    expect(ids(commands)).not.toContain('section:p:demo-project:runs');
    expect(ids(commands)).not.toContain('section:p:demo-project:settings');
  });

  it('omits pages the session does not offer', () => {
    const bare = buildCommands({sandboxes: [], projects: [], runs: false, orca: false});
    expect(ids(bare)).not.toContain('page:runs');
    expect(ids(bare)).not.toContain('page:orca');
    expect(ids(bare)).toContain('action:new');
  });

  it('only navigates: every entry is a console hash and no title names a change', () => {
    for (const c of commands) {
      expect(c.href.startsWith('#/')).toBe(true);
      expect(c.title).not.toMatch(/\b(delete|remove|stop|lock|open web|approve|revoke|restrict|grant)\b/i);
    }
  });
});

describe('matching', () => {
  it('finds a sandbox section from its name and the section', () => {
    expect(top('orca-pilot internet')?.id).toBe('section:s:orca-pilot:internet');
    expect(top('orca internet')?.id).toBe('section:s:orca-pilot:internet');
    expect(top('internet orca-pilot')?.id).toBe('section:s:orca-pilot:internet');
    expect(top('coding-a lan')?.id).toBe('section:s:coding-a:lan');
  });

  it('finds sections by the words people use for them', () => {
    expect(top('orca-pilot egress')?.id).toBe('section:s:orca-pilot:internet');
    expect(top('demo upload')?.id).toBe('section:p:demo-project:downloads');
    expect(top('research audit')?.id).toBe('section:s:research:history');
    expect(top('coding-a dev links')?.id).toBe('section:s:coding-a:previews');
  });

  it('puts an exact workspace name before its sections', () => {
    expect(top('orca-pilot')?.id).toBe('sandbox:orca-pilot');
    expect(top('research')?.id).toBe('sandbox:research');
    expect(top('demo-project')?.id).toBe('project:demo-project');
  });

  it('matches letters in order when they stay close', () => {
    expect(tokenScore('rsrch', 'research')).toBeGreaterThan(0);
    expect(tokenScore('lan', 'LAN devices')).toBe(8);
    expect(tokenScore('dev', 'LAN devices')).toBe(6);
    expect(tokenScore('vice', 'LAN devices')).toBe(4);
    expect(tokenScore('rh', 'research')).toBe(0);
    expect(tokenScore('oxz', 'orca-pilot')).toBe(0);
    // Short tokens never match inside a word.
    expect(tokenScore('lan', 'at a glance')).toBe(0);
    expect(searchCommands('lan', commands).every(c => c.kind === 'section')).toBe(true);
  });

  it('needs every word to match', () => {
    expect(searchCommands('orca-pilot zebra', commands)).toEqual([]);
    expect(scoreCommand('nothing-like-this', commands[0])).toBeNull();
  });

  it('prefers the sections of the workspace on screen', () => {
    expect(top('lan', 's:coding-a')?.id).toBe('section:s:coding-a:lan');
    expect(top('lan', 's:research')?.id).toBe('section:s:research:lan');
  });

  it('finds pages and safe actions', () => {
    expect(top('runs')?.id).toBe('page:runs');
    expect(top('new')?.id).toBe('action:new');
    expect(top('inbox')?.id).toBe('page:attention');
    expect(top('waiting')?.kind).toMatch(/page|action/);
  });

  it('ignores case, accents and extra spaces', () => {
    expect(top('  ORCA-PILOT   Internet ')?.id).toBe('section:s:orca-pilot:internet');
    expect(top('rèsearch')?.id).toBe('sandbox:research');
  });

  it('caps the result list', () => {
    expect(searchCommands('a', commands, {limit: 5})).toHaveLength(5);
  });
});

describe('empty query', () => {
  it('shows recent picks first, then the current workspace sections, then pages and workspaces', () => {
    const list = searchCommands('', commands, {recent: ['section:s:research:lan', 'gone'], current: 's:coding-a'});
    expect(list[0].id).toBe('section:s:research:lan');
    expect(list[1].id).toBe('section:s:coding-a:internet');
    expect(ids(list)).toContain('page:home');
    expect(ids(list)).toContain('sandbox:orca-pilot');
    expect(ids(list)).not.toContain('section:s:orca-pilot:internet');
    expect(new Set(ids(list)).size).toBe(list.length);
  });
});

describe('recent picks', () => {
  it('keeps the newest first without repeats', () => {
    expect(pushRecent(['a', 'b', 'c'], 'b')).toEqual(['b', 'a', 'c']);
    expect(pushRecent(['a', 'b', 'c', 'd', 'e'], 'f')).toEqual(['f', 'a', 'b', 'c', 'd']);
  });
  it('survives junk in storage', () => {
    expect(parseRecent(null)).toEqual([]);
    expect(parseRecent('{')).toEqual([]);
    expect(parseRecent('{"a":1}')).toEqual([]);
    expect(parseRecent('["a",2,"b"]')).toEqual(['a', 'b']);
  });
});

describe('shortcut', () => {
  const key = (over: Partial<{key: string; ctrlKey: boolean; metaKey: boolean; altKey: boolean; shiftKey: boolean}>) => ({key: 'k', ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...over});
  it('opens on Ctrl-K or Cmd-K only', () => {
    expect(isPaletteShortcut(key({ctrlKey: true}))).toBe(true);
    expect(isPaletteShortcut(key({metaKey: true}))).toBe(true);
    expect(isPaletteShortcut(key({}))).toBe(false);
    expect(isPaletteShortcut(key({ctrlKey: true, shiftKey: true}))).toBe(false);
    expect(isPaletteShortcut(key({ctrlKey: true, altKey: true}))).toBe(false);
    expect(isPaletteShortcut(key({ctrlKey: true, key: 'j'}))).toBe(false);
  });
  it('labels the shortcut for the platform', () => {
    expect(shortcutLabel('MacIntel')).toBe('⌘K');
    expect(shortcutLabel('Linux x86_64')).toBe('Ctrl K');
  });
});

describe('access levels in the palette', () => {
  it('shows a workspace level and finds workspaces by it', () => {
    const commands = buildCommands({sandboxes: [{id: 'box-a', kind: 'Coding sandbox', level: 'Standard'}, {id: 'box-b', kind: 'Coding sandbox', level: 'Open'}], projects: [{name: 'site', repository: 'example/site', level: 'Locked'}], runs: false, orca: false});
    expect(commands.find(c => c.id === 'sandbox:box-a')?.context).toBe('Coding sandbox · Standard');
    expect(commands.find(c => c.id === 'project:site')?.context).toBe('example/site · Locked');
    const hits = searchCommands('standard', commands).filter(c => c.kind === 'sandbox' || c.kind === 'project').map(c => c.id);
    expect(hits).toEqual(['sandbox:box-a']);
  });
});
