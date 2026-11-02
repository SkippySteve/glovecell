// Agent blocked reports (Overview "Needs a decision"): data source and actions.
// Today: sandbox-control GET /api/reports and POST /dismiss-report. Report text
// is written by the agent and untrusted; the UI renders it as plain text.
import {sandboxReports, type AgentReport} from '../../api';
import {plainText} from '../../orca';
import type {LanView} from './lan';

export type {AgentReport};
// Report times are RFC 3339 from sandbox-control; anything else is shown as text.
export function reportTime(value: string): string {
  const t = Date.parse(value);
  return Number.isFinite(t) && /^\d{4}-\d{2}-\d{2}T/.test(value) ? new Date(t).toLocaleString([], {month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'}) : plainText(value, 40);
}
export const loadReports = (workspace: string) => sandboxReports(workspace);

// A web report's link to the Internet request list (sandbox-control computes
// the filter from the resource; the agent's text never becomes markup). The
// filter is only a search term, so it is clipped and stripped of controls.
export function reportRequests(r: AgentReport): {filter: string; waiting: number; approved: number; note: string} | undefined {
  if (r.kind !== 'web' || !r.requests) return undefined;
  const filter = plainText(r.requests.filter, 120);
  if (!filter) return undefined;
  return {filter, waiting: r.requests.waiting || 0, approved: r.requests.approved || 0, note: plainText(r.requests.note || '', 400)};
}
export const dismissReport = (r: AgentReport) => ({path: '/dismiss-report', fields: {report: String(r.id), tab: 'activity'}});

const PRIVATE = [[10, 0, 8], [172, 16, 12], [192, 168, 16]] as const;
function privateIPv4(text: string): string | undefined {
  for (const field of text.split(/[^0-9.]+/)) {
    const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(field);
    if (!m) continue;
    const o = m.slice(1).map(Number);
    if (o.some(n => n > 255) || m.slice(1).some(s => s.length > 1 && s.startsWith('0'))) continue;
    const value = ((o[0] << 24) >>> 0) + (o[1] << 16) + (o[2] << 8) + o[3];
    if (PRIVATE.some(([a, b, bits]) => value >>> (32 - bits) === (((a << 24) >>> 0) + (b << 16)) >>> (32 - bits))) return o.join('.');
  }
  return undefined;
}

// A report naming a private IPv4 address that the LAN allowlist lists links to
// the LAN devices section. Only a parsed address is used, never the raw text.
export function reportLanHost(r: AgentReport, lan: LanView | null): {address: string; open: boolean} | undefined {
  const address = privateIPv4(r.resource);
  const host = address && lan?.hosts.find(h => h.address === address && h.listed);
  return host ? {address: host.address, open: Boolean(host.grant)} : undefined;
}
