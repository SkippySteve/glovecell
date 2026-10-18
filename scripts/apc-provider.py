#!/usr/bin/env python3
"""Stock provider launchers consume projected access-only grants.

No account sign-in or direct credential service connection is permitted here.
Cursor rotation ends only its process. Its session directory is retained for
the application's normal resume operation; completed prompts are never replayed.
"""
import json
import math
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

GRANTS = Path('/run/model/provider-access')
BINARIES = {'grok': '/opt/grok/grok', 'cursor': '/opt/cursor-agent/cursor-agent',
            'muse': '/opt/muse/muse'}


def validate(value, now=None):
    now = time.time() if now is None else now
    if not isinstance(value, dict) or set(value) - {'enabled', 'access'}:
        raise ValueError('invalid provider grant')
    if value.get('enabled') is not True:
        raise ValueError('provider revoked or not authorized')
    access = value.get('access')
    if not isinstance(access, dict) or set(access) - {'accessToken', 'expiresAt', 'issuer'}:
        raise ValueError('access-only provider grant required')
    expiry = access.get('expiresAt')
    if isinstance(expiry, bool) or not isinstance(expiry, (int, float)) or not math.isfinite(expiry) or expiry <= now + 60:
        raise ValueError('provider grant expired; trusted broker renewal required')
    token = access.get('accessToken')
    if not isinstance(token, str) or not 0 < len(token) <= 32768 or any(ord(c) < 33 or ord(c) > 126 for c in token):
        raise ValueError('invalid provider access token')
    if 'issuer' in access and access['issuer'] != 'https://auth.x.ai':
        raise ValueError('unexpected provider issuer')
    return access


def grant(provider, path=GRANTS):
    raw = path.read_bytes()
    if len(raw) > 131072:
        raise ValueError('provider grants too large')
    document = json.loads(raw)
    if not isinstance(document, dict) or set(document) - (set(BINARIES) | {"codex"}):
        raise ValueError('unreviewed provider')
    access = validate(document.get(provider))
    if provider == 'grok' and access.get('issuer') != 'https://auth.x.ai':
        raise ValueError('Grok issuer required')
    return access


def enrolled_directory(directory, root=Path('/workspace/repo'), registry=Path('/identity/projects.json')):
    resolved = Path(directory).resolve(strict=True)
    bases = [root.resolve(strict=True)]
    if registry.exists():
        raw = registry.read_bytes()
        if len(raw) > 131072:
            raise ValueError('project registry too large')
        entries = json.loads(raw)
        if not isinstance(entries, list) or len(entries) > 16:
            raise ValueError('invalid project registry')
        for entry in entries:
            name = entry.get('name', '')
            if not re.fullmatch('[a-z][a-z0-9-]{0,39}', name):
                raise ValueError('invalid enrolled directory')
            base = root.parent / 'projects' / name
            if base.resolve(strict=True) != base:
                raise ValueError('enrolled storage changed')
            bases.append(base)
    if not resolved.is_dir() or not any(resolved.is_relative_to(base) for base in bases):
        raise ValueError('select an enrolled project before starting the provider')
    return resolved


def arguments_allowed(arguments):
    if arguments and arguments[0] in {'login', 'logout', 'auth', 'update', 'worker', 'cursor-worker'}:
        raise ValueError('account authorization and client updates belong to the trusted backend')
    forbidden = {'--api-key', '--api-key-stdin', '--auth-token', '--endpoint', '-e', '--base-url', '--insecure', '-k'}
    if any(argument.split('=', 1)[0] in forbidden for argument in arguments):
        raise ValueError('provider credential or endpoint overrides are denied')


def main():
    os.umask(0o077)
    name = Path(sys.argv[0]).name
    provider = {'cursor-agent': 'cursor'}.get(name, name)
    arguments = sys.argv[1:]
    if name == 'apc-provider' and arguments[:1] == ['grant'] and len(arguments) == 2:
        # Only Grok's documented external-auth subprocess consumes this output.
        if arguments[1] != 'grok':
            raise ValueError('unsupported external-auth consumer')
        access = grant('grok')
        print(json.dumps({'access_token': access['accessToken'],
                          'expires_in': max(1, int(access['expiresAt'] - time.time())),
                          'issuer': access['issuer']}))
        return 0
    if provider not in BINARIES:
        raise ValueError('unsupported provider')
    binary = BINARIES[provider]
    if arguments in [['--version'], ['-V'], ['--help'], ['-h']]:
        os.execv(binary, [binary, *arguments])
    if os.getuid() != 1000 or Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists():
        raise ValueError('tokenless project worker required')
    arguments_allowed(arguments)
    enrolled_directory(Path.cwd())
    access = grant(provider)
    env = dict(os.environ)
    for key in ('CURSOR_API_KEY', 'CURSOR_AUTH_TOKEN', 'META_API_KEY', 'XAI_API_KEY',
                'GROK_API_KEY', 'MUSE_AUTH_PATH', 'CURSOR_CONFIG_DIR', 'GROK_AUTH_PROVIDER_COMMAND'):
        env.pop(key, None)
    home = Path('/workspace/.apc/providers') / provider
    home.mkdir(mode=0o700, parents=True, exist_ok=True)
    if home.resolve() != home:
        raise ValueError('provider session storage changed')
    env.update(HOME=str(home), XDG_CONFIG_HOME=str(home / '.config'),
               XDG_DATA_HOME=str(home / '.local/share'), NODE_USE_ENV_PROXY='1')
    if provider == 'grok':
        env.update(GROK_AUTH_PROVIDER_COMMAND='/usr/local/bin/apc-provider grant grok',
                   GROK_AUTH_EARLY_INVALIDATION_SECS='60', GROK_DISABLE_AUTOUPDATER='1')
        login = subprocess.run([binary, 'login'], env=env, stdin=subprocess.DEVNULL,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
        if login.returncode:
            raise ValueError('official Grok external-access initialization failed')
    elif provider == 'cursor':
        config = home / '.config/cursor'
        config.mkdir(mode=0o700, parents=True, exist_ok=True)
        settings = config / 'cli-config.json'
        existing = json.loads(settings.read_text()) if settings.exists() else {}
        existing.setdefault('version', 1)
        existing.setdefault('editor', {'vimMode': False})
        existing.setdefault('permissions', {'allow': [], 'deny': []})
        existing.setdefault('network', {})['useHttp1ForAgent'] = True
        settings.write_text(json.dumps(existing))
        env['CURSOR_AUTH_TOKEN'] = access['accessToken']
    else:
        # The non-secret placeholder is accepted only by the inspecting gateway,
        # which injects the subscription credential obtained by official sign-in.
        if access['accessToken'] != 'apc-muse-subscription-gateway':
            raise ValueError('Muse subscription must remain at the trusted gateway')
        config = home / '.config/muse'
        config.mkdir(mode=0o700, parents=True, exist_ok=True)
        (config / 'auth.json').write_text(json.dumps({'schema_version': 1, 'providers': {'meta': {
            'mechanism': 'oauth', 'obtained_via': 'device_code', 'api_base_url': 'https://api.meta.ai/v1',
            'access_token': access['accessToken'], 'api_key': access['accessToken']}}}))
    child = subprocess.Popen([binary, *arguments], env=env)
    def stop(signum, _frame):
        if child.poll() is None:
            child.send_signal(signum)
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGHUP, stop)
    try:
        while child.poll() is None:
            current = grant(provider)
            if provider == 'cursor' and current['accessToken'] != access['accessToken']:
                raise ValueError('Cursor access renewed; resume this session to consume the new grant')
            time.sleep(1)
        return child.returncode
    finally:
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (ValueError, OSError, KeyError, TypeError, json.JSONDecodeError, subprocess.SubprocessError) as error:
        print('Provider unavailable: ' + (str(error) if isinstance(error, ValueError)
              and not isinstance(error, json.JSONDecodeError) else type(error).__name__), file=sys.stderr)
        raise SystemExit(1)
