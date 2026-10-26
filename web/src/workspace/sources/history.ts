// History section: data source.
//
// There is no single history API today, so this merges what the console can
// read: LAN grant history (GET /api/lan), dev link decisions (GET /api/devlinks
// Events), policy revisions, which include start and stop (GET /api/workspace),
// and open agent reports (GET /api/reports). Web access open, lock and approval
// events have no read API yet. Blocked requests, commands and connections stay
// in the Activity view (Loki and Tempo through GET /api/activity).
import type {AgentReport, DevLink, Revision} from '../../api';
import {plainText, RESOURCE_LIMIT} from '../../orca';
import type {DownloadEvent} from './downloads';
import type {LanView} from './lan';

export type HistoryType = 'lan' | 'devlink' | 'policy' | 'report' | 'download';
export interface HistoryEvent { at: number; type: HistoryType; title: string; detail: string; by: string }
export const HISTORY_FILTERS: [HistoryType | 'all', string][] = [['all', 'All'], ['lan', 'LAN'], ['devlink', 'Dev links'], ['report', 'Reports'], ['download', 'Downloads'], ['policy', 'Policy & lifecycle']];

const LAN_EVENTS: Record<string, string> = {opened: 'LAN access opened', extended: 'LAN access extended', restricted: 'LAN access restricted', expired: 'LAN access expired', applied: 'LAN rule applied', revoked: 'LAN rule removed', 'ports-learned': 'LAN ports learned', 'ports-forgotten': 'Learned LAN ports forgotten', 'worker-restarted': 'Workspace pod restarted', 'controller-restart': 'Grant ended by a sandbox-control restart', 'orphan-removed': 'Orphaned LAN rule removed'};
export const lanEventTitle = (event: string) => LAN_EVENTS[event] || 'LAN ' + event.replace(/-/g, ' ');

const DOWNLOAD_EVENTS: Record<string, string> = {'download-chain-checked': 'Download URL checked', 'download-requested': 'Download started', 'download-done': 'Download finished', 'download-failed': 'Download failed', 'download-changed': 'Download stopped: redirect chain changed', 'download-cancelled': 'Download cancelled'};

export function mergeHistory(src: {lan?: LanView | null; devlinks?: DevLink[] | null; revisions?: Revision[]; reports?: AgentReport[] | null; downloads?: DownloadEvent[] | null}): HistoryEvent[] {
  const out: HistoryEvent[] = [];
  for (const e of src.downloads || []) {
    out.push({at: e.at, type: 'download', title: DOWNLOAD_EVENTS[e.action] || e.action.replace(/-/g, ' '), detail: e.detail, by: e.by});
  }
  for (const e of src.lan?.history || []) {
    out.push({at: e.at, type: 'lan', title: `${lanEventTitle(e.event)} · ${e.host}`, detail: [e.ports ? 'TCP ' + e.ports : '', e.deadline ? 'until ' + new Date(e.deadline).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'}) : '', e.detail].filter(Boolean).join(' · '), by: e.by});
  }
  for (const l of src.devlinks || []) for (const e of l.Events || []) {
    out.push({at: e.At * 1000, type: 'devlink', title: `Dev link ${l.Name}: ${e.Action}`, detail: e.Detail, by: e.Actor});
  }
  for (const r of src.revisions || []) {
    out.push({at: r.at * 1000, type: 'policy', title: `Policy revision ${r.id} · ${r.status}`, detail: r.error || '', by: r.actor || ''});
  }
  for (const r of src.reports || []) {
    const at = Date.parse(r.last);
    out.push({at: Number.isFinite(at) ? at : 0, type: 'report', title: `Agent report (${plainText(r.kind, 40)})`, detail: `${plainText(r.resource, RESOURCE_LIMIT)} · reported ${r.count} time${r.count === 1 ? '' : 's'}`, by: 'agent'});
  }
  return out.sort((a, b) => b.at - a.at);
}
