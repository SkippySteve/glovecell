#!/usr/bin/env python3
"""Bounded first-cell acceptance on an explicitly disposable workspace.

Uses existing console credentials and ordinary APIs. Never discovers or copies
credentials into a worker. Missing authorization produces BLOCKED, not PASS.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

CHECKS = ('controller-enrollment', 'worker-image', 'repository-checkout',
          'upload-readback', 'outbound-transfer', 'pause-resume', 'file-persistence',
          'provider-access', 'provider-renewal', 'provider-revocation')


class Blocked(Exception):
    pass


class Failed(Exception):
    pass


def private_credential(path):
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'r') as f:
        info = os.fstat(f.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise Blocked('credential-file-must-be-owned-mode-0600')
        value = f.read(32769).strip()
    if not value or len(value) > 32768 or any(ord(c) < 33 or ord(c) > 126 for c in value):
        raise Blocked('invalid-credential-file')
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class Console:
    def __init__(self, origin, credential, session=False):
        parsed = urllib.parse.urlsplit(origin)
        if parsed.scheme != 'https' or not parsed.hostname or parsed.path not in ('', '/') or parsed.query or parsed.fragment or parsed.username or parsed.password:
            raise Blocked('explicit-https-console-origin-required')
        self.origin = origin.rstrip('/')
        self.headers = {'Origin': self.origin, 'Content-Type': 'application/json'}
        self.headers['Cookie' if session else 'Authorization'] = ('__Host-apc-session=' if session else 'Bearer ') + credential
        self.opener = urllib.request.build_opener(NoRedirect(), urllib.request.ProxyHandler({}))

    def call(self, path, method='GET', body=None):
        request = urllib.request.Request(self.origin + '/api/v1/' + path,
                                        data=None if body is None else json.dumps(body).encode(),
                                        headers=self.headers, method=method)
        try:
            with self.opener.open(request, timeout=60) as response:
                raw = response.read(2 << 20)
                return json.loads(raw)
        except urllib.error.HTTPError as e:
            if e.code in (401, 403):
                raise Blocked('console-authorization-required') from None
            raise Failed('console-http-' + str(e.code)) from None
        except (urllib.error.URLError, ValueError):
            raise Blocked('console-response-unavailable') from None


def disposable(obj, name, repository, uid=None):
    if not re.fullmatch(r'qualification-[a-z0-9-]{1,26}', name):
        raise Blocked('qualification-workspace-name-required')
    if obj.get('metadata', {}).get('name') != name or obj.get('spec', {}).get('repository') != repository:
        raise Blocked('disposable-workspace-repository-mismatch')
    actual = obj['metadata'].get('uid', '')
    if not actual or uid is not None and actual != uid:
        raise Blocked('workspace-identity-changed')
    return actual


def workspace(console, name):
    for obj in console.call('workspaces'):
        if obj.get('metadata', {}).get('name') == name:
            return obj
    raise Blocked('workspace-not-found')


def wait_phase(console, name, repository, uid, phase, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        obj = workspace(console, name)
        disposable(obj, name, repository, uid)
        if obj.get('status', {}).get('phase') == phase and obj.get('status', {}).get('observedGeneration') == obj['metadata'].get('generation'):
            return obj
        time.sleep(2)
    raise Failed('workspace-phase-timeout-' + phase.lower())


def worker(namespace):
    result = subprocess.run(['kubectl', '-n', namespace, 'get', 'pods', '-l', 'projects.agentwork.dev/role=worker', '-o', 'json'], capture_output=True, timeout=30)
    if result.returncode:
        raise Blocked('worker-inspection-denied-or-unavailable')
    try:
        pods = json.loads(result.stdout)['items']
        if len(pods) != 1:
            raise Blocked('exactly-one-worker-required')
        pod = pods[0]
        if not any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in pod.get('status', {}).get('conditions', [])):
            raise Blocked('worker-not-ready')
        container = next(c for c in pod['spec']['containers'] if c['name'] == 'workspace')
        return {'pod': pod['metadata']['name'], 'podUid': pod['metadata']['uid'],
                'image': container['image'], 'runtime': pod['spec'].get('runtimeClassName'),
                'node': pod['spec'].get('nodeName')}
    except (ValueError, KeyError, StopIteration):
        raise Blocked('invalid-worker-metadata') from None


def remote(namespace, pod, program, args=(), stdin=None):
    command = ['kubectl', '-n', namespace, 'exec']
    if stdin is not None:
        command.append('-i')
    command += [pod, '-c', 'workspace', '--', 'python3', '-c', program, *args]
    result = subprocess.run(command, input=stdin, capture_output=True, timeout=45)
    if result.returncode:
        raise Blocked('worker-exec-denied-or-probe-failed')
    try:
        return json.loads(result.stdout)
    except ValueError:
        raise Blocked('worker-probe-invalid-output') from None


REPO = """import json,subprocess
branch=subprocess.check_output(['git','-C','/workspace/repo','branch','--show-current'],text=True).strip()
origin=subprocess.check_output(['git','-C','/workspace/repo','remote','get-url','origin'],text=True).strip()
print(json.dumps({'branch':branch,'repositoryMatches':origin.endswith('/'+__import__('sys').argv[1]+'.git')}))
"""
MARKER = """import json,hashlib,pathlib,sys
p=pathlib.Path('/workspace')/sys.argv[1]
if sys.argv[2]=='write':p.write_bytes(sys.stdin.buffer.read())
print(json.dumps({'sha256':hashlib.sha256(p.read_bytes()).hexdigest()}))
"""


def record(report, name, fn):
    try:
        evidence = fn()
        report['checks'][name] = {'result': 'passed', 'evidence': evidence}
    except Blocked as e:
        report['checks'][name] = {'result': 'blocked', 'reason': str(e)}
    except Failed as e:
        report['checks'][name] = {'result': 'failed', 'reason': str(e)}


def conclude(report):
    values = [c['result'] for c in report['checks'].values()]
    return 'failed' if 'failed' in values else 'passed' if values and all(v == 'passed' for v in values) else 'incomplete'


def qualify(console, name, repository, expected_image, runtime, timeout, execute_worker=False):
    report = {'schemaVersion': 1, 'at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'workspace': name, 'checks': {c: {'result': 'not-run'} for c in CHECKS}}
    obj = workspace(console, name)
    uid = disposable(obj, name, repository)
    namespace = 'apc-' + uid.replace('-', '')
    if obj.get('status', {}).get('namespace') != namespace:
        raise Blocked('workspace-namespace-not-bound-to-uid')
    obj = wait_phase(console, name, repository, uid, 'Ready', timeout)
    report['identity'] = {'uid': uid, 'namespace': namespace}
    report['checks']['controller-enrollment'] = {'result': 'passed', 'evidence': 'controller-ready-observed-generation'}
    before = worker(namespace)
    report['workerBefore'] = before
    def image_check():
        if before['image'] != expected_image or before['runtime'] != runtime:
            raise Failed('worker-image-or-runtime-mismatch')
        return 'matches-explicit-expected-image-and-runtime'
    record(report, 'worker-image', image_check)
    if report['checks']['worker-image']['result'] != 'passed':
        report['result'] = conclude(report)
        return report
    nonce = 'qualification-' + os.urandom(12).hex()
    payload = nonce.encode()
    digest = hashlib.sha256(payload).hexdigest()
    marker = nonce + '.txt'
    if execute_worker:
        def repo_check():
            found = remote(namespace, before['pod'], REPO, (repository,))
            if not found.get('repositoryMatches') or not found.get('branch', '').startswith('agent/apc-'):
                raise Failed('checkout-or-agent-branch-mismatch')
            return {'branch': found['branch'], 'repositoryMatches': True}
        record(report, 'repository-checkout', repo_check)
        def transfer():
            got = remote(namespace, before['pod'], MARKER, (marker, 'write'), payload)
            readback = remote(namespace, before['pod'], MARKER, (marker, 'read'))
            if got.get('sha256') != digest or readback.get('sha256') != digest:
                raise Failed('operator-transfer-hash-mismatch')
            return {'sha256': digest, 'channel': 'operator-exec-not-console-upload'}
        record(report, 'outbound-transfer', transfer)
    else:
        for c in ['repository-checkout', 'outbound-transfer', 'file-persistence']:
            report['checks'][c] = {'result': 'blocked', 'reason': 'worker-exec-not-enabled'}
    # Never infer console upload/provider success from a capability flag or Ready.
    report['checks']['upload-readback'] = {'result': 'not-run', 'reason': 'requires-separate-oidc-console-upload-evidence'}
    for c in ['provider-access', 'provider-renewal', 'provider-revocation']:
        report['checks'][c] = {'result': 'not-run', 'reason': 'requires-real-provider-and-oidc-revocation-evidence'}
    def cycle():
        current = workspace(console, name)
        disposable(current, name, repository, uid)
        console.call('workspaces/' + name, 'PATCH', {'resourceVersion': current['metadata']['resourceVersion'], 'desiredState': 'Paused'})
        paused = wait_phase(console, name, repository, uid, 'Paused', timeout)
        console.call('workspaces/' + name, 'PATCH', {'resourceVersion': paused['metadata']['resourceVersion'], 'desiredState': 'Running'})
        wait_phase(console, name, repository, uid, 'Ready', timeout)
        after = worker(namespace)
        if after['podUid'] == before['podUid']:
            raise Failed('pause-resume-did-not-replace-worker')
        if after['image'] != expected_image or after['runtime'] != runtime:
            raise Failed('resumed-worker-image-or-runtime-mismatch')
        report['workerAfter'] = after
        return 'same-workspace-uid-new-ready-worker'
    record(report, 'pause-resume', cycle)
    if execute_worker and report['checks']['outbound-transfer']['result'] == 'passed' and report['checks']['pause-resume']['result'] == 'passed':
        def persistence():
            got = remote(namespace, report['workerAfter']['pod'], MARKER, (marker, 'read'))
            if got.get('sha256') != digest:
                raise Failed('persistent-marker-hash-mismatch')
            return {'sha256': digest}
        record(report, 'file-persistence', persistence)
    # Leave only this disposable cell paused, retaining the report's marker.
    current = workspace(console, name)
    disposable(current, name, repository, uid)
    console.call('workspaces/' + name, 'PATCH', {'resourceVersion': current['metadata']['resourceVersion'], 'desiredState': 'Paused'})
    wait_phase(console, name, repository, uid, 'Paused', timeout)
    report['finalState'] = 'Paused'
    report['result'] = conclude(report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['origin', 'workspace', 'disposable-repository', 'credential-file', 'expected-worker-image']:
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--session-cookie', action='store_true')
    parser.add_argument('--expected-runtime', default='agent-gvisor')
    parser.add_argument('--execute-worker-probes', action='store_true')
    parser.add_argument('--timeout', type=int, default=300)
    parser.add_argument('--acknowledge-disposable-target', required=True, action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'.+@sha256:[a-f0-9]{64}', args.expected_worker_image):
        parser.error('expected worker image must be digest-pinned')
    if not 10 <= args.timeout <= 900:
        parser.error('timeout must be between 10 and 900 seconds')
    try:
        console = Console(args.origin, private_credential(args.credential_file), args.session_cookie)
        report = qualify(console, args.workspace, args.disposable_repository,
                         args.expected_worker_image, args.expected_runtime, args.timeout,
                         args.execute_worker_probes)
    except (Blocked, Failed, OSError, subprocess.TimeoutExpired) as e:
        # Never print arbitrary exception text, HTTP response bodies, remote
        # stderr or credentials. A failed operation cannot produce an all-pass report.
        report = {'result': 'failed' if isinstance(e, Failed) else 'incomplete', 'reason': str(e) if isinstance(e, (Blocked, Failed)) else 'execution-unavailable',
                  'checks': {c: {'result': 'not-run'} for c in CHECKS}}
    print(json.dumps(report, indent=2))
    return 0 if report['result'] == 'passed' else 2


if __name__ == '__main__':
    raise SystemExit(main())
