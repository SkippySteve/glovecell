import {api, type HTTPSRule} from './api';

// Reference rules (README "Reference rules"): the deployment's read-only list
// every project's proxy gets after its own rules, labelled with their source.
export interface ReferenceList { digest: string; rules: HTTPSRule[] }
export interface ReferenceGroup { source: string; project: string; rules: HTTPSRule[] }

export const referenceRules = () => api<ReferenceList>('reference-rules');

// "git:reference-sources.yaml#cilium" -> "cilium".
export const projectOf = (source: string) => source.includes('#') ? source.slice(source.lastIndexOf('#') + 1) : source;

// One group per source, sorted by project; the filter matches the project,
// a host or a path, case-insensitively.
export function groupReference(rules: HTTPSRule[], filter: string): ReferenceGroup[] {
  const q = filter.trim().toLowerCase();
  const groups = new Map<string, ReferenceGroup>();
  for (const rule of rules) {
    const source = rule.source || '';
    const group = groups.get(source) || {source, project: projectOf(source), rules: []};
    group.rules.push(rule);
    groups.set(source, group);
  }
  return [...groups.values()]
    .filter(g => !q || g.project.toLowerCase().includes(q) || g.rules.some(r => r.host.includes(q) || r.paths.some(p => p.toLowerCase().includes(q))))
    .sort((a, b) => a.project.localeCompare(b.project));
}
