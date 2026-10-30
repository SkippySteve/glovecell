// Previews & dev links section: data source.
// Today: sandbox-control GET /api/devlinks and GET /api/previews; decisions on
// POST /devlink-decision and POST /decision (made by the existing DevLinks and
// Previews components, unchanged).
import {sandboxDevLinks, sandboxPreviews, type DevLink, type Preview} from '../../api';
import type {TagVariant} from '../../ui';

export const loadDevLinks = (workspace: string) => sandboxDevLinks(workspace);
export const loadPreviews = (workspace: string, inactive: boolean) => sandboxPreviews(workspace, inactive);

export interface LiveLink { name: string; host: string; url: string; expires: number }
// Approved, reachable dev links with their approval deadline (epoch ms).
export const liveLinks = (links: DevLink[] | null): LiveLink[] => (links || []).filter(l => l.Live).map(l => ({name: l.Name, host: l.Host, url: l.URL, expires: (l.Expires || 0) * 1000}));
export const pendingLinks = (links: DevLink[] | null) => (links || []).filter(l => l.State === 'Pending');

// One row of the "every link in this sandbox" list: a dev link or a detected
// server, with a status the operator can act on. Built only from what
// sandbox-control reports; nothing here probes the app itself.
//   pending   waits on a scan, the operator or the agent (Pending, Scanning, Observing, Needs network binding)
//   serving   approved and currently answering through sandbox-control
//   approved  approved, but sandbox-control does not serve it right now
//   stale     was approved: the app stopped, the listener went quiet or the approval expired
//   inactive  denied, revoked or failed to scan
export type LinkStatus = 'pending' | 'serving' | 'approved' | 'stale' | 'inactive';
export interface LinkUrl { label: string; url: string }
export interface LinkRow {
  key: string;
  kind: 'devlink' | 'server';
  name: string;
  where: string;          // project / worktree · port
  status: LinkStatus;
  label: string;          // the tag text
  variant: TagVariant;
  note: string;           // one plain sentence: why this status, what happens next
  urls: LinkUrl[];        // copyable addresses; the first is the one "Open" uses
  open: string;           // URL for "Open in new tab"; empty when it would not show the app
  checkedLabel: string;   // what the time means: "Scanned", "Seen" or "Requested"
  checked: number;        // epoch ms of that event, 0 when unknown
  expires: number;        // approval deadline, epoch ms, 0 when none
  target: string;         // DOM id of the detailed card below the list
}

// A detected server's approval ends when its listener has not been observed
// for 15 s (sandbox-control README). The list is fetched every 30 s, so a
// listener not seen for two minutes is reported as stale rather than serving.
export const SERVER_STALE_MS = 2 * 60 * 1000;

export const devlinkTarget = (name: string) => 'devlink-' + name;
export const serverTarget = (id: string) => 'server-' + id;

const ms = (unix: number) => (unix > 0 ? unix * 1000 : 0);
const where = (project: string, worktree: string, port: number) => [project && project !== 'unknown' ? `${project} / ${worktree}` : '', `port ${port}`].filter(Boolean).join(' · ');

export function devlinkRow(l: DevLink): LinkRow {
  const finished = ms(l.Scan?.Finished || 0), started = ms(l.Scan?.Started || 0);
  const base = {
    key: 'd:' + l.Name, kind: 'devlink' as const, name: l.Host || l.Name, where: where(l.Project, l.Worktree, l.Port),
    urls: l.URL ? [{label: 'Dev link', url: l.URL}] : [], open: '', expires: ms(l.Expires),
    checkedLabel: finished ? 'Scanned' : started ? 'Scan started' : 'Requested', checked: finished || started || ms(l.Requested), target: devlinkTarget(l.Name),
  };
  if (l.Live) return {...base, status: 'serving', label: 'Serving', variant: 'success', open: l.URL, note: 'Opens on its verification card first.'};
  switch (l.State) {
    case 'Pending': {
      const c = l.Counts;
      const found = c ? [c.critical ? `${c.critical} critical` : '', c.high ? `${c.high} high` : '', c.secrets ? `${c.secrets} possible secrets` : ''].filter(Boolean).join(', ') : '';
      return {...base, status: 'pending', label: 'Pending approval', variant: 'warning', note: 'Scan complete' + (found ? `: ${found}.` : '; nothing critical or high.') + ' Review the build below.'};
    }
    case 'Scanning': return {...base, status: 'pending', label: 'Scanning', variant: 'info', note: 'Checking dependencies, vulnerabilities and secrets; usually a minute or two.'};
    case 'Approved': return {...base, status: 'approved', label: 'Approved', variant: 'info', note: 'Approved, but sandbox-control is not serving this build right now.'};
    case 'Offline': return {...base, status: 'stale', label: 'Offline', variant: 'neutral', note: 'The app stopped listening. The same build becomes live again when it restarts; a changed build needs approval.'};
    case 'Expired': return {...base, status: 'stale', label: 'Expired', variant: 'neutral', note: 'The approval ran out. The agent has to publish the link again.'};
    case 'Scan failed': return {...base, status: 'inactive', label: 'Scan failed', variant: 'danger', note: l.Scan?.Error ? 'Scan failed: ' + l.Scan.Error : 'The scan failed; the agent can publish again.'};
    case 'Denied': return {...base, status: 'inactive', label: 'Denied', variant: 'danger', note: 'This build was denied. A changed build is scanned again.'};
    case 'Revoked': return {...base, status: 'inactive', label: 'Revoked', variant: 'danger', note: 'Approval withdrawn.'};
    default: return {...base, status: 'pending', label: l.State || 'Unknown', variant: 'neutral', note: ''};
  }
}

export function serverRow(p: Preview, now: number): LinkRow {
  const urls: LinkUrl[] = [];
  if (p.state === 'Approved' && p.tailscale && p.urls?.tailnet) urls.push({label: 'Tailscale', url: p.urls.tailnet});
  if (p.state === 'Approved' && p.lan && p.urls?.lan) urls.push({label: 'LAN', url: p.urls.lan});
  const seen = ms(p.seen);
  const base = {
    key: 's:' + p.id, kind: 'server' as const, name: p.proposedName || 'Unnamed server', where: where(p.listener.project, p.listener.worktree, p.listener.port),
    urls, open: '', expires: 0, checkedLabel: 'Seen', checked: seen, target: serverTarget(p.id),
  };
  switch (p.state) {
    case 'Approved':
      if (seen && now - seen <= SERVER_STALE_MS) return {...base, status: 'serving', label: 'Serving', variant: 'success', open: urls[0]?.url || '', note: urls.length ? `Reachable on ${urls.map(u => u.label).join(' and ')}.` : 'Approved without a network channel.'};
      return {...base, status: 'stale', label: 'Not seen', variant: 'neutral', note: 'Approved, but the listener has not been observed recently. The approval ends when the process stops.'};
    case 'Pending': return {...base, status: 'pending', label: 'Pending approval', variant: 'warning', note: p.listener.loopback ? 'Bound to loopback; the agent must bind a sandbox interface first.' : 'Choose the paths and channels below, then approve.'};
    case 'Needs network binding': return {...base, status: 'pending', label: 'Needs network binding', variant: 'warning', note: 'Bound to loopback only. Ask the agent to bind 0.0.0.0.'};
    case 'Observing': return {...base, status: 'pending', label: 'Observing', variant: 'info', note: 'Seen once; a request appears after the second observation.'};
    case 'Expired': return {...base, status: 'stale', label: 'Expired', variant: 'neutral', note: 'The process stopped or was replaced.'};
    case 'Denied': return {...base, status: 'inactive', label: 'Denied', variant: 'danger', note: 'Repeated requests from this listener are suppressed.'};
    case 'Revoked': return {...base, status: 'inactive', label: 'Revoked', variant: 'danger', note: 'Approval withdrawn.'};
    default: return {...base, status: 'pending', label: p.state || 'Unknown', variant: 'neutral', note: ''};
  }
}

const ORDER: Record<LinkStatus, number> = {pending: 0, serving: 1, approved: 2, stale: 3, inactive: 4};

// Every dev link and detected server of a sandbox, what waits on the operator
// first, then what can be opened.
export function linkRows(links: DevLink[] | null, servers: Preview[] | null, now: number): LinkRow[] {
  const rows = [...(links || []).map(devlinkRow), ...(servers || []).map(p => serverRow(p, now))];
  return rows.sort((a, b) => ORDER[a.status] - ORDER[b.status] || a.name.localeCompare(b.name) || a.key.localeCompare(b.key));
}

export function linkCounts(rows: LinkRow[]): Record<LinkStatus, number> {
  const out: Record<LinkStatus, number> = {pending: 0, serving: 0, approved: 0, stale: 0, inactive: 0};
  for (const r of rows) out[r.status]++;
  return out;
}

// Why "Open" is unavailable for a row, in one short phrase (empty when it opens).
export function openBlocked(r: LinkRow): string {
  if (r.open) return '';
  if (r.status === 'pending') return 'Opens after approval';
  if (r.status === 'approved') return 'Not serving right now';
  if (r.status === 'stale') return r.kind === 'devlink' && r.label === 'Expired' ? 'Approval expired' : 'App not running';
  return 'Not approved';
}

// "Open browser session": an in-console view of the running page. No backend
// for it exists yet, so the entry is always unavailable and says why.
// Browser sessions (agent-project-controller PR #76) start a short-lived
// browser for an agent in a project sandbox; they do not cover sandbox-control
// sandboxes, and nothing streams a page to the console.
export interface BrowserEntry { available: false; reason: string; detail: string }
export function browserSessionEntry(kind: 'sandbox' | 'project'): BrowserEntry {
  if (kind === 'project') return {available: false, reason: 'Waiting on browser sessions (PR #76)',
    detail: 'Browser sessions are in review. Once deployed they let this sandbox\'s agent drive a short-lived browser; showing that page here is a later step.'};
  return {available: false, reason: 'Not available for this sandbox',
    detail: 'Browser sessions (PR #76, in review) are planned for project sandboxes, for agents to use. Until the console can show a page, open a serving link in a new tab.'};
}
