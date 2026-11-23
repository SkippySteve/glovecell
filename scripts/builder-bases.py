#!/usr/bin/env python3
"""Prebuilt builder bases for the image build.

Two stages of the Dockerfile compile third-party tools from source on every
image build: `gh` (the GitHub CLI, from gh-build) and `cloudflared` (from
cloudflared-build). They change only when their versions or Go image change,
yet they cost more CPU than the controller and console together. CI therefore
builds each once, pushes it by digest (.forgejo/workflows/builder-bases.yml)
and records the digest in ci/builder-bases.json together with a hash of the
stages it was built from. The image job (security.yml) then sets
BUILDER_BASE_<NAME> for each pinned base, and docker-bake.hcl passes it to
`docker buildx bake` as a named context, which replaces that stage.

A pin is used only while its hash matches the Dockerfile as it is now, and only
if the registry still has that digest; otherwise the stage builds from source,
as it does in every build outside CI. A pin is a reviewed change in Git, like
the digest pins of the Dockerfile's own base images.

  builder-bases.py inputs NAME         hash of NAME's stages (and the Go image)
  builder-bases.py stale               names whose pin is missing or out of date
  builder-bases.py bake-env [--verify]   BUILDER_BASE_<NAME>=... lines for usable pins
  builder-bases.py pin NAME IMAGE@sha256:...  record a pin for the current stages
  builder-bases.py check               validate ci/builder-bases.json
"""
import base64
import hashlib
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DOCKERFILE = ROOT / 'Dockerfile'
PINS = ROOT / 'ci' / 'builder-bases.json'
# Each base: the stages it is built from, the last one being the stage it replaces.
BASES = {'gh': ['gh-build', 'gh'], 'cloudflared': ['cloudflared-build', 'cloudflared']}
IMAGE = re.compile(r'^(?P<registry>[a-z0-9.-]+(?::[0-9]+)?)/(?P<path>[a-z0-9._/-]+)@(?P<digest>sha256:[0-9a-f]{64})$')
ACCEPT = ', '.join(['application/vnd.oci.image.index.v1+json', 'application/vnd.oci.image.manifest.v1+json',
                    'application/vnd.docker.distribution.manifest.list.v2+json',
                    'application/vnd.docker.distribution.manifest.v2+json'])


def stages(text):
    """{stage name: its instruction lines}, without comments and blank lines."""
    out, name, lines = {}, None, []
    for line in text.splitlines():
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        m = re.match(r'^FROM\s+\S+(?:\s+AS\s+(\S+))?\s*$', line, re.I)
        if m:
            if name:
                out[name] = lines
            name, lines = m.group(1), []
        if name or m:
            lines.append(line.rstrip())
    if name:
        out[name] = lines
    return out


def inputs(name, text=None):
    text = DOCKERFILE.read_text() if text is None else text
    found = stages(text)
    h = hashlib.sha256()
    # Global ARGs (before the first FROM) set the Go image the stages start from.
    for line in text.splitlines():
        if re.match(r'^FROM\s', line, re.I):
            break
        if re.match(r'^ARG\s', line):
            h.update(line.strip().encode() + b'\n')
    for stage in BASES[name]:
        if stage not in found:
            raise SystemExit(f'Dockerfile has no stage {stage}')
        h.update(('\n'.join(found[stage]) + '\n').encode())
    return h.hexdigest()


def load():
    return json.loads(PINS.read_text()) if PINS.exists() else {}


def check(pins):
    errors = []
    for name, pin in pins.items():
        if name not in BASES:
            errors.append(f'{name}: unknown base (known: {", ".join(BASES)})')
            continue
        if set(pin) != {'image', 'inputs'}:
            errors.append(f'{name}: needs exactly "image" and "inputs"')
        if not IMAGE.match(pin.get('image', '')):
            errors.append(f'{name}: image must be REGISTRY/PATH@sha256:<64 hex>')
        if not re.fullmatch(r'[0-9a-f]{64}', pin.get('inputs', '')):
            errors.append(f'{name}: inputs must be a sha256 hex digest')
    return errors


def stale(pins, text=None):
    return [n for n in BASES if pins.get(n, {}).get('inputs') != inputs(n, text)]


def exists(image, token):
    m = IMAGE.match(image)
    req = urllib.request.Request(f'https://{m["registry"]}/v2/{m["path"]}/manifests/{m["digest"]}', method='HEAD',
                                 headers={'Accept': ACCEPT})
    if token:
        req.add_header('Authorization', 'Basic ' + base64.b64encode(f'image-publisher:{token}'.encode()).decode())
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status == 200
    except Exception:
        return False


def bake_env(pins, verify=False, text=None, token=None, has=exists):
    args, current = [], set(BASES) - set(stale(pins, text))
    for name in BASES:
        if name not in pins:
            continue
        if name not in current:
            print(f'builder base {name}: pin is for other stages; building it from source', file=sys.stderr)
            continue
        if verify and not has(pins[name]['image'], token):
            print(f'builder base {name}: {pins[name]["image"]} not found in the registry; building it from source', file=sys.stderr)
            continue
        args.append(f'BUILDER_BASE_{name.upper()}=docker-image://{pins[name]["image"]}')
    return args


def main(argv):
    if not argv:
        raise SystemExit(__doc__)
    cmd, rest = argv[0], argv[1:]
    pins = load()
    if cmd == 'inputs' and len(rest) == 1 and rest[0] in BASES:
        print(inputs(rest[0]))
    elif cmd == 'stale' and not rest:
        print('\n'.join(stale(pins)))
    elif cmd == 'bake-env' and rest in ([], ['--verify']):
        bad = check(pins)
        if bad:
            raise SystemExit('\n'.join(bad))
        lines = bake_env(pins, verify=bool(rest), token=os.environ.get('CI_IMAGE_TOKEN'))
        if lines:
            print('\n'.join(lines))
    elif cmd == 'pin' and len(rest) == 2 and rest[0] in BASES:
        pins[rest[0]] = {'image': rest[1], 'inputs': inputs(rest[0])}
        bad = check(pins)
        if bad:
            raise SystemExit('\n'.join(bad))
        PINS.write_text(json.dumps(dict(sorted(pins.items())), indent=2) + '\n')
    elif cmd == 'check' and not rest:
        bad = check(pins)
        if bad:
            raise SystemExit('\n'.join(bad))
    else:
        raise SystemExit(__doc__)


if __name__ == '__main__':
    main(sys.argv[1:])
