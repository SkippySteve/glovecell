// A blocked report's "Open LAN access" hands its host to the LAN section, which
// preselects it and quotes the report. Kept in memory for one navigation.
import type {AgentReport} from '../api';

export interface LanPrefill { workspace: string; address: string; reason: string }
let pending: LanPrefill | null = null;

export const setLanPrefill = (workspace: string, address: string, report: AgentReport) => { pending = {workspace, address, reason: report.reason}; };
export function takeLanPrefill(workspace: string): LanPrefill | null {
  const p = pending && pending.workspace === workspace ? pending : null;
  pending = null;
  return p;
}
