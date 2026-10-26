// LAN devices section: data source and actions.
//
// Today the data comes from sandbox-control's GET /api/lan (a stable contract:
// fields are added, never renamed or removed; see sandbox-control api.go apiLAN
// and its TestLANAPIContractFieldNames). Actions ride on the existing POST
// /egress route as action=lan-*. The section UI only sees LanView and the
// LanAction descriptors below, so a later backend (Glovecell) replaces this
// module without touching the UI.
import {sandboxGetOptional} from '../../api';

// The wire shape of GET /api/lan, field for field.
export interface LanApiGrant { mode: string; ports: number[]; openedBy: string; openedAt: string; deadline: string; state: string; reason: string }
export interface LanApiLearned { port: number; verdict: string; firstSeen: string; lastSeen: string; count: number }
export interface LanApiHost { address: string; listed: boolean; all: boolean; ports: number[]; grant: LanApiGrant | null; learned: LanApiLearned[]; learnedGrant: number[]; suggestedAllowLine: string }
export interface LanApiEvent { at: string; event: string; host: string; ports: string; by: string; deadline: string; detail: string }
export interface LanApi {
  workspace: string; enabled: boolean; reason: string; error: string; running: boolean; podUid: string;
  windows: number[]; limits: {grantsPerWorkspace: number; portsPerGrant: number};
  hosts: LanApiHost[]; history: LanApiEvent[];
}

// What the section renders. Times are epoch milliseconds, 0 when unset.
export type GrantState = 'Opening' | 'Open' | 'Restricting';
export interface LanGrant { all: boolean; ports: number[]; openedBy: string; openedAt: number; deadline: number; state: GrantState; reason: string }
export interface LanLearned { port: number; used: boolean; count: number; firstSeen: number; lastSeen: number }
export interface LanHost {
  address: string; listed: boolean; all: boolean; ports: number[]; grant: LanGrant | null;
  learned: LanLearned[]; learnedGrant: number[]; suggestedAllowLine: string;
}
export interface LanEvent { at: number; event: string; host: string; ports: string; by: string; deadline: number; detail: string }
export interface LanView {
  workspace: string; enabled: boolean; reason: string; error: string; running: boolean; podUid: string;
  windows: number[]; grantLimit: number; portLimit: number; hosts: LanHost[]; history: LanEvent[];
  // Derived: grants that exist (any state), and whether one is mid-transition.
  open: number; applying: boolean;
}

const str = (v: unknown) => typeof v === 'string' ? v : '';
const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v) ? v : 0;
const list = <T>(v: unknown): T[] => Array.isArray(v) ? v as T[] : [];
const ports = (v: unknown) => list<unknown>(v).filter((p): p is number => Number.isInteger(p) && (p as number) > 0 && (p as number) <= 65535);
export const time = (v: unknown) => { const t = Date.parse(str(v)); return Number.isFinite(t) ? t : 0; };
const STATES: GrantState[] = ['Opening', 'Open', 'Restricting'];

function grant(raw: unknown): LanGrant | null {
  if (!raw || typeof raw !== 'object') return null;
  const g = raw as Partial<LanApiGrant>;
  const state = STATES.find(s => s === g.state) || 'Opening';
  const all = g.mode === 'all';
  return {all, ports: all ? [] : ports(g.ports), openedBy: str(g.openedBy), openedAt: time(g.openedAt), deadline: time(g.deadline), state, reason: str(g.reason)};
}

// toLanView maps one GET /api/lan body. Unknown or missing fields degrade to
// empty values rather than failing, so an older or newer release still renders.
export function toLanView(raw: unknown): LanView {
  const b = (raw && typeof raw === 'object' ? raw : {}) as Partial<LanApi>;
  const hosts: LanHost[] = list<Partial<LanApiHost>>(b.hosts).filter(h => h && typeof h.address === 'string').map(h => ({
    address: str(h.address), listed: h.listed === true, all: h.all === true, ports: ports(h.ports), grant: grant(h.grant),
    learned: list<Partial<LanApiLearned>>(h.learned).filter(l => l && Number.isInteger(l.port)).map(l => ({port: num(l.port), used: l.verdict === 'used', count: num(l.count), firstSeen: time(l.firstSeen), lastSeen: time(l.lastSeen)})),
    learnedGrant: ports(h.learnedGrant), suggestedAllowLine: str(h.suggestedAllowLine),
  }));
  const history = list<Partial<LanApiEvent>>(b.history).filter(e => e && typeof e === 'object').map(e => ({at: time(e.at), event: str(e.event), host: str(e.host), ports: str(e.ports), by: str(e.by), deadline: time(e.deadline), detail: str(e.detail)}));
  const limits = (b.limits && typeof b.limits === 'object' ? b.limits : {}) as Partial<LanApi['limits']>;
  const windows = list<unknown>(b.windows).filter((w): w is number => Number.isInteger(w) && (w as number) > 0);
  return {
    workspace: str(b.workspace), enabled: b.enabled === true, reason: str(b.reason), error: str(b.error),
    running: b.running === true, podUid: str(b.podUid),
    windows: windows.length ? windows : [900, 3600, 14400], grantLimit: num(limits.grantsPerWorkspace) || 8, portLimit: num(limits.portsPerGrant) || 32,
    hosts, history, open: hosts.filter(h => h.grant).length, applying: hosts.some(h => h.grant && h.grant.state !== 'Open'),
  };
}

// Null when the console or sandbox-control release does not serve /api/lan yet.
export async function loadLan(workspace: string): Promise<LanView | null> {
  const raw = await sandboxGetOptional<unknown>('/api/lan', {workspace});
  return raw === null ? null : toLanView(raw);
}

// One POST /egress form for a LAN action. pod_uid binds Open and Extend to the
// pod the operator saw, so a replaced worker is refused rather than granted.
export interface LanAction { path: '/egress'; fields: Record<string, string | string[]> }
const action = (fields: Record<string, string | string[]>): LanAction => ({path: '/egress', fields});
export type OpenMode = 'learned' | 'choose' | 'all';
export const lanOpen = (address: string, mode: OpenMode, chosen: number[], seconds: number, podUid: string) => action({
  action: mode === 'all' ? 'lan-open-all' : mode === 'learned' ? 'lan-open-learned' : 'lan-open',
  'lan.address': address, ...(mode === 'choose' ? {'lan.port': chosen.map(String)} : {}), duration: String(seconds), pod_uid: podUid,
});
export const lanExtend = (address: string, seconds: number, podUid: string) => action({action: 'lan-extend', 'lan.address': address, duration: String(seconds), pod_uid: podUid});
export const lanRestrict = (address: string, podUid: string) => action({action: 'lan-restrict', 'lan.address': address, pod_uid: podUid});
export const lanForget = (address: string) => action({action: 'lan-forget', 'lan.address': address});

// Ports a "Choose ports" draft opens: ticked chips plus typed ports (typing is
// offered only on hosts allowlisted for all ports). Returns an error message
// instead when the draft is not a valid grant.
export function chosenPorts(picked: number[], extra: string, limit: number): number[] | string {
  const fields = extra.split(/[\s,]+/).filter(Boolean);
  for (const f of fields) {
    if (!/^\d{1,5}$/.test(f)) return f.includes('-') || f.includes(':') ? 'List each port; ranges are not accepted.' : `${f} is not a port number.`;
    if (+f < 1 || +f > 65535) return `${f} is outside 1–65535.`;
  }
  const all = [...new Set([...picked, ...fields.map(Number)])].sort((a, b) => a - b);
  if (!all.length) return 'Choose at least one TCP port.';
  if (all.length > limit) return `A grant holds at most ${limit} ports; use All ports.`;
  return all;
}

// The port chips a closed host offers, mirroring the classic page: an explicit
// host offers its listed ports, an all-ports host its learned ports. Chips are
// preselected from the learned grant when there is one, otherwise all of them.
export function portChips(h: LanHost): {port: number; checked: boolean}[] {
  const chips = h.all ? h.learnedGrant : h.ports;
  return chips.map(port => ({port, checked: !h.learnedGrant.length || h.learnedGrant.includes(port)}));
}

export const grantLabel = (g: LanGrant) => 'TCP ' + (g.all ? 'all ports' : g.ports.join(', '));
export const windowLabel = (seconds: number) => seconds % 3600 === 0 ? (seconds === 3600 ? '1 hour' : `${seconds / 3600} hours`) : `${Math.round(seconds / 60)} min`;
