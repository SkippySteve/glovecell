import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('bb', Path(__file__).with_name('builder-bases.py'))
bb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bb)

DF = '''ARG GO_IMAGE=golang:1@sha256:aaaa
FROM ${GO_IMAGE} AS gh-build
# a comment
RUN build gh
FROM scratch AS gh
COPY --from=gh-build /gh /gh
FROM ${GO_IMAGE} AS cloudflared-build
RUN build cloudflared
FROM scratch AS cloudflared
COPY --from=cloudflared-build /cloudflared /cloudflared
FROM debian AS workspace
COPY --from=gh /gh /usr/local/bin/gh
'''
IMG = 'registry.example/image-publisher/glovecell-builder-gh@sha256:' + 'b' * 64


def pins_for(text, name='gh', image=IMG):
    return {name: {'image': image, 'inputs': bb.inputs(name, text)}}


class BuilderBases(unittest.TestCase):
    def test_hash_ignores_comments_and_other_stages(self):
        h = bb.inputs('gh', DF)
        self.assertEqual(h, bb.inputs('gh', DF.replace('# a comment', '# changed').replace('FROM debian', 'FROM alpine')))

    def test_hash_follows_stage_and_go_image(self):
        h = bb.inputs('gh', DF)
        self.assertNotEqual(h, bb.inputs('gh', DF.replace('RUN build gh', 'RUN build gh v2')))
        self.assertNotEqual(h, bb.inputs('gh', DF.replace('sha256:aaaa', 'sha256:cccc')))
        self.assertNotEqual(h, bb.inputs('gh', DF.replace('COPY --from=gh-build /gh /gh', 'COPY --from=gh-build /gh /x')))

    def test_current_pin_gives_context(self):
        args = bb.bake_env(pins_for(DF), text=DF)
        self.assertEqual(args, ['BUILDER_BASE_GH=docker-image://' + IMG])

    def test_stale_pin_builds_from_source(self):
        pins = pins_for(DF)
        changed = DF.replace('RUN build gh', 'RUN build gh v2')
        self.assertEqual(bb.bake_env(pins, text=changed), [])
        self.assertIn('gh', bb.stale(pins, changed))

    def test_missing_image_builds_from_source(self):
        self.assertEqual(bb.bake_env(pins_for(DF), verify=True, text=DF, has=lambda image, token: False), [])
        self.assertEqual(len(bb.bake_env(pins_for(DF), verify=True, text=DF, has=lambda image, token: True)), 1)

    def test_check_refuses_bad_pins(self):
        self.assertEqual(bb.check(pins_for(DF)), [])
        for bad in [{'other': {'image': IMG, 'inputs': 'a' * 64}},
                    {'gh': {'image': 'registry.example/x:latest', 'inputs': 'a' * 64}},
                    {'gh': {'image': IMG, 'inputs': 'short'}},
                    {'gh': {'image': IMG, 'inputs': 'a' * 64, 'extra': 1}}]:
            self.assertTrue(bb.check(bad), bad)

    def test_bake_file_declares_every_base(self):
        bake = (Path(__file__).resolve().parents[1] / 'docker-bake.hcl').read_text()
        for name in bb.BASES:
            self.assertIn(f'variable "BUILDER_BASE_{name.upper()}"', bake)
            self.assertIn(f'{name} = BUILDER_BASE_{name.upper()}', bake)

    def test_repository_dockerfile_has_every_base(self):
        for name in bb.BASES:
            self.assertRegex(bb.inputs(name), '^[0-9a-f]{64}$')
        self.assertEqual(bb.check(bb.load()), [])


if __name__ == '__main__':
    unittest.main()
