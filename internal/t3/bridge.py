"""T3 stdio adapter. Operator grants stay inside the worker."""
import json
import math
import os
from pathlib import Path
import queue
import re
import signal
import subprocess
import sys
import threading
import time

class RPC:
    def __init__(self, env):
        self.process = subprocess.Popen(
            ['codex', 'app-server', '--stdio', '-c', 'analytics.enabled=false',
             '-c', 'features.responses_websockets=false',
             '-c', 'features.responses_websockets_v2=false',
             '-c', 'features.multi_agent=false', '-c', 'project_doc_max_bytes=32768'],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, bufsize=1, env=env, start_new_session=True)
        self.messages = queue.Queue()
        self.write_lock = threading.Lock()
        self.next_id = 0
        self.delta_count = 0
        self.completed_commands = 0
        self.failures = set()
        self.requested_methods = set()
        self.item_types = set()

        def reader():
            try:
                while True:
                    line = self.process.stdout.readline(4_194_305)
                    if not line:
                        break
                    if len(line) > 4_194_304:
                        self.messages.put(None)
                        return
                    self.messages.put(json.loads(line))
            finally:
                self.messages.put(None)
        threading.Thread(target=reader, daemon=True).start()

    def send(self, value):
        with self.write_lock:
            self.process.stdin.write(json.dumps(value) + '\n')
            self.process.stdin.flush()

    def receive(self, deadline):
        try:
            message = self.messages.get(timeout=max(.01, deadline - time.monotonic()))
        except queue.Empty:
            raise RuntimeError('app-server-timeout') from None
        if message is None:
            raise RuntimeError('app-server-disconnected')
        return message

    def handle(self, message):
        method = message.get('method', '')
        if 'id' in message and method:
            self.requested_methods.add(method)
            # Never approve unexpected tool, permission or refresh requests.
            self.send({'id': message['id'], 'error': {'code': -32000, 'message': 'manual client does not approve this request'}})
        if method == 'item/agentMessage/delta':
            self.delta_count += 1
        if method == 'item/completed':
            item = message.get('params', {}).get('item', {})
            self.item_types.add(item.get('type', 'unknown'))
            if item.get('type') == 'commandExecution' and item.get('exitCode') == 0:
                self.completed_commands += 1
        if method == 'error':
            text = json.dumps(message).lower()
            self.failures.update(name for name, marker in (
                ('authentication', '401'), ('policy-denial', '403'),
                ('rate-limit', '429'), ('transport', 'connect'),
                ('sandbox', 'sandbox'), ('websocket', 'websocket')) if marker in text)

    def call(self, method, params):
        self.next_id += 1
        ident = self.next_id
        self.send({'id': ident, 'method': method, 'params': params})
        deadline = time.monotonic() + 30
        while True:
            message = self.receive(deadline)
            if message.get('id') == ident and 'method' not in message:
                if 'error' in message:
                    raise RuntimeError('app-server-rejected-' + method)
                return message.get('result')
            self.handle(message)

    def close(self):
        try: os.killpg(self.process.pid, signal.SIGTERM)
        except ProcessLookupError: pass
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            try: os.killpg(self.process.pid, signal.SIGKILL)
            except ProcessLookupError: pass
            self.process.wait()


ROOT = Path('/workspace/repo')
HOME = Path('/workspace/.apc/codex')


class WorkspaceDirectoryDenied(ValueError):
    pass


def denied_response(value, error):
    if not isinstance(value, dict) or 'id' not in value:
        return None
    message = str(error) if isinstance(error, WorkspaceDirectoryDenied) else 'request denied by project adapter'
    return {'id': value['id'], 'error': {'code': -32602, 'message': message}}


def registered_roots(root, registry):
    roots = [root.resolve(strict=True)]
    if not registry.exists():
        return roots
    entries = json.loads(registry.read_text())
    if not isinstance(entries, list) or len(entries) > 16:
        raise ValueError('invalid project registry')
    for entry in entries:
        name = entry.get('name', '')
        if not re.fullmatch('[a-z][a-z0-9-]{0,39}', name):
            raise ValueError('invalid project registry name')
        path = root.parent / 'projects' / name
        resolved = path.resolve(strict=True)
        if resolved != path or not resolved.is_dir():
            raise ValueError('enrolled project storage changed')
        roots.append(resolved)
    return roots


def translate(message, root=ROOT, registry=Path('/identity/projects.json')):
    if not isinstance(message, dict):
        raise ValueError('invalid request')
    method = message.get('method', '')
    params = message.get('params')
    if isinstance(message.get('id'), str) and message['id'].startswith('apc-internal-'):
        raise ValueError('reserved request identifier')
    if method.startswith('account/') and method not in ('account/read', 'account/rateLimits/read'):
        raise ValueError('model account is managed by the operator')
    if method == 'account/read':
        params = dict(params or {}, refreshToken=False)
        message['params'] = params
    if method in ('thread/start', 'thread/resume', 'thread/fork'):
        if not isinstance(params, dict): raise ValueError('thread parameters required')
        cwd = params.get('cwd')
        if cwd is None and method == 'thread/start': raise ValueError('project directory required')
        if cwd is not None:
            try:
                directory = Path(cwd).resolve(strict=True)
                bases = registered_roots(root, registry)
            except OSError:
                raise WorkspaceDirectoryDenied('Project folder is unavailable. Open the enrolled checkout at ' + str(root) + ' and start a new thread.') from None
            if not directory.is_dir() or not any(directory.is_relative_to(base) for base in bases):
                raise WorkspaceDirectoryDenied('This thread uses a folder outside the enrolled checkout. Open the project at ' + str(root) + ' and start a new thread.')
            params['cwd'] = str(directory)
            params['runtimeWorkspaceRoots'] = [str(directory)]
        params.pop('permissions', None)
        params.pop('approvalsReviewer', None)
        params['sandbox'] = 'read-only'
        params['config'] = {'analytics.enabled': False, 'features.multi_agent': False}
    if method == 'turn/start':
        if not isinstance(params, dict): raise ValueError('turn parameters required')
        params.pop('permissions', None)
        params.pop('approvalsReviewer', None)
        params['sandboxPolicy'] = {'type': 'externalSandbox', 'networkAccess': 'enabled'}
    return message


def grant(directory=Path('/run/model')):
    allowed = {'access-token', 'chatgpt-account-id', 'expires-at', 'chatgpt-plan-type', 'provider-access'}
    names = {p.name for p in directory.iterdir() if not p.name.startswith('.')}
    if names - allowed: raise ValueError('unsupported model fields')
    # Resolve one projected Secret generation so rotation cannot mix accounts,
    # tokens and expiry from different Kubernetes symlink targets.
    generation = directory / '..data'
    if generation.exists(): directory = generation.resolve(strict=True)
    provider_file = directory / 'provider-access'
    if provider_file.exists():
        raw = provider_file.read_bytes()
        if len(raw) > 131072: raise ValueError('oversized provider projection')
        providers = json.loads(raw)
        if not isinstance(providers, dict) or set(providers) - {'codex', 'grok', 'cursor', 'muse'}:
            raise ValueError('unsupported provider projection')
        if 'codex' in providers:
            record = providers['codex']
            if not isinstance(record, dict) or set(record) - {'enabled', 'access'} or record.get('enabled') is not True:
                raise ValueError('Codex revoked')
            access = record.get('access')
            if not isinstance(access, dict) or set(access) - {'accessToken', 'expiresAt', 'chatgptAccountId', 'chatgptPlanType'}:
                raise ValueError('access-only Codex projection required')
            expiry = access.get('expiresAt')
            if isinstance(expiry, bool) or not isinstance(expiry, (int, float)) or not math.isfinite(expiry) or expiry <= time.time() + 60:
                raise ValueError('Codex grant expired')
            token, account = access.get('accessToken'), access.get('chatgptAccountId')
            if not isinstance(token, str) or not re.fullmatch(r'[A-Za-z0-9._~+/=-]{1,32768}', token) or not isinstance(account, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,256}', account):
                raise ValueError('invalid Codex access')
            return {'type': 'chatgptAuthTokens', 'accessToken': token, 'chatgptAccountId': account,
                    'chatgptPlanType': access.get('chatgptPlanType')}
    expiry = float((directory / 'expires-at').read_text())
    if not math.isfinite(expiry) or expiry <= time.time() + 60: raise ValueError('model grant expired')
    token = (directory / 'access-token').read_text().strip()
    account = (directory / 'chatgpt-account-id').read_text().strip()
    if not token or not account: raise ValueError('incomplete model grant')
    plan = directory / 'chatgpt-plan-type'
    return {'type': 'chatgptAuthTokens', 'accessToken': token, 'chatgptAccountId': account,
            'chatgptPlanType': plan.read_text().strip() if plan.exists() else None}


def refreshed_grant(account, directory=Path('/run/model'), previous_account=None):
    current = grant(directory)
    if current['chatgptAccountId'] != account or previous_account not in (None, account):
        raise ValueError('Codex account changed')
    return current



def main():
    if os.getuid() != 1000 or Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists():
        raise ValueError('tokenless project worker required')
    auth = grant()
    HOME.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not HOME.resolve().is_relative_to(Path('/workspace')):
        raise ValueError('invalid session directory')
    first = sys.stdin.readline(4_194_305)
    if len(first) > 4_194_304: raise ValueError('oversized initialize request')
    initial = json.loads(first)
    if initial.get('method') != 'initialize' or 'id' not in initial:
        raise ValueError('initialize required')
    rpc = RPC(dict(os.environ, CODEX_HOME=str(HOME), PATH='/opt/codex/bin:' + os.environ['PATH']))
    output_lock = threading.Lock()
    def send(value):
        with output_lock:
            print(json.dumps(value), flush=True)
    try:
        initialized = rpc.call('initialize', initial.get('params', {}))
        rpc.send({'method': 'initialized', 'params': {}})
        rpc.call('account/login/start', auth)
        send({'id': initial['id'], 'result': initialized})
        ended = threading.Event()
        def frontend():
            try:
                while True:
                    line = sys.stdin.readline(4_194_305)
                    if not line: break
                    if len(line) > 4_194_304: raise ValueError('oversized request')
                    value = json.loads(line)
                    try: value = translate(value)
                    except (ValueError, OSError, TypeError) as error:
                        response = denied_response(value, error)
                        if response is not None: send(response)
                        continue
                    # T3 may immediately reuse integer IDs from its initialize call.
                    if value.get('method') == 'initialized': continue
                    rpc.send(value)
            except Exception:
                pass
            finally: ended.set()
        threading.Thread(target=frontend, daemon=True).start()
        internal_id = 0
        pending_login = None
        checked = 0
        while not ended.is_set():
            if time.monotonic() - checked >= 1:
                checked = time.monotonic()
                current = refreshed_grant(auth['chatgptAccountId'])
                if current['accessToken'] != auth['accessToken'] and pending_login is None:
                    internal_id += 1
                    pending_login = 'apc-internal-renew-' + str(internal_id)
                    rpc.send({'id': pending_login, 'method': 'account/login/start', 'params': current})
                    auth = current
            try: value = rpc.messages.get(timeout=.2)
            except queue.Empty: continue
            if value is None: raise ValueError('app-server disconnected')
            if pending_login is not None and value.get('id') == pending_login and 'method' not in value:
                if 'error' in value: raise ValueError('Codex renewal rejected')
                pending_login = None
                continue
            if value.get('method') == 'account/chatgptAuthTokens/refresh' and 'id' in value:
                try:
                    current = refreshed_grant(auth['chatgptAccountId'], previous_account=value.get('params', {}).get('previousAccountId'))
                    rpc.send({'id': value['id'], 'result': {k: v for k, v in current.items() if k != 'type'}})
                    auth = current
                except (ValueError, OSError, TypeError):
                    rpc.send({'id': value['id'], 'error': {'code': -32000, 'message': 'Codex grant renewal unavailable'}})
                continue
            if value.get('method', '').startswith('account/') and 'id' in value:
                rpc.send({'id': value['id'], 'error': {'code': -32000, 'message': 'operator grant renewal required'}})
                continue
            send(value)
    finally: rpc.close()


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    try: main()
    except (Exception, KeyboardInterrupt):
        print('T3 model adapter stopped; verify project readiness and the operator grant.', file=sys.stderr)
        sys.exit(1)
