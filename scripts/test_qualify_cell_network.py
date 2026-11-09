import importlib.util
import subprocess
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('network_qualification', Path(__file__).with_name('qualify-cell-network.py'))
q = importlib.util.module_from_spec(spec)
spec.loader.exec_module(q)


class NetworkQualificationTests(unittest.TestCase):
    def test_dead_control_never_proves_denial(self):
        calls = []
        def probe(target, request):
            calls.append(target)
            return False
        result = q.qualify([('dns', {'kind': 'resolve'}, False)], 'worker', 'control', probe)
        self.assertEqual(result[0]['result'], 'inconclusive')
        self.assertEqual(calls, ['control'])

    def test_allow_and_deny_are_both_required(self):
        cases = [('allow', {'kind': 'https', 'via_proxy': True}, True), ('deny', {'kind': 'tcp'}, False)]
        for reachable, expected in [(False, ['fail', 'pass']), (True, ['pass', 'fail'])]:
            def probe(target, request):
                if target == 'control':
                    self.assertFalse(request['via_proxy'])
                    return True
                return reachable
            self.assertEqual([r['result'] for r in q.qualify(cases, 'worker', 'control', probe)], expected)

    def test_exec_errors_are_not_denials(self):
        for status, output in [(1, ''), (0, 'not JSON'), (0, '{"reachable": "false"}'), (0, '{}')]:
            def execute(*a, **kw):
                return subprocess.CompletedProcess(a, status, output, 'sensitive error')
            with self.assertRaises(RuntimeError):
                q.run_probe(('ns', 'pod', 'container'), {}, execute)

    def test_exec_uses_arguments_without_shell(self):
        def execute(command, **kwargs):
            self.assertEqual(command[:8], ['kubectl', '-n', 'ns', 'exec', 'pod', '-c', 'workspace', '--'])
            self.assertNotIn('shell', kwargs)
            return subprocess.CompletedProcess(command, 0, '{"reachable":false}', '')
        self.assertFalse(q.run_probe(('ns', 'pod', 'workspace'), {'host': 'a; echo secret'}, execute))

    def test_remote_program_compiles(self):
        compile(q.PROBE, '<probe>', 'exec')

class RemoteProbeTests(unittest.TestCase):
    def run_remote(self, request, opener=None, connect=None):
        import contextlib
        import io
        import json
        from unittest.mock import patch
        output = io.StringIO()
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch('sys.argv', ['probe', json.dumps(request)]))
            stack.enter_context(contextlib.redirect_stdout(output))
            if opener is not None:
                stack.enter_context(patch('urllib.request.build_opener', return_value=opener))
            if connect is not None:
                stack.enter_context(patch('socket.create_connection', side_effect=connect))
            exec(compile(q.PROBE, '<probe>', 'exec'), {})
        return json.loads(output.getvalue())

    def test_http_errors_do_not_all_count_as_denial(self):
        import urllib.error
        from unittest.mock import Mock
        for code in [403, 404, 500]:
            opener = Mock()
            opener.open.side_effect = urllib.error.HTTPError('https://fixture.invalid', code, 'secret', {}, None)
            result = self.run_remote({'kind': 'https', 'url': 'https://fixture.invalid', 'via_proxy': False}, opener)
            self.assertEqual(result, {'reachable': False} if code == 403 else {'error': 'unexpected-http-status'})

    def test_html_is_not_a_successful_doh_response(self):
        from unittest.mock import MagicMock
        opener = MagicMock()
        response = opener.open.return_value.__enter__.return_value
        response.status = 200
        response.read.return_value = b'<html>not DNS</html>'
        response.headers.get_content_type.return_value = 'text/html'
        self.assertEqual(self.run_remote({'kind': 'doh', 'url': 'https://fixture.invalid', 'via_proxy': False}, opener), {'error': 'probe-error'})

    def test_socket_denial_and_interrupted_execution(self):
        for error, expected in [(ConnectionRefusedError(), {'reachable': False}), (OSError('secret'), {'error': 'probe-error'})]:
            self.assertEqual(self.run_remote({'kind': 'tcp', 'host': '127.0.0.1', 'port': 1}, connect=error), expected)


if __name__ == '__main__':
    unittest.main()
