import {describe, expect, it} from 'vitest';
import type {AgentReport, AllowOptions, Observation, WebRule} from '../../api';
import {hashQuery, parse, withQuery} from '../../router';
import {addRule, draftFor, draftRisks, needsConfirm, pageKeeping, pageOf, pageSize, rulesBySource, type RequestList} from './internet';
import {reportRequests} from './reports';

const item = (i: number): Observation => ({host: 'proxy.golang.org', method: 'GET', path: `/m/@v/v${i}.zip`, scheme: 'https', port: 443, observation: String(i)});
const allow: AllowOptions = {host: 'api.github.com', method: 'GET', path: '/repos/steve/tool/releases', pattern: '/repos/steve/tool/**', wildcard: '*.github.com', wildcardReason: '', githubOwner: 'steve', githubRepo: 'tool', sharedHost: false, passthrough: true};

describe('request list paging', () => {
  it('uses the server page when sandbox-control pages', () => {
    const l: RequestList = {total: 141, items: Array.from({length: 10}, (_, i) => item(i)), truncated: false, paging: {page: 10, size: 10, pages: 15, first: 91, last: 100}};
    expect(pageOf(l, 10, 10)).toMatchObject({page: 10, pages: 15, from: 91, to: 100, total: 141});
  });
  it('pages locally on older releases, clamped to the ends', () => {
    const l: RequestList = {total: 2673, items: Array.from({length: 141}, (_, i) => item(i)), truncated: true};
    expect(pageOf(l, 1, 25)).toMatchObject({page: 1, pages: 6, from: 1, to: 25, total: 141});
    expect(pageOf(l, 99, 25)).toMatchObject({page: 6, pages: 6, from: 126, to: 141});
    expect(pageOf(l, 0, 25)).toMatchObject({page: 1, from: 1});
    expect(pageOf({total: 0, items: [], truncated: false}, 3, 25)).toMatchObject({page: 1, pages: 1, from: 0, to: 0});
  });
  it('accepts only 10, 25, 50 and 100 per page and keeps the first item when the size changes', () => {
    expect([10, 25, 50, 100, 7, 1000, 'x', null].map(pageSize)).toEqual([10, 25, 50, 100, 25, 25, 25, 25]);
    expect(pageKeeping(91, 50)).toBe(2);
    expect(pageKeeping(91, 10)).toBe(10);
    expect(pageKeeping(0, 25)).toBe(1);
  });
  it('keeps view state in the hash query without changing the route', () => {
    const href = withQuery('#/sandboxes/orca-pilot/internet', {q: 'github.com/x', page: 3, size: 50, empty: ''});
    expect(href).toBe('#/sandboxes/orca-pilot/internet?q=github.com%2Fx&page=3&size=50');
    expect(parse(href)).toEqual({view: 'sandbox', id: 'orca-pilot', section: 'internet'});
    expect(hashQuery(href).get('q')).toBe('github.com/x');
  });
});

describe('web rule drafts', () => {
  it('start narrow: the parent path pattern, the request method, one day', () => {
    expect(draftFor(allow)).toMatchObject({kind: 'path', host: 'api.github.com', path: '/repos/steve/tool/**', methods: ['GET'], anyMethod: false, passthrough: false, expires: '1d', github: 'steve/tool'});
  });
  it('post the fields sandbox-control reads', () => {
    expect(addRule(draftFor(allow)).fields).toEqual({action: 'web-rule-add', 'rule.kind': 'path', 'rule.expires': '1d', 'rule.host': 'api.github.com', 'rule.path': '/repos/steve/tool/**', 'rule.method': ['GET']});
    const wildcard = {...draftFor(allow), kind: 'wildcard' as const, apex: true, anyMethod: true, confirm: true, expires: '7d' as const};
    expect(addRule(wildcard).fields).toEqual({action: 'web-rule-add', 'rule.kind': 'wildcard', 'rule.expires': '7d', 'rule.wildcard': '*.github.com', 'rule.apex': 'on', 'rule.any_method': 'on', 'rule.confirm': 'on'});
    const repo = {...draftFor(allow), kind: 'github-repo' as const, write: true, confirm: true};
    expect(addRule(repo).fields).toEqual({action: 'web-rule-add', 'rule.kind': 'github-repo', 'rule.expires': '1d', 'rule.github': 'steve/tool', 'rule.write': 'on', 'rule.confirm': 'on'});
    const pass = {...draftFor(allow), kind: 'domain' as const, passthrough: true, confirm: true};
    expect(addRule(pass).fields).toMatchObject({'rule.kind': 'domain', 'rule.host': 'api.github.com', 'rule.passthrough': 'on'});
    expect(addRule({...pass, kind: 'path'}).fields['rule.passthrough']).toBeUndefined();
  });
  it('ask for confirmation and explain the risk of broad choices', () => {
    const d = draftFor(allow);
    expect(needsConfirm(d)).toBe(false);
    expect(needsConfirm({...d, kind: 'domain', anyMethod: true})).toBe(true);
    expect(needsConfirm({...d, kind: 'github-repo', write: true})).toBe(true);
    expect(needsConfirm({...d, kind: 'domain', passthrough: true})).toBe(true);
    expect(draftRisks({...d, kind: 'domain', anyMethod: true}).join(' ')).toContain('upload data there');
    expect(draftRisks({...d, kind: 'github-repo', write: true}).join(' ')).toContain('push to it');
    expect(draftRisks({...d, kind: 'domain', passthrough: true}).join(' ')).toContain('cannot show or limit');
    expect(draftRisks({...d, kind: 'wildcard'}).join(' ')).toContain('not github.com itself');
  });
});

describe('web blocked reports', () => {
  const report = (r: Partial<AgentReport>): AgentReport => ({id: 22, kind: 'web', resource: 'github.com/google/elephant-goldfish-model', reason: '', count: 1, first: '', last: '', ...r});
  it('link to the Internet list with the filter', () => {
    expect(reportRequests(report({requests: {filter: 'google/elephant-goldfish-model', waiting: 1, approved: 0, note: ''}}))).toEqual({filter: 'google/elephant-goldfish-model', waiting: 1, approved: 0, note: ''});
    expect(reportRequests(report({requests: {filter: 'x', waiting: 0, approved: 0, note: 'No request from this sandbox matching “x” was recorded.'}}))?.note).toContain('No request');
  });
  it('ignore other kinds and older releases', () => {
    expect(reportRequests(report({kind: 'package', requests: {filter: 'x', waiting: 0, approved: 0, note: ''}}))).toBeUndefined();
    expect(reportRequests(report({}))).toBeUndefined();
  });
});

describe('web rules by source', () => {
  const rule = (id: string, fromGit: boolean) => ({id, fromGit, source: fromGit ? 'git:reference-sources.yaml#' + id : undefined}) as WebRule;
  const rules = [rule('a', false), rule('cilium', true), rule('uv', true)];
  it('counts and filters rules from Git and rules added here', () => {
    expect(rulesBySource(rules, 'all')).toMatchObject({git: 2, here: 1});
    expect(rulesBySource(rules, 'all').shown).toHaveLength(3);
    expect(rulesBySource(rules, 'git').shown.map(r => r.id)).toEqual(['cilium', 'uv']);
    expect(rulesBySource(rules, 'here').shown.map(r => r.id)).toEqual(['a']);
  });
});
