import {describe, expect, it} from 'vitest';
import {isIPv4, parsePorts, portsLabel} from './lan';

describe('LAN host input', () => {
  it('accepts only a canonical IPv4 address', () => {
    expect(isIPv4('192.168.110.2')).toBe(true);
    for (const bad of ['192.168.110.0/24', '0192.168.110.2', '192.168.110', 'devbox.lan', 'fd00::2', '::ffff:192.168.110.2', ' 192.168.110.2', '256.1.1.1']) expect(isIPv4(bad)).toBe(false);
  });
  it('parses an explicit port list', () => {
    expect(parsePorts('8080, 22 443')).toEqual([22, 443, 8080]);
    expect(parsePorts('')).toMatch(/at least one/);
    expect(parsePorts('22-25')).toMatch(/ranges/);
    expect(parsePorts('0')).toMatch(/outside/);
    expect(parsePorts('65536')).toMatch(/outside/);
    expect(parsePorts('22,22')).toMatch(/twice/);
    expect(parsePorts('ssh')).toMatch(/not a port/);
    expect(parsePorts(Array.from({length: 17}, (_, i) => String(i + 1)).join(','))).toMatch(/at most/);
  });
  it('labels ports', () => {
    expect(portsLabel({address: '192.168.110.2', ports: [22, 8080]})).toBe('TCP 22, 8080');
  });
});
