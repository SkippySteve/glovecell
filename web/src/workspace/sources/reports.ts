// Agent blocked reports (Overview "Needs a decision"): data source and actions.
// Today: sandbox-control GET /api/reports and POST /dismiss-report. Report text
// is written by the agent and untrusted; the UI renders it as plain text.
import {sandboxReports, type AgentReport} from '../../api';
import type {LanView} from './lan';

export type {AgentReport};
export const loadReports = (workspace: string) => sandboxReports(workspace);
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
