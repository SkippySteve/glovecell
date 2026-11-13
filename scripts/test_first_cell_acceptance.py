import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('first_cell', Path(__file__).with_name('qualify-first-cell.py'))
q = importlib.util.module_from_spec(spec)
spec.loader.exec_module(q)
IMAGE = 'registry.example/runtime@sha256:' + 'a' * 64


def obj(uid='fixture-uid'):
    return {'metadata': {'name': 'qualification-fixture', 'uid': uid, 'resourceVersion': '1', 'generation': 1},
            'spec': {'repository': 'owner/disposable-fixture'},
            'status': {'phase': 'Ready', 'observedGeneration': 1, 'namespace': 'apc-' + uid.replace('-', '')}}


class Console:
    def __init__(self):
        self.object = obj()
        self.mutations = []

    def call(self, path, method='GET', body=None):
        if path == 'workspaces':
            return [self.object]
        if method == 'PATCH':
            self.mutations.append(body['desiredState'])
            self.object['status']['phase'] = 'Ready' if body['desiredState'] == 'Running' else 'Paused'
            return self.object
        raise AssertionError('unexpected API')


class AcceptanceTests(unittest.TestCase):
    def test_partial_or_missing_evidence_cannot_pass(self):
        for state in ['blocked', 'not-run', 'failed']:
            report = {'checks': {'first': {'result': 'passed'}, 'missing': {'result': state}}}
            self.assertNotEqual(q.conclude(report), 'passed')
        self.assertEqual(q.conclude({'checks': {}}), 'incomplete')

    def test_disposable_repository_and_uid_binding(self):
        for name, repo, uid in [('production', 'owner/disposable-fixture', None),
                                ('qualification-fixture', 'owner/live-app', None),
                                ('qualification-fixture', 'owner/disposable-fixture', 'replacement')]:
            with self.assertRaises(q.Blocked):
                q.disposable(obj(), name, repo, uid)
        self.assertEqual(q.disposable(obj(), 'qualification-fixture', 'owner/disposable-fixture'), 'fixture-uid')

    def test_ready_only_and_lifecycle_success_never_imply_full_acceptance(self):
        console = Console()
        workers = [{'pod': 'before', 'podUid': 'one', 'image': IMAGE, 'runtime': 'agent-gvisor'},
                   {'pod': 'after', 'podUid': 'two', 'image': IMAGE, 'runtime': 'agent-gvisor'}]
        with patch.object(q, 'worker', side_effect=workers), patch.object(q, 'remote', side_effect=AssertionError('unrequested exec')):
            report = q.qualify(console, 'qualification-fixture', 'owner/disposable-fixture', IMAGE, 'agent-gvisor', 10)
        self.assertEqual(report['result'], 'incomplete')
        self.assertEqual(report['checks']['pause-resume']['result'], 'passed')
        self.assertEqual(report['checks']['provider-access']['result'], 'not-run')
        self.assertEqual(report['checks']['upload-readback']['result'], 'not-run')
        self.assertEqual(console.mutations, ['Paused', 'Running', 'Paused'])

    def test_wrong_worker_image_stops_before_any_mutation(self):
        console = Console()
        with patch.object(q, 'worker', return_value={'image': 'wrong', 'runtime': 'agent-gvisor'}):
            report = q.qualify(console, 'qualification-fixture', 'owner/disposable-fixture', IMAGE, 'agent-gvisor', 10)
        self.assertEqual(report['result'], 'failed')
        self.assertEqual(console.mutations, [])

    def test_same_pod_is_not_proof_of_pause_resume(self):
        console = Console()
        with patch.object(q, 'worker', return_value={'pod': 'worker', 'podUid': 'one', 'image': IMAGE, 'runtime': 'agent-gvisor'}):
            report = q.qualify(console, 'qualification-fixture', 'owner/disposable-fixture', IMAGE, 'agent-gvisor', 10)
        self.assertEqual(report['checks']['pause-resume']['result'], 'failed')
        self.assertEqual(report['result'], 'failed')

    def test_persistence_hash_mismatch_fails_even_after_successful_cycle(self):
        console = Console()
        workers = [{'pod': 'before', 'podUid': 'one', 'image': IMAGE, 'runtime': 'agent-gvisor'},
                   {'pod': 'after', 'podUid': 'two', 'image': IMAGE, 'runtime': 'agent-gvisor'}]
        def remote(ns, pod, program, args=(), stdin=None):
            import hashlib
            if program == q.REPO:
                return {'branch': 'agent/apc-fixture', 'repositoryMatches': True}
            if pod == 'after':
                return {'sha256': 'corrupt'}
            if stdin is not None:
                self.digest = hashlib.sha256(stdin).hexdigest()
            return {'sha256': self.digest}
        with patch.object(q, 'worker', side_effect=workers), patch.object(q, 'remote', side_effect=remote):
            report = q.qualify(console, 'qualification-fixture', 'owner/disposable-fixture', IMAGE, 'agent-gvisor', 10, True)
        self.assertEqual(report['checks']['file-persistence']['result'], 'failed')
        self.assertEqual(report['result'], 'failed')

    def test_credential_permissions_and_symlinks_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'credential'
            path.write_text('fixture-secret')
            path.chmod(0o600)
            self.assertEqual(q.private_credential(path), 'fixture-secret')
            path.chmod(0o644)
            with self.assertRaises(q.Blocked):
                q.private_credential(path)
            link = Path(directory) / 'link'
            link.symlink_to(path)
            with self.assertRaises(OSError):
                q.private_credential(link)

    def test_console_origin_refuses_credential_or_cross_path_targets(self):
        for value in ['http://fixture.invalid', 'https://user:password@fixture.invalid', 'https://fixture.invalid/elsewhere']:
            with self.assertRaises(q.Blocked):
                q.Console(value, 'fixture-secret')
        self.assertIsNone(q.NoRedirect().redirect_request(None, None, None, None, None, None))


if __name__ == '__main__':
    unittest.main()
