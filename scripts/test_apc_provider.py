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
