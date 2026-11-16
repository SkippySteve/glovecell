import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('provider', Path(__file__).with_name('apc-provider.py'))
provider = importlib.util.module_from_spec(spec)
spec.loader.exec_module(provider)


class ProviderBoundary(unittest.TestCase):
    def test_grant_expiry_revocation_and_recovery(self):
        value = {'enabled': True, 'access': {'accessToken': 'access-only', 'expiresAt': 1000}}
        self.assertEqual(provider.validate(value, 100)['accessToken'], 'access-only')
        for bad in [{'enabled': False}, dict(value, refresh_token='secret'),
                    {'enabled': True, 'access': dict(value['access'], refreshToken='secret')}]:
            with self.assertRaises(ValueError): provider.validate(bad, 100)
        with self.assertRaises(ValueError): provider.validate(value, 950)
        value['access']['expiresAt'] = 2000
        self.assertEqual(provider.validate(value, 950)['expiresAt'], 2000)

    def test_direct_auth_and_endpoint_overrides_denied(self):
        for args in [['login'], ['auth'], ['--api-key=key'], ['--endpoint', 'https://other.example'], ['--insecure']]:
            with self.assertRaises(ValueError): provider.arguments_allowed(args)
        provider.arguments_allowed(['acp'])

    def test_claude_cell_token_only(self):
        home = Path('/workspace/.apc/providers/claude')
        env = {'ANTHROPIC_API_KEY': 'k', 'ANTHROPIC_BASE_URL': 'https://other.example',
               'CLAUDE_CODE_USE_BEDROCK': '1', 'CLAUDE_CONFIG_DIR': '/tmp/x', 'PATH': '/usr/bin'}
        cell = {'accessToken': 'sk-ant-oat01-apc-cell-' + 'a' * 43, 'expiresAt': 2000}
        result = provider.claude_environment(cell, home, env)
        self.assertEqual(result['CLAUDE_CODE_OAUTH_TOKEN'], cell['accessToken'])
        self.assertEqual(result['CLAUDE_CONFIG_DIR'], str(home / '.claude'))
        self.assertEqual(result['DISABLE_AUTOUPDATER'], '1')
        for key in ['ANTHROPIC_API_KEY', 'ANTHROPIC_BASE_URL', 'CLAUDE_CODE_USE_BEDROCK', 'CLAUDE_CONFIG_DIR']:
            self.assertNotIn(key, env)
        self.assertEqual(env['PATH'], '/usr/bin')
        # A real subscription token (or anything else) is refused.
        for token in ['sk-ant-oat01-' + 'b' * 60, 'sk-ant-api03-key', 'apc-cell-grok']:
            with self.assertRaises(ValueError):
                provider.claude_environment({'accessToken': token, 'expiresAt': 2000}, home, {})

    def test_claude_sign_in_and_updates_denied(self):
        for args in [['setup-token'], ['install'], ['migrate-installer'], ['update'], ['login']]:
            with self.assertRaises(ValueError): provider.arguments_allowed(args)
        provider.arguments_allowed(['-p', 'hello'])

    def test_home_and_symlink_escape_denied(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory); root = work / 'repo'; root.mkdir()
            home = work / 'home'; home.mkdir()
            extra = work / 'projects/extra'; extra.mkdir(parents=True)
            registry = work / 'projects.json'; registry.write_text(json.dumps([{'name': 'extra'}]))
            self.assertEqual(provider.enrolled_directory(extra, root, registry), extra)
            with self.assertRaises(ValueError): provider.enrolled_directory(home, root, registry)
            (root / 'escape').symlink_to(home, target_is_directory=True)
            with self.assertRaises(ValueError): provider.enrolled_directory(root / 'escape', root, registry)
            extra.rmdir(); extra.symlink_to(home, target_is_directory=True)
            with self.assertRaises(ValueError): provider.enrolled_directory(extra, root, registry)


if __name__ == '__main__': unittest.main()
