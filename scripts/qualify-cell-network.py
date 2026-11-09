#!/usr/bin/env python3
"""Read-only network probes for disposable Cilium cells. See docs/network-qualification.md."""
import argparse
import json
import subprocess
import sys

# Run the same probe in a separately qualified control pod and the cell. Never
# print exception strings, response bodies, environment values or credentials.
PROBE = r'''
import json, os, socket, ssl, struct, sys, urllib.request, urllib.error
p = json.loads(sys.argv[1])
try:
    if p['kind'] == 'tcp':
        with socket.create_connection((p['host'], p['port']), timeout=4): pass
    elif p['kind'] == 'resolve':
        socket.setdefaulttimeout(4)
        if not socket.getaddrinfo(p['host'], 443): raise RuntimeError()
    elif p['kind'] == 'dns':
        ident = os.urandom(2)
        query = ident + struct.pack('!HHHHH', 256, 1, 0, 0, 0)
        query += b''.join(bytes([len(v)]) + v.encode('ascii') for v in p['name'].split('.')) + b'\0\0\1\0\1'
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
            s.settimeout(4)
            s.connect((p['host'], 53))
            s.send(query)
            answer = s.recv(4096)
            if len(answer) < 12 or answer[:2] != ident or not answer[2] & 128 or answer[3] & 15 or struct.unpack('!H', answer[6:8])[0] == 0: raise RuntimeError()
    else:
        proxies = urllib.request.getproxies() if p['via_proxy'] else {}
        if p['via_proxy'] and not proxies.get('https'): raise RuntimeError()
        # An explicit proxy must not be bypassed by no_proxy.
        if p['via_proxy']:
            os.environ.pop('NO_PROXY', None)
            os.environ.pop('no_proxy', None)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler(proxies), urllib.request.HTTPSHandler(context=ssl.create_default_context()))
        with opener.open(p['url'], timeout=6) as response:
            if response.status != 200: raise RuntimeError()
            if p['kind'] == 'doh':
                answer = response.read(4097)
                if response.headers.get_content_type() != 'application/dns-message' or not 12 <= len(answer) <= 4096 or not answer[2] & 128 or answer[3] & 15 or struct.unpack('!H', answer[6:8])[0] == 0: raise RuntimeError()
            else:
                response.read(1)
    print(json.dumps({'reachable': True}))
except urllib.error.HTTPError as e:
    if e.code == 403:
        print(json.dumps({'reachable': False}))
    else:
        print(json.dumps({'error': 'unexpected-http-status'}))
except (socket.timeout, ConnectionRefusedError, socket.gaierror):
    print(json.dumps({'reachable': False}))
except Exception:
    print(json.dumps({'error': 'probe-error'}))
'''


def run_probe(target, probe, execute=subprocess.run):
    ns, pod, container = target
    result = execute(['kubectl', '-n', ns, 'exec', pod, '-c', container, '--',
                      'python3', '-c', PROBE, json.dumps(probe)],
                     capture_output=True, text=True, timeout=20)
    if result.returncode:
        raise RuntimeError('probe execution failed; cannot count as network denial')
    try:
        value = json.loads(result.stdout)
    except (ValueError, TypeError):
        raise RuntimeError('invalid probe output') from None
    if not isinstance(value, dict):
        raise RuntimeError('invalid probe result')
    if set(value) != {'reachable'} or type(value['reachable']) is not bool:
        raise RuntimeError('invalid probe result')
    return value['reachable']


def matrix(args):
    return [
        ('allowed-proxy-https', {'kind': 'https', 'url': args.allowed_https, 'via_proxy': True}, True),
        ('unlisted-dns', {'kind': 'resolve', 'host': args.unlisted_name}, False),
        ('direct-public-https', {'kind': 'tcp', 'host': args.public_ip, 'port': 443}, False),
        ('direct-dot', {'kind': 'tcp', 'host': args.resolver_ip, 'port': 853}, False),
        ('external-dns-tcp', {'kind': 'tcp', 'host': args.resolver_ip, 'port': 53}, False),
        ('external-dns-udp', {'kind': 'dns', 'host': args.resolver_ip, 'name': args.unlisted_name}, False),
        ('allowed-doh', {'kind': 'doh', 'url': args.allowed_doh, 'via_proxy': True}, True),
        ('unlisted-doh', {'kind': 'doh', 'url': args.unlisted_doh, 'via_proxy': True}, False),
    ]


def qualify(cases, worker, control, probe=run_probe):
    results = []
    for name, request, expected in cases:
        # Control has direct access. It proves that a refused/failed cell probe
        # did not merely hit a dead service, bad URL, or nonexistent DNS name.
        baseline = dict(request, via_proxy=False)
        if not probe(control, baseline):
            results.append({'check': name, 'result': 'inconclusive', 'reason': 'control-unreachable'})
            continue
        actual = probe(worker, request)
        results.append({'check': name, 'result': 'pass' if actual == expected else 'fail'})
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ['namespace', 'pod', 'container', 'control-namespace', 'control-pod', 'control-container',
                 'unlisted-name', 'public-ip', 'resolver-ip', 'allowed-https', 'allowed-doh', 'unlisted-doh']:
        parser.add_argument('--' + flag, required=True)
    parser.add_argument('--acknowledge-disposable-targets', action='store_true', required=True)
    args = parser.parse_args()
    if args.namespace == args.control_namespace:
        parser.error('control must be in a separate namespace outside cell policy')
    for value in [args.allowed_https, args.allowed_doh, args.unlisted_doh]:
        from urllib.parse import urlsplit
        url = urlsplit(value)
        if url.scheme != 'https' or not url.hostname or url.username or url.password or url.fragment:
            parser.error('probe URLs must be HTTPS without credentials or fragments')
    import ipaddress
    for value in [args.public_ip, args.resolver_ip]:
        if not ipaddress.IPv4Address(value).is_global:
            parser.error('direct targets must be public IPv4 addresses')
    try:
        results = qualify(matrix(args), (args.namespace, args.pod, args.container),
                          (args.control_namespace, args.control_pod, args.control_container))
    except (RuntimeError, subprocess.TimeoutExpired):
        print(json.dumps({'result': 'inconclusive', 'reason': 'probe-execution-error'}))
        return 2
    print(json.dumps({'checks': results, 'scope': 'IPv4 probes only; not full isolation qualification'}, indent=2))
    return 0 if all(r['result'] == 'pass' for r in results) else 1


if __name__ == '__main__':
    sys.exit(main())
