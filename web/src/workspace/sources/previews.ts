// Previews & dev links section: data source.
// Today: sandbox-control GET /api/devlinks and GET /api/previews; decisions on
// POST /devlink-decision and POST /decision (made by the existing DevLinks and
// Previews components, unchanged).
import {sandboxDevLinks, sandboxPreviews, type DevLink} from '../../api';

export const loadDevLinks = (workspace: string) => sandboxDevLinks(workspace);
export const loadPreviews = (workspace: string, inactive: boolean) => sandboxPreviews(workspace, inactive);

export interface LiveLink { name: string; host: string; url: string; expires: number }
// Approved, reachable dev links with their approval deadline (epoch ms).
export const liveLinks = (links: DevLink[] | null): LiveLink[] => (links || []).filter(l => l.Live).map(l => ({name: l.Name, host: l.Host, url: l.URL, expires: (l.Expires || 0) * 1000}));
export const pendingLinks = (links: DevLink[] | null) => (links || []).filter(l => l.State === 'Pending');
