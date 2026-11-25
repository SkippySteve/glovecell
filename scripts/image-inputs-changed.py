#!/usr/bin/env python3
"""Say whether a pull request changes anything the image build reads.

Reads changed paths (one per line, `git diff --name-only`) on stdin and prints
`true` when the image job must build, `false` when every path is outside the
image build. Unknown or empty input builds: the list of skippable paths is
short on purpose, and anything not on it counts as an image input.

Skippable: docs/ except docs/factory/ (the console tests read the agent report
prompt there), Markdown and LICENSE files at the repository root, the Helm
chart (charts/, not copied into any image; CI's checks job tests it) and
GitHub-only files (.github/). Main, tags, the nightly run and manual runs
always build; .forgejo/workflows/security.yml calls this for pull requests only.
"""
import sys

ROOT_FILES = ('LICENSE',)
SKIPPABLE_DIRS = ('docs/', 'charts/', '.github/')
NOT_SKIPPABLE_DIRS = ('docs/factory/',)


def is_input(path):
    path = path.strip()
    if path.startswith(NOT_SKIPPABLE_DIRS):
        return True
    if path.startswith(SKIPPABLE_DIRS):
        return False
    if '/' not in path and (path.endswith('.md') or path in ROOT_FILES):
        return False
    return True


def must_build(paths):
    paths = [p for p in paths if p.strip()]
    return not paths or any(is_input(p) for p in paths)


if __name__ == '__main__':
    print('true' if must_build(sys.stdin.read().splitlines()) else 'false')
