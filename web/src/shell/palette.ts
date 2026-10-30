// Command palette entries and matching, kept free of React so they are testable.
// Every entry only navigates: the palette offers no action that changes a
// sandbox, so a stray Enter can never open access, stop or delete anything.
import {attentionHref, homeHref, newSandboxHref, orcaHref, projectHref, projectsHref, sandboxesHref, sandboxHref, sectionsFor, type Section} from '../router';

export type CommandKind = 'page' | 'sandbox' | 'project' | 'section' | 'action';
export interface Command {
  id: string;
  kind: CommandKind;
  title: string;
  // The workspace a section belongs to, or a short description.
  context?: string;
  // Extra words that find this entry, never shown.
  keywords?: string;
  href: string;
  // The workspace key ('s:<id>' or 'p:<name>') a workspace or section entry belongs to.
  workspace?: string;
}

export const KIND_LABEL: Record<CommandKind, string> = {page: 'Page', sandbox: 'Sandbox', project: 'Project', section: 'Section', action: 'Action'};

// Words people use for each section, beyond its label.
export const SECTION_KEYWORDS: Record<Section, string> = {
  overview: 'summary status home grants',
  internet: 'web egress http https access requests approve rules domains hosts allow open lock learning',
  lan: 'network local devices tcp ssh ports hosts grants',
  packages: 'npm pypi pip go cargo crates registry dependencies',
  downloads: 'files download upload url transfer',
  previews: 'dev links devlinks servers ports preview apps share',
  credentials: 'repositories repos secrets tokens keys providers git push identity',
  history: 'activity audit log events timeline',
  runs: 'factory orchestration reports gates',
  settings: 'profile policy revisions services shared',
};

export interface CommandSource {
  // level: the workspace's access level in a word ("Standard", "Open", "Fixed policy"), when known.
  sandboxes: {id: string; kind: string; level?: string}[];
  projects: {name: string; repository?: string; level?: string}[];
  runs: boolean;
  orca: boolean;
  sandboxControl?: boolean;
}

// A workspace entry names its access level, and the level's word finds it ("standard", "open").
const withLevel = (context: string, level?: string) => level ? `${context} · ${level}` : context;
const levelWords = (level?: string) => level ? 'access level ' + level.toLowerCase() : undefined;

export function buildCommands(src: CommandSource): Command[] {
  const out: Command[] = [
    {id: 'page:home', kind: 'page', title: 'Home', context: 'Every sandbox and project at a glance', keywords: 'dashboard start overview all', href: homeHref()},
    {id: 'page:attention', kind: 'page', title: 'Needs attention', context: 'Approvals, blocked requests and problems', keywords: 'inbox waiting pending review approvals blocked expiring failed', href: attentionHref()},
    {id: 'page:sandboxes', kind: 'page', title: 'Sandboxes', context: 'Coding sandboxes and the Orca runtime', keywords: 'list workspaces', href: sandboxesHref()},
    {id: 'page:projects', kind: 'page', title: 'Projects', context: 'Project sandboxes', keywords: 'list workspaces repositories', href: projectsHref()},
  ];
  if (src.runs) out.push({id: 'page:runs', kind: 'page', title: 'Runs', context: 'Orchestration runs from the factory', keywords: 'factory reports gates', href: '#/runs'});
  if (src.orca) out.push({id: 'page:orca', kind: 'page', title: 'Orca projects', context: 'Repositories in the Orca runtime', keywords: 'orca publish repositories', href: orcaHref()});
  out.push({id: 'action:new', kind: 'action', title: 'New sandbox', context: 'Create a project sandbox from a repository', keywords: 'create add start project', href: newSandboxHref()});
  if (src.orca) out.push({id: 'action:orca-publish', kind: 'action', title: 'Publish a repository to Orca', context: 'Opens the Orca projects form', keywords: 'add orca project repository', href: orcaHref()});
  if (src.sandboxControl) {
    out.push({id: 'action:show-waiting', kind: 'action', title: 'Show sandboxes waiting on you', context: 'Home, filtered', keywords: 'pending review approvals requests filter', href: homeHref() + '?show=waiting'});
    out.push({id: 'action:show-open', kind: 'action', title: 'Show sandboxes open to the web', context: 'Home, filtered', keywords: 'egress open learning internet exposed filter', href: homeHref() + '?show=open'});
  }
  for (const s of src.sandboxes) {
    const key = 's:' + s.id;
    out.push({id: 'sandbox:' + s.id, kind: 'sandbox', title: s.id, context: withLevel(s.kind, s.level), keywords: levelWords(s.level), href: sandboxHref(s.id), workspace: key});
    for (const [section, label] of sectionsFor('sandbox', src.runs)) {
      if (section === 'overview') continue;
      out.push({id: `section:${key}:${section}`, kind: 'section', title: label, context: s.id, keywords: SECTION_KEYWORDS[section], href: sandboxHref(s.id, section), workspace: key});
    }
  }
  for (const p of src.projects) {
    const key = 'p:' + p.name;
    out.push({id: 'project:' + p.name, kind: 'project', title: p.name, context: withLevel(p.repository || 'Project sandbox', p.level), keywords: levelWords(p.level), href: projectHref(p.name), workspace: key});
    for (const [section, label] of sectionsFor('project', src.runs)) {
      if (section === 'overview') continue;
      out.push({id: `section:${key}:${section}`, kind: 'section', title: label, context: p.name, keywords: SECTION_KEYWORDS[section], href: projectHref(p.name, section), workspace: key});
    }
  }
  return out;
}

const normalize = (text: string) => text.toLowerCase().normalize('NFKD').replace(/[\u0300-\u036f]/g, '');
const words = (text: string) => text.split(/[\s\-_./:&]+/).filter(Boolean);

// How well one query token matches one field: 0 for no match. A whole word
// beats a word start, which beats a substring, which beats letters in order.
export function tokenScore(token: string, field: string): number {
  if (!token || !field) return 0;
  const f = normalize(field);
  const ws = words(f);
  if (ws.includes(token) || f === token) return 8;
  if (ws.some(w => w.startsWith(token)) || f.startsWith(token)) return 6;
  // Inside a word only for longer tokens: "lan" should not find "glance".
  if (token.length >= 4 && f.includes(token)) return 4;
  // Letters in order ("lnd" finds "lan devices"), only when they stay close together.
  if (token.length < 4) return 0;
  let at = -1, first = -1;
  for (const ch of token) {
    at = f.indexOf(ch, at + 1);
    if (at < 0) return 0;
    if (first < 0) first = at;
  }
  return at - first + 1 <= token.length * 2.5 ? 2 : 0;
}

const FIELD_WEIGHT = {title: 3, context: 2, keywords: 1, kind: 1};

// Scores one command against a query; null when any token finds nothing.
export function scoreCommand(query: string, command: Command, current?: string): number | null {
  const tokens = words(normalize(query));
  if (!tokens.length) return 0;
  const fields: [string, number][] = [[command.title, FIELD_WEIGHT.title], [command.context || '', FIELD_WEIGHT.context], [command.keywords || '', FIELD_WEIGHT.keywords], [KIND_LABEL[command.kind], FIELD_WEIGHT.kind]];
  let total = 0;
  for (const token of tokens) {
    let best = 0;
    for (const [field, weight] of fields) best = Math.max(best, tokenScore(token, field) * weight);
    if (!best) return null;
    total += best;
  }
  // A typed hyphenated name ("orca-pilot") counts as one exact hit.
  const whole = normalize(query.trim());
  if (normalize(command.title) === whole) total += 20;
  else if (normalize(command.title).startsWith(whole)) total += 6;
  if (current && command.workspace === current) total += 3;
  // Workspaces and pages before their sections at an equal score.
  if (command.kind === 'section') total -= 1;
  return total;
}

export interface SearchOptions { current?: string; recent?: string[]; limit?: number }

// The entries to show for a query. With no query: recent picks, then the
// current workspace's sections, then pages, actions and workspaces.
export function searchCommands(query: string, commands: Command[], options: SearchOptions = {}): Command[] {
  const limit = options.limit ?? 50;
  if (!query.trim()) {
    const byId = new Map(commands.map(c => [c.id, c]));
    const recent = (options.recent || []).map(id => byId.get(id)).filter((c): c is Command => Boolean(c));
    const here = commands.filter(c => c.kind === 'section' && options.current && c.workspace === options.current);
    const rest = commands.filter(c => c.kind !== 'section');
    const out: Command[] = [];
    for (const c of [...recent, ...here, ...rest]) if (!out.includes(c)) out.push(c);
    return out.slice(0, limit);
  }
  const order: Record<CommandKind, number> = {page: 0, sandbox: 1, project: 1, action: 2, section: 3};
  return commands
    .map((command, index) => ({command, index, score: scoreCommand(query, command, options.current)}))
    .filter((r): r is {command: Command; index: number; score: number} => r.score !== null)
    .sort((a, b) => b.score - a.score || order[a.command.kind] - order[b.command.kind] || a.command.title.length - b.command.title.length || a.index - b.index)
    .slice(0, limit)
    .map(r => r.command);
}

// Recent picks, newest first, without repeats.
export const RECENT_LIMIT = 5;
export function pushRecent(list: string[], id: string, limit = RECENT_LIMIT): string[] {
  return [id, ...list.filter(item => item !== id)].slice(0, limit);
}
export function parseRecent(raw: string | null): string[] {
  try {
    const value = JSON.parse(raw || '[]');
    return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string').slice(0, RECENT_LIMIT) : [];
  } catch { return []; }
}

// The shortcut label for this platform.
export const shortcutLabel = (platform: string) => /mac|iphone|ipad/i.test(platform) ? '⌘K' : 'Ctrl K';
// True for Ctrl-K, or Cmd-K on a Mac, with no other modifier.
export const isPaletteShortcut = (e: {key: string; ctrlKey: boolean; metaKey: boolean; altKey: boolean; shiftKey: boolean}) =>
  (e.key === 'k' || e.key === 'K') && (e.ctrlKey || e.metaKey) && !(e.ctrlKey && e.metaKey) && !e.altKey && !e.shiftKey;
