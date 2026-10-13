import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('apc_fj', Path(__file__).with_name('apc-fj.py'))
fj = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fj)


class ForgejoLauncherTest(unittest.TestCase):
    environment = {'GIT_ORIGIN': 'https://git.example.com', 'GIT_HOST': 'git.example.com',
                   'HTTPS_PROXY': 'http://proxy:3128', 'SSL_CERT_FILE': '/trust/combined.crt',
                   'XDG_DATA_HOME': '/workspace/home/.local/share'}

    def test_invalid_destination_or_material_is_denied(self):
        for origin, host, token in (
            ('http://git.example.com', 'git.example.com', 'grant'),
            ('https://git.example.com:8443', 'git.example.com:8443', 'grant'),
            ('https://attacker.example', 'git.example.com', 'grant'),
            ('https://git.example.com@attacker.example', 'attacker.example', 'grant'),
            ('https://git.example.com/api', 'git.example.com', 'grant'),
            ('https://github.com', 'github.com', 'grant'),
            ('https://git.example.com', 'git.example.com', ''),
            ('https://git.example.com', 'git.example.com', 'grant\nother'),
        ):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                fj.configuration(origin, host, token)

    def test_direct_authentication_and_host_overrides_are_denied(self):
        for arguments in (['auth', 'login'], ['-C', '/project', 'auth', 'add-token'],
                          ['--host=attacker.example', 'repo', 'view'],
                          ['-Hattacker.example', 'repo', 'view']):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                fj.validate_arguments(arguments)
        fj.validate_arguments(['pr', 'create', '--title', 'auth'])

    def test_renewal_uses_current_grant_and_preserves_proxy_and_trust(self):
        paths = []
        tokens = []
        def native(arguments, env):
            state = Path(env['XDG_DATA_HOME']) / 'forgejo-cli' / 'keys.json'
            paths.append(state)
            self.assertEqual(state.stat().st_mode & 0o777, 0o600)
            self.assertEqual(state.parent.stat().st_mode & 0o777, 0o700)
            config = json.loads(state.read_text())
            tokens.append(config['hosts']['git.example.com']['token'])
            self.assertEqual(env['HTTPS_PROXY'], self.environment['HTTPS_PROXY'])
            self.assertEqual(env['SSL_CERT_FILE'], self.environment['SSL_CERT_FILE'])
            self.assertNotIn(tokens[-1], arguments)
            self.assertNotIn(tokens[-1], env.values())
            self.assertEqual(config['hosts']['git.example.com']['type'], 'Application')
            return subprocess.CompletedProcess(arguments, 0)
        with tempfile.TemporaryDirectory() as directory, patch.object(fj.subprocess, 'run', native):
            identity = Path(directory)
            for token in ('first-access-grant', 'renewed-access-grant'):
                (identity / 'token').write_text(token)
                self.assertEqual(fj.run(['repo', 'view'], self.environment, identity), 0)
                self.assertFalse(paths[-1].exists())
        self.assertEqual(tokens, ['first-access-grant', 'renewed-access-grant'])

    def test_interrupted_launch_cleans_up_and_can_retry(self):
        states = []
        def interrupted(arguments, env):
            states.append(Path(env['XDG_DATA_HOME']))
            raise OSError('interrupted launch')
        with tempfile.TemporaryDirectory() as directory:
            identity = Path(directory)
            (identity / 'token').write_text('access-only-grant')
            with patch.object(fj.subprocess, 'run', interrupted), self.assertRaises(OSError):
                fj.run(['repo', 'view'], self.environment, identity)
            self.assertFalse(states[0].exists())
            with patch.object(fj.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)):
                self.assertEqual(fj.run(['repo', 'view'], self.environment, identity), 0)

    def test_revoked_or_missing_grant_never_launches_native_client(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(fj.subprocess, 'run') as native:
            identity = Path(directory)
            with self.assertRaises(OSError):
                fj.run(['repo', 'view'], self.environment, identity)
            (identity / 'token').write_text('')
            with self.assertRaises(ValueError):
                fj.run(['repo', 'view'], self.environment, identity)
            native.assert_not_called()


if __name__ == '__main__':
    unittest.main()
