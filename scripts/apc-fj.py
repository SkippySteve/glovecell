#!/usr/bin/python3
"""Run stock fj with the controller's current repository-scoped grant."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit


def configuration(origin, host, token):
    url = urlsplit(origin)
    if (url.scheme != 'https' or not url.hostname or url.netloc != host
            or url.username is not None or url.password is not None
            or url.path not in ('', '/') or url.query or url.fragment
            or url.port not in (None, 443)
            or url.hostname in ('github.com', 'api.github.com')):
        raise ValueError('an enrolled Forgejo HTTPS origin is required')
    if not token or any(c in token for c in '\r\n\0'):
        raise ValueError('repository grant is unavailable')
    return {'hosts': {host: {'type': 'Application', 'token': token}},
            'aliases': {}, 'default_ssh': []}


def validate_arguments(arguments):
    # Host selection belongs to the controller. Authentication stays brokered.
    for argument in arguments:
        if argument == '--host' or argument.startswith('--host=') or argument.startswith('-H'):
            raise ValueError('Forgejo host is managed by the controller')
    index = 0
    while index < len(arguments):
        argument = arguments[index]
        if argument in ('-C', '--cwd', '--style'):
            index += 2
            continue
        if argument.startswith(('--cwd=', '--style=')) or argument.startswith('-'):
            index += 1
            continue
        if argument == 'auth':
            raise ValueError('repository authorization is managed by the controller; enroll the repository there')
        break


def run(arguments, environment, identity=Path('/identity'), binary='/opt/fj/fj'):
    validate_arguments(arguments)
    # Read at invocation time so renewed grants do not need a sandbox restart.
    token = (identity / 'token').read_text().strip()
    host = environment.get('GIT_HOST', '')
    origin = environment.get('GIT_ORIGIN', '')
    registry = identity / 'projects.json'
    if registry.exists():
        # Choose the current repository's grant, including an explicit native -C.
        cwd = os.getcwd()
        for index, argument in enumerate(arguments):
            if argument in ('-C', '--cwd'):
                cwd = arguments[index + 1]
            elif argument.startswith('--cwd='):
                cwd = argument.split('=', 1)[1]
        remote = subprocess.run(['git', '-C', cwd, 'config', '--get', 'remote.origin.url'],
                                capture_output=True, text=True, check=True).stdout.strip()
        entries = json.loads(registry.read_text())
        if not isinstance(entries, list) or len(entries) > 16:
            raise ValueError('invalid project registry')
        selected = next((e for e in entries if remote == e['origin'] + '/' + e['repository'] + '.git'), None)
        if selected is not None:
            if selected['host'] != 'forgejo':
                raise ValueError('this project uses GitHub')
            origin, token = selected['origin'], selected['token']
            host = urlsplit(origin).netloc
        elif remote != origin.rstrip('/') + '/' + environment.get('GIT_REPOSITORY', '') + '.git':
            raise ValueError('current repository is not enrolled')
    config = configuration(origin, host, token)
    # Do not write credentials into the persistent workspace or process arguments.
    with tempfile.TemporaryDirectory(prefix='apc-fj-') as directory:
        state = Path(directory) / 'forgejo-cli'
        state.mkdir(mode=0o700)
        keys = state / 'keys.json'
        descriptor = os.open(keys, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, 'w') as output:
            json.dump(config, output)
        child = dict(environment, XDG_DATA_HOME=directory)
        return subprocess.run([binary, '--host', origin, *arguments], env=child).returncode


def main():
    try:
        return run(sys.argv[1:], os.environ)
    except (OSError, ValueError, KeyError, IndexError, subprocess.CalledProcessError):
        # Neither file paths nor native exceptions should expose credential data.
        print('Forgejo CLI unavailable: verify the enrolled Forgejo repository and its controller-managed grant. Host overrides and direct sign-in are disabled.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
