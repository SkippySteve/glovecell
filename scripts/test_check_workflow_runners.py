import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('cwr', Path(__file__).with_name('check-workflow-runners.py'))
cwr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cwr)

WF = '''name: x
on: push
jobs:
  source:
    runs-on: ubuntu-latest
    steps:
      - run: true
  image:
    runs-on: {label}
{oidc}    steps:
      - run: true
'''


def wf(label, oidc=True):
    return WF.format(label=label, oidc='    enable-openid-connect: true\n' if oidc else '')


class WorkflowRunners(unittest.TestCase):
    def test_publishing_job_on_shared_pool_fails(self):
        errors = cwr.check_workflow('w.yml', wf('ubuntu-latest'))
        self.assertEqual(len(errors), 1)
        self.assertIn('job image', errors[0])

    def test_publishing_job_on_isolated_pool_passes(self):
        self.assertEqual(cwr.check_workflow('w.yml', wf('publish-isolated')), [])

    def test_isolated_label_without_token_fails(self):
        self.assertEqual(len(cwr.check_workflow('w.yml', wf('publish-isolated', oidc=False))), 1)

    def test_targets_must_match_bake(self):
        bake = 'group "default" {\n  targets = ["a", "b"]\n}\n'
        self.assertEqual(cwr.check_targets('    TARGETS: a b\n', bake), [])
        self.assertEqual(len(cwr.check_targets('    TARGETS: a\n', bake)), 1)

    def test_this_repository(self):
        # The real workflows and docker-bake.hcl: CI fails if a publishing job moves.
        self.assertEqual(cwr.main(), 0)


if __name__ == '__main__':
    unittest.main()
