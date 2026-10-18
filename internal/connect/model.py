"""Manual operator session using the worker's mounted access-only grant."""
import json
import math
import re
import os
from pathlib import Path
import queue
import subprocess
import tempfile
import threading
import time

class RPC:
    def __init__(self, env):
        self.process = subprocess.Popen(
            ['codex', 'app-server', '--stdio', '-c', 'analytics.enabled=false',
             '-c', 'features.responses_websockets=false',
             '-c', 'features.responses_websockets_v2=false',
             '-c', 'features.multi_agent=false', '-c', 'project_doc_max_bytes=0'],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, bufsize=1, env=env)
        self.messages = queue.Queue()
        self.next_id = 0
        self.delta_count = 0
        self.completed_commands = 0
        self.failures = set()
        self.requested_methods = set()
        self.item_types = set()

        def reader():
            try:
                for line in self.process.stdout:
                    if len(line) > 4_194_304:
                        self.messages.put(None)
                        return
                    self.messages.put(json.loads(line))
            finally:
                self.messages.put(None)
        threading.Thread(target=reader, daemon=True).start()

    def send(self, value):
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
        self.process.terminate()
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()


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



class Session(RPC):
    def receive(self, deadline):
        while time.monotonic() < deadline:
            if getattr(self, "auth", None) is not None:
                current = refreshed_grant(self.auth["chatgptAccountId"])
                if current["accessToken"] != self.auth["accessToken"]:
                    self.auth = current
                    self.send({"id": "apc-internal-manual-renew", "method": "account/login/start", "params": current})
            try: message = self.messages.get(timeout=min(.2, max(.01, deadline-time.monotonic())))
            except queue.Empty: continue
            if message is None: raise RuntimeError("app-server-disconnected")
            if message.get("id") == "apc-internal-manual-renew" and "method" not in message:
                if "error" in message: raise RuntimeError("model renewal rejected")
                continue
            return message
        raise RuntimeError("app-server-timeout")

    def handle(self, message):
        if message.get('method') == 'account/chatgptAuthTokens/refresh' and 'id' in message:
            try:
                current = refreshed_grant(self.auth['chatgptAccountId'], previous_account=message.get('params', {}).get('previousAccountId'))
                self.send({'id': message['id'], 'result': {k: v for k, v in current.items() if k != 'type'}})
                self.auth = current
            except Exception:
                self.send({'id': message['id'], 'error': {'code': -32000, 'message': 'Codex renewal unavailable'}})
            return
        super().handle(message)
        method = message.get('method')
        if method == 'item/agentMessage/delta':
            print(message.get('params', {}).get('delta', ''), end='', flush=True)
        if method == 'item/completed':
            item = message.get('params', {}).get('item', {})
            if item.get('type') == 'commandExecution':
                print('\nCommand completed, exit ' + str(item.get('exitCode')))


def main():
    if os.getuid() != 1000 or Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists():
        raise RuntimeError('tokenless non-root worker required')
    repo = Path('/workspace/repo')
    auth = grant()
    with tempfile.TemporaryDirectory(prefix='apc-session-', dir='/workspace') as home:
        session = Session(dict(os.environ, CODEX_HOME=home, PATH='/opt/codex/bin:' + os.environ['PATH']))
        session.auth = auth
        try:
            session.call('initialize', {'clientInfo': {'name': 'apc-manual-session', 'version': '0.1.0'}, 'capabilities': {'experimentalApi': True}})
            session.send({'method': 'initialized', 'params': {}})
            session.call('account/login/start', auth)
            models = session.call('model/list', {})['data']
            model = next((m['id'] for m in models if m.get('isDefault')), models[0]['id'])
            thread = session.call('thread/start', {'cwd': str(repo), 'ephemeral': True, 'approvalPolicy': 'never', 'sandbox': 'read-only', 'model': model})['thread']['id']
            print('Manual session ready. Type a task, or /quit. Files stay on the workspace volume.')
            while True:
                try:
                    prompt = input('Task> ')
                except EOFError:
                    break
                if prompt.strip() == '/quit': break
                if not prompt.strip(): continue
                refreshed_grant(session.auth['chatgptAccountId'])
                turn = session.call('turn/start', {'threadId': thread, 'input': [{'type': 'text', 'text': prompt}],
                    'sandboxPolicy': {'type': 'externalSandbox', 'networkAccess': 'enabled'}})['turn']['id']
                print('Working...', flush=True)
                deadline = time.monotonic() + 600
                while True:
                    message = session.receive(deadline)
                    session.handle(message)
                    if message.get('method') == 'turn/completed' and message.get('params', {}).get('turn', {}).get('id') == turn:
                        status = message['params']['turn']['status']
                        print('\nTurn ' + status)
                        break
        finally:
            session.close()

if __name__ == '__main__':
    try:
        main()
    except KeyboardInterrupt:
        print('\nSession stopped.')
    except Exception:
        raise SystemExit('Session failed. Check workspace readiness, the model grant and allowed HTTPS policy. Credentials withheld.') from None
