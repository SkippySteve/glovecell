// Packages section: data source.
//
// Today the only package data the console can read is the registry downloads
// among a learning workspace's web requests (GET /api/egress items carry a
// `package` field). The allowed-package lists and package-policy blocks have no
// read API yet (sandbox-control would need GET /api/packages), so the section
// shows those as not available and links to the classic page.
import type {Observation} from '../../api';
import type {Requests} from './internet';

export interface PackageRequest { ecosystem: string; name: string; version: string; request: Observation }

export const ECOSYSTEMS: Record<string, string> = {npm: 'npm', go: 'Go modules', python: 'Python', pypi: 'Python', rust: 'Rust crates', cargo: 'Rust crates'};
export const ecosystemLabel = (e: string) => ECOSYSTEMS[e.toLowerCase()] || e;

const packages = (items: Observation[]): PackageRequest[] => items.filter(o => o.package?.name).map(o => ({ecosystem: o.package!.ecosystem, name: o.package!.name, version: o.package!.version || '', request: o}));

export function packageRequests(r: Requests | null): {pending: PackageRequest[]; approved: PackageRequest[]} {
  return {pending: packages(r?.pending.items || []), approved: packages(r?.approved.items || [])};
}

// No console API serves the allowed-package lists yet.
export const allowedPackagesAvailable = false;
