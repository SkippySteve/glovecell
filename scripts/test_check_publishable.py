import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('check_publishable', Path(__file__).with_name('check-publishable.py'))
cp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cp)

ALLOW = cp.load_allow('''
value 10\\.96\\.\\d{1,3}\\.\\d{1,3}(/\\d{1,2})?
path fixtures/*_test.go private-key
''')

# Values that must be refused are assembled at run time, so this file itself
# passes the check and needs no exception. None of them is a real address.
def ip(*parts):
    return '.'.join(str(p) for p in parts)


def node(name, n):
    return f'{name}-node-{n}'


PEM = '-----BEGIN {}PRIVATE KEY-----'.format


def findings(line, path='x.go', allow=ALLOW):
    out = []
    cp.scan_line(line, (path, 1), allow, out)
    return out


class RefusalTest(unittest.TestCase):
    def test_private_keys_are_refused(self):
        for line in (PEM('OPENSSH '), PEM('RSA '), PEM(''), PEM('EC ')):
            self.assertIn('private-key', findings(line)[0], line)

    def test_token_formats_are_refused(self):
        for line in ('ghp_' + 'a1' * 18, 'github_pat_' + 'A' * 70, 'glpat-' + 'x' * 20,
                     'xox' + 'b-1234567890-abc', 'AKIA' + 'ABCDEFGHIJKLMNOP', 'xai-' + 'k' * 48,
                     'sk-proj-' + 'z' * 40, 'AGE-SECRET-KEY-1' + 'Q' * 58,
                     'token: ' + 'ab12' * 10, 'FORGEJO_TOKEN="' + '0f' * 20 + '"'):
            self.assertEqual(len(findings(line)), 1, line)
            self.assertIn('token', findings(line)[0])

    def test_every_private_address_outside_the_allowlist_is_refused(self):
        for value in (ip(10, 66, 5, 4), ip(10, 200, 1, 9), ip(10, 66, 0, 0) + '/16', ip(172, 20, 1, 1),
                      ip(10, 233, 64, 0) + '/18'):
            self.assertEqual(len(findings(f'addr {value} here')), 1, value)

    def test_node_names_are_refused(self):
        for value in (node('edge', '01'), node('build', 3), node('edge', 12)):
            self.assertIn('node-name', findings(f'nodeName: {value}')[0], value)

    def test_values_are_masked(self):
        line = 'ghp_' + 'S3cr3t' * 6
        self.assertNotIn('S3cr3t', ''.join(findings(line)))
        self.assertNotIn('5.4', ''.join(findings(ip(10, 66, 5, 4))))


class AllowTest(unittest.TestCase):
    def test_allowed_values_and_public_or_other_ranges_pass(self):
        for line in ('10.96.0.10', '10.96.0.0/12', '192.168.1.20', '8.8.8.8', '172.32.0.1',
                     '100.64.1.1', 'version 1.10.66.2', 'v10.66', 'ghp_short', 'token: abc',
                     'commit ' + 'ab' * 20, 'node-a', 'worker-b'):
            self.assertEqual(findings(line), [], line)

    def test_path_exception_covers_only_its_category_and_files(self):
        key = PEM('OPENSSH ')
        self.assertEqual(findings(key, 'fixtures/redact_test.go'), [])
        self.assertEqual(len(findings(key, 'cmd/main.go')), 1)
        self.assertEqual(len(findings(ip(10, 66, 1, 1), 'fixtures/redact_test.go')), 1)

    def test_allow_file_errors_stop_the_check(self):
        for text in ('values 10\\.1', 'path only-one-field', 'path a/* not-a-category', 'oops'):
            with self.assertRaises(SystemExit, msg=text):
                cp.load_allow(text)


class RangeTest(unittest.TestCase):
    def test_commit_messages_and_added_lines_are_scanned(self):
        with tempfile.TemporaryDirectory() as d:
            def git(*args):
                subprocess.run(['git', '-c', 'user.name=t', '-c', 'user.email=t@example.com', *args],
                               cwd=d, check=True, capture_output=True)
            git('init', '-q', '-b', 'main')
            Path(d, 'a.txt').write_text('clean\n')
            git('add', '.'); git('commit', '-q', '-m', 'base')
            Path(d, 'a.txt').write_text(f'clean\nnode at {ip(10, 66, 1, 9)}\n')
            git('add', '.'); git('commit', '-q', '-m', 'move to ' + node('edge', '02'))
            Path(d, 'a.txt').write_text('clean\n')
            git('add', '.'); git('commit', '-q', '-m', 'remove it again')
            out = []
            with patch.object(cp, 'ROOT', Path(d)):
                cp.scan_range('HEAD~2..HEAD', ALLOW, out)
                tree = []
                cp.scan_tree(ALLOW, tree)
            self.assertEqual(sorted(o.split(': ')[1].split()[0] for o in out), ['node-name', 'private-ip'])
            self.assertTrue(any('message' in o for o in out))
            self.assertEqual(tree, [], 'the tip is clean, but history still publishes the address')

    def test_path_exceptions_apply_to_commit_ranges(self):
        with tempfile.TemporaryDirectory() as d:
            def git(*args):
                subprocess.run(['git', '-c', 'user.name=t', '-c', 'user.email=t@example.com', *args],
                               cwd=d, check=True, capture_output=True)
            git('init', '-q', '-b', 'main')
            Path(d, 'fixtures').mkdir()
            Path(d, 'fixtures', 'redact_test.go').write_text(PEM('OPENSSH ') + '\n')
            Path(d, 'leak.go').write_text(PEM('OPENSSH ') + '\n')
            git('add', '.'); git('commit', '-q', '-m', 'keys')
            out = []
            with patch.object(cp, 'ROOT', Path(d)):
                cp.scan_range('HEAD', ALLOW, out)
            self.assertEqual(len(out), 1)
            self.assertIn('leak.go', out[0])


if __name__ == '__main__':
    unittest.main()
