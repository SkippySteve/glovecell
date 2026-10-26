import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import {describe, expect, it} from 'vitest';
import type {AgentReport, DevLink, Observation, Revision} from '../../api';
import {parse} from '../../router';
import {toClusterView} from './credentials';
import {mergeHistory} from './history';
import {decideRequest, paginate, webAccess} from './internet';
import {chosenPorts, lanExtend, lanForget, lanOpen, lanRestrict, portChips, toLanView, windowLabel, type LanApi} from './lan';
import {packageRequests} from './packages';
import {reportLanHost} from './reports';

// The /api/lan sample body. Its keys must be exactly the contract's
// (sandbox-control lan_test.go TestLANAPIContractFieldNames).
const fixture = JSON.parse(readFileSync(join(__dirname, '../../../fixtures/lan.json'), 'utf8')) as LanApi;
const keys = (v: unknown) => Object.keys(v as object).sort();

describe('GET /api/lan contract', () => {
  it('uses exactly the field names sandbox-control serves', () => {
    expect(keys(fixture)).toEqual(['enabled', 'error', 'history', 'hosts', 'limits', 'podUid', 'reason', 'running', 'windows', 'workspace']);
    expect(keys(fixture.limits)).toEqual(['grantsPerWorkspace', 'portsPerGrant']);
    for (const h of fixture.hosts) expect(keys(h)).toEqual(['address', 'all', 'grant', 'learned', 'learnedGrant', 'listed', 'ports', 'suggestedAllowLine']);
    expect(keys(fixture.hosts[1].grant)).toEqual(['deadline', 'mode', 'openedAt', 'openedBy', 'ports', 'reason', 'state']);
    expect(keys(fixture.hosts[0].learned[0])).toEqual(['count', 'firstSeen', 'lastSeen', 'port', 'verdict']);
    expect(keys(fixture.history[0])).toEqual(['at', 'by', 'deadline', 'detail', 'event', 'host', 'ports']);
  });

  it('maps every contract field into the section view', () => {
    const v = toLanView(fixture);
    expect(v).toMatchObject({workspace: 'coding-a', enabled: true, reason: '', error: '', running: true, podUid: '3f1c0d2e-0000-4000-8000-000000000001', windows: [900, 3600, 14400], grantLimit: 8, portLimit: 32, open: 2, applying: true});
    const [all, explicit, unlisted] = v.hosts;
    expect(all).toMatchObject({address: '192.168.1.50', listed: true, all: true, ports: [], grant: null, learnedGrant: [1337, 50000, 50013], suggestedAllowLine: ''});
    expect(all.learned[0]).toEqual({port: 1337, used: true, count: 14, firstSeen: Date.parse('2030-01-01T10:00:00Z'), lastSeen: Date.parse('2030-01-02T10:00:00Z')});
    expect(all.learned[2].used).toBe(false);
    expect(explicit.grant).toEqual({all: false, ports: [22], openedBy: 'operator', openedAt: Date.parse('2030-01-02T11:00:00Z'), deadline: Date.parse('2030-01-02T12:00:00Z'), state: 'Open', reason: ''});
    expect(explicit.suggestedAllowLine).toBe('192.168.1.60: 22, 2122, 8080');
    expect(unlisted).toMatchObject({listed: false, grant: {all: true, ports: [], state: 'Restricting', reason: 'unlisted'}});
    expect(v.history[0]).toEqual({at: Date.parse('2030-01-02T11:30:00Z'), event: 'opened', host: '192.168.1.70', ports: 'all', by: 'operator', deadline: Date.parse('2030-01-02T11:45:00Z'), detail: ''});
    expect(v.history[2].deadline).toBe(0);
  });

  it('degrades instead of failing on missing or malformed fields', () => {
    const v = toLanView({hosts: [{address: '192.168.1.5', grant: {state: 'Weird', ports: [22, 'x', 70000]}, learned: null}, {nope: true}], history: 'bad'});
    expect(v).toMatchObject({enabled: false, running: false, podUid: '', windows: [900, 3600, 14400], grantLimit: 8, portLimit: 32, history: [], open: 1});
    expect(v.hosts).toHaveLength(1);
    expect(v.hosts[0].grant).toMatchObject({state: 'Opening', ports: [22], deadline: 0});
    expect(toLanView(null).hosts).toEqual([]);
  });
});

describe('LAN actions', () => {
  it('posts the existing /egress form fields', () => {
    expect(lanOpen('192.168.1.60', 'choose', [22, 2122], 3600, 'pod')).toEqual({path: '/egress', fields: {action: 'lan-open', 'lan.address': '192.168.1.60', 'lan.port': ['22', '2122'], duration: '3600', pod_uid: 'pod'}});
    expect(lanOpen('192.168.1.50', 'all', [], 900, 'pod').fields).toEqual({action: 'lan-open-all', 'lan.address': '192.168.1.50', duration: '900', pod_uid: 'pod'});
    expect(lanOpen('192.168.1.50', 'learned', [1], 14400, 'pod').fields.action).toBe('lan-open-learned');
    expect(lanExtend('192.168.1.60', 900, 'pod').fields).toEqual({action: 'lan-extend', 'lan.address': '192.168.1.60', duration: '900', pod_uid: 'pod'});
    expect(lanRestrict('192.168.1.60', 'pod').fields).toEqual({action: 'lan-restrict', 'lan.address': '192.168.1.60', pod_uid: 'pod'});
    expect(lanForget('192.168.1.50').fields).toEqual({action: 'lan-forget', 'lan.address': '192.168.1.50'});
  });
  it('validates chosen ports like the classic form', () => {
    expect(chosenPorts([2122], '22, 1337 22', 32)).toEqual([22, 1337, 2122]);
    expect(chosenPorts([], '', 32)).toMatch(/at least one/);
    expect(chosenPorts([], '20-25', 32)).toMatch(/ranges/);
    expect(chosenPorts([], '0', 32)).toMatch(/outside/);
    expect(chosenPorts([], 'ftp', 32)).toMatch(/not a port/);
    expect(chosenPorts([1, 2, 3], '4', 3)).toMatch(/at most 3/);
  });
  it('offers listed ports on explicit hosts and learned ports on all-port hosts', () => {
    const [all, explicit] = toLanView(fixture).hosts;
    expect(portChips(all)).toEqual([{port: 1337, checked: true}, {port: 50000, checked: true}, {port: 50013, checked: true}]);
    expect(portChips(explicit)).toEqual([{port: 22, checked: true}, {port: 2122, checked: true}]);
    expect(portChips({...explicit, learnedGrant: [22]})).toEqual([{port: 22, checked: true}, {port: 2122, checked: false}]);
  });
  it('labels windows', () => {
    expect([900, 3600, 14400].map(windowLabel)).toEqual(['15 min', '1 hour', '4 hours']);
  });
});

const report = (resource: string): AgentReport => ({id: 1, kind: 'other', resource, reason: 'x', count: 1, first: '', last: ''});

describe('blocked reports', () => {
  it('links a report to an allowlisted LAN host by its parsed address only', () => {
    const lan = toLanView(fixture);
    expect(reportLanHost(report('TCP 192.168.1.50:1337 times out'), lan)).toEqual({address: '192.168.1.50', open: false});
    expect(reportLanHost(report('192.168.1.60:22'), lan)).toEqual({address: '192.168.1.60', open: true});
    // Not allowlisted (listed false), public, malformed, or no LAN data.
    expect(reportLanHost(report('192.168.1.70'), lan)).toBeUndefined();
    expect(reportLanHost(report('8.8.8.8 and 192.168.001.50'), lan)).toBeUndefined();
    expect(reportLanHost(report('192.168.1.50'), null)).toBeUndefined();
  });
});

const obs = (id: string, extra: Partial<Observation> = {}): Observation => ({host: 'registry.npmjs.org', method: 'GET', path: '/x', scheme: 'https', port: 443, observation: id, ...extra});

describe('Internet and packages', () => {
  it('pages over the fetched window', () => {
    const items = Array.from({length: 23}, (_, i) => i);
    expect(paginate(items, 0)).toMatchObject({rows: items.slice(0, 10), page: 0, pages: 3, from: 1, to: 10});
    expect(paginate(items, 9)).toMatchObject({page: 2, from: 21, to: 23});
    expect(paginate([], 0)).toMatchObject({rows: [], pages: 1, from: 0, to: 0});
  });
  it('decides grouped endpoints and single fingerprints', () => {
    expect(decideRequest(obs('a', {fingerprints: 3}), true).fields).toEqual({action: 'approve-endpoint', observation: 'a'});
    expect(decideRequest(obs('b'), false).fields).toEqual({action: 'revoke-learning', observation: 'b'});
  });
  it('reads web access only for learning workspaces', () => {
    const base = {learningEgress: true, learning: {phase: 'Open' as const, minutes: 60, started: 100, expires: 200}};
    expect(webAccess(base as never)).toEqual({controllable: true, phase: 'Open', minutes: 60, started: 100000, expires: 200000});
    expect(webAccess({...base, learning: {...base.learning, phase: 'Locked'}} as never)?.expires).toBe(0);
    expect(webAccess({learningEgress: false} as never)).toBeNull();
  });
  it('lists registry downloads among the requests', () => {
    const r = {pending: {total: 2, items: [obs('p', {package: {ecosystem: 'npm', name: 'left-pad', version: '1.3.0'}}), obs('q')], truncated: false}, approved: {total: 0, items: [], truncated: false}};
    expect(packageRequests(r).pending.map(p => [p.ecosystem, p.name, p.version])).toEqual([['npm', 'left-pad', '1.3.0']]);
    expect(packageRequests(null)).toEqual({pending: [], approved: []});
  });
});

describe('history and credentials', () => {
  it('merges LAN, dev link, revision and report events newest first', () => {
    const lan = toLanView(fixture);
    const devlinks = [{Name: 'docs', Events: [{Action: 'approved', Actor: 'operator', Detail: 'build abc', At: Date.parse('2030-01-02T11:15:00Z') / 1000}]}] as unknown as DevLink[];
    const revisions: Revision[] = [{id: 4, status: 'Active', actor: 'operator', at: Date.parse('2030-01-01T00:00:00Z') / 1000}];
    const reports = [{...report('192.168.1.50:1337‮'), last: '2030-01-02T11:20:00Z', count: 2}];
    const events = mergeHistory({lan, devlinks, revisions, reports});
    expect(events.map(e => e.type)).toEqual(['lan', 'report', 'devlink', 'lan', 'lan', 'policy']);
    expect(events[0].title).toBe('LAN access opened · 192.168.1.70');
    expect(events[1].detail).toBe('192.168.1.50:1337 · reported 2 times');
  });
  it('reads the cluster identity and grants', () => {
    const v = toClusterView({Identity: {Namespace: 'agent-workspace-a', Name: 'agent'}, Grants: [{Namespace: 'app', Capability: 'debug', Reason: 'crash', GrantedBy: 'operator', Expires: '2030-01-01T00:00:00Z', ThisSandbox: true}, null]});
    expect(v).toEqual({identity: {namespace: 'agent-workspace-a', name: 'agent'}, grants: [{namespace: 'app', capability: 'debug', reason: 'crash', grantedBy: 'operator', expires: Date.parse('2030-01-01T00:00:00Z'), thisSandbox: true}]});
    expect(toClusterView({Identity: null}).identity).toBeNull();
  });
});

describe('routes', () => {
  it('maps sections and the earlier tab names', () => {
    expect(parse('#/sandboxes/a/lan')).toEqual({view: 'sandbox', id: 'a', section: 'lan'});
    expect(parse('#/sandboxes/a/access')).toEqual({view: 'sandbox', id: 'a', section: 'internet'});
    expect(parse('#/sandboxes/a/devlinks')).toMatchObject({section: 'previews'});
    expect(parse('#/sandboxes/a/activity')).toMatchObject({section: 'history'});
    expect(parse('#/projects/p/repositories')).toEqual({view: 'project', name: 'p', section: 'credentials'});
    expect(parse('#/projects/p/bogus')).toMatchObject({section: 'overview'});
    expect(parse('#/sandboxes/%E0%A4%A')).toMatchObject({view: 'sandbox'});
  });
});
