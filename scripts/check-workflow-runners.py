#!/usr/bin/env python3
"""Fail when a workflow job that can publish runs on a shared runner.

A job with `enable-openid-connect: true` gets a Forgejo Actions ID token per
pushed image, and that token is what lets an image be promoted without a
person. Such a job must run on `publish-isolated` (a fresh runner pod per job,
empty Docker daemon), and no other job may take that label. Shared runners
keep one Docker daemon across jobs, so a branch job could leave a cache layer
or a background container behind for a later main build.

Also checks that the image job's TARGETS list matches the default group in
docker-bake.hcl, so a target is never built without being gated, or the reverse.

Standard library only (CI runs it without PyYAML).
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ISOLATED = 'publish-isolated'


def jobs(text):
    """{job name: job body} from a workflow file (jobs are two-space keys under `jobs:`)."""
    _, _, body = text.partition('\njobs:\n')
    out, name, lines = {}, None, []
    for line in body.splitlines():
        m = re.match(r'^  ([A-Za-z0-9_-]+):\s*$', line)
        if m:
            if name:
                out[name] = '\n'.join(lines)
            name, lines = m.group(1), []
        elif re.match(r'^\S', line):
            break
        else:
            lines.append(line)
    if name:
        out[name] = '\n'.join(lines)
    return out


def runner(job):
    m = re.search(r'^    runs-on:\s*["\']?([\w.-]+)', job, re.M)
    return m.group(1) if m else None


def check_workflow(path, text):
    errors = []
    for name, job in jobs(text).items():
        oidc = re.search(r'^    enable-openid-connect:\s*true\b', job, re.M) is not None
        label = runner(job)
        if oidc and label != ISOLATED:
            errors.append(f'{path}: job {name} has enable-openid-connect: true but runs on {label}, not {ISOLATED}')
        if not oidc and label == ISOLATED:
            errors.append(f'{path}: job {name} runs on {ISOLATED} without enable-openid-connect (the pool is for publishing jobs)')
    return errors


def bake_targets(text, group='default'):
    m = re.search(r'group\s+"' + re.escape(group) + r'"\s*\{[^}]*targets\s*=\s*\[([^\]]*)\]', text, re.S)
    return set(re.findall(r'"([\w.-]+)"', m.group(1))) if m else set()


def check_targets(workflow_text, bake_text):
    m = re.search(r'^\s+TARGETS:\s*(.+)$', workflow_text, re.M)
    if not m:
        return []
    listed, baked = set(m.group(1).split()), bake_targets(bake_text)
    errors = []
    if listed != baked:
        errors.append(f'security.yml TARGETS {sorted(listed)} differs from docker-bake.hcl default group {sorted(baked)}')
    # Every target the gate expects must be built by one of the bake steps.
    built = set()
    for m in re.finditer(r'docker buildx bake [^\n]*?(\S+)\s*$', workflow_text, re.M):
        name = m.group(1)
        built |= bake_targets(bake_text, name) or {name}
    if 'docker buildx bake' in workflow_text and built and listed - built:
        errors.append(f'security.yml builds no bake step for {sorted(listed - built)}')
    return errors


def main():
    errors = []
    for path in sorted((ROOT / '.forgejo/workflows').glob('*.y*ml')):
        errors += check_workflow(path.relative_to(ROOT), path.read_text())
    sec, bake = ROOT / '.forgejo/workflows/security.yml', ROOT / 'docker-bake.hcl'
    if sec.exists() and bake.exists():
        errors += check_targets(sec.read_text(), bake.read_text())
    for e in errors:
        print(e, file=sys.stderr)
    return 1 if errors else 0


if __name__ == '__main__':
    sys.exit(main())
