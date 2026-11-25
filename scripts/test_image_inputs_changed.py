import importlib.util
from pathlib import Path
import subprocess
import sys
import unittest

spec = importlib.util.spec_from_file_location('iic', Path(__file__).with_name('image-inputs-changed.py'))
iic = importlib.util.module_from_spec(spec)
spec.loader.exec_module(iic)


class ImageInputs(unittest.TestCase):
    def test_docs_only_skips(self):
        self.assertFalse(iic.must_build(['docs/design/repository-requests.md', 'docs/design/decisions.md']))
        self.assertFalse(iic.must_build(['README.md', 'AGENTS.md', 'LICENSE', 'charts/agent-project-controller/values.yaml', '.github/x']))

    def test_image_inputs_build(self):
        for path in ['Dockerfile', 'docker-bake.hcl', '.dockerignore', 'go.mod', 'internal/web/server.go',
                     'web/src/App.tsx', 'docs/factory/report.md', '.claude/skills/ui-sdk/SKILL.md',
                     'config/session-hub/gateway.json', 'scripts/apc-fj.py', 'third_party/licenses/x',
                     '.forgejo/workflows/security.yml', 'internal/README.md', 'docsx/a.md']:
            self.assertTrue(iic.must_build(['docs/a.md', path]), path)

    def test_nothing_or_blank_builds(self):
        self.assertTrue(iic.must_build([]))
        self.assertTrue(iic.must_build(['', '  ']))

    def test_cli(self):
        script = Path(__file__).with_name('image-inputs-changed.py')
        run = lambda text: subprocess.run([sys.executable, str(script)], input=text, capture_output=True, text=True, check=True).stdout.strip()
        self.assertEqual(run('docs/a.md\n'), 'false')
        self.assertEqual(run('docs/a.md\nDockerfile\n'), 'true')
        self.assertEqual(run(''), 'true')


if __name__ == '__main__':
    unittest.main()
