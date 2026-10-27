import {describe, expect, it} from 'vitest';
import {groupReference, projectOf} from './reference';
import type {HTTPSRule} from './api';

const rules: HTTPSRule[] = [
  {host: '*.cilium.io', methods: ['GET', 'HEAD'], paths: ['/'], source: 'git:reference-sources.yaml#cilium'},
  {host: 'github.com', methods: ['GET', 'HEAD'], paths: ['/cilium/cilium/info/refs'], source: 'git:reference-sources.yaml#cilium'},
  {host: 'docs.astral.sh', methods: ['GET', 'HEAD'], paths: ['/'], source: 'git:reference-sources.yaml#uv'},
];

describe('reference rules', () => {
  it('names the project from the source label', () => {
    expect(projectOf('git:reference-sources.yaml#cilium')).toBe('cilium');
    expect(projectOf('plain')).toBe('plain');
  });
  it('groups by source, sorted by project', () => {
    const groups = groupReference(rules, '');
    expect(groups.map(g => g.project)).toEqual(['cilium', 'uv']);
    expect(groups[0].rules).toHaveLength(2);
  });
  it('filters by project, host or path', () => {
    expect(groupReference(rules, 'UV').map(g => g.project)).toEqual(['uv']);
    expect(groupReference(rules, 'cilium.io').map(g => g.project)).toEqual(['cilium']);
    expect(groupReference(rules, '/cilium/cilium').map(g => g.project)).toEqual(['cilium']);
    expect(groupReference(rules, 'nothing')).toEqual([]);
  });
});
