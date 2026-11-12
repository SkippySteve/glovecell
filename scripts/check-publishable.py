#!/usr/bin/env python3
"""Refuse content that must not reach the public Glovecell repository.

Forgejo main is mirrored to GitHub on every commit, so whatever lands on main
becomes public. This check runs in the required `checks` job and fails on:

  private-key   a PEM/OpenSSH private key block
  token         a known credential format (GitHub, GitLab, Slack, AWS, xAI, OpenAI,
                age secret keys, or a 40-hex value right after the word "token")
  private-ip    a 10.x or 172.16-31.x address outside the documentation examples
                in .publish-allow (the deployment's own LAN never appears here
                by name: every private address is refused unless allowed)
  node-name     a node host name such as <name>-node-<n>
  personal-email  an address at a personal mailbox provider (Gmail, Outlook,
                iCloud, Proton, ...). Commits on main carry project or noreply
                addresses only; the maintainer's Forgejo account keeps its
                email private, so merge commits use its noreply address.

It scans the files at HEAD (tracked, text) and, with --range, the messages,
added lines and author/committer of the commits in that range. --last-commit
checks the message and author/committer of HEAD only (CI on main: the merge
commit Forgejo created). Exceptions live in .publish-allow:
`value <regex>` allows a matched value anywhere, `path <glob> <category>`
allows one category in matching files (test fixtures with fake keys).
Matches are reported by file, line and category; values are masked.

  scripts/check-publishable.py                     # tree at HEAD
  scripts/check-publishable.py --range origin/main..HEAD
  scripts/check-publishable.py --last-commit
"""
import argparse
import fnmatch
import ipaddress
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ALLOW_FILE = '.publish-allow'

PATTERNS = {
    'private-key': re.compile(r'-----BEGIN (?:[A-Z]+ )*PRIVATE KEY-----'),
    'token': re.compile(
        r'\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,}|glpat-[A-Za-z0-9_-]{20,}'
        r'|xox[abposr]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|xai-[A-Za-z0-9]{40,}'
        r'|sk-(?:proj-|ant-)?[A-Za-z0-9_-]{32,}|AGE-SECRET-KEY-1[0-9A-Z]{58})\b'
        r'|(?i:token)["\'\s:=]{1,4}[0-9a-f]{40}\b'),
    'private-ip': re.compile(r'(?<![\d.])(?:10|172)\.\d{1,3}\.\d{1,3}\.\d{1,3}(?:/\d{1,2})?(?![\d.]*\d)'),
    'node-name': re.compile(r'\b[a-z][a-z0-9]*-node-\d+\b'),
    'personal-email': re.compile(
        r'[A-Za-z0-9._%+-]+@(?:gmail|googlemail|outlook|hotmail|live|msn|yahoo|ymail|icloud|me|mac'
        r'|aol|proton|protonmail|pm|gmx|yandex|zoho|fastmail|hey)\.(?:com|net|me|de|ch|ru|co\.uk)\b', re.I),
}


def load_allow(text):
    values, paths = [], []
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith('#'):
            continue
        kind, _, rest = line.partition(' ')
        if kind == 'value' and rest:
            values.append(re.compile(rest.strip()))
        elif kind == 'path' and len(rest.split()) == 2:
            glob, category = rest.split()
            if category not in PATTERNS:
                raise SystemExit(f'{ALLOW_FILE}: unknown category {category!r}')
            paths.append((glob, category))
        else:
            raise SystemExit(f'{ALLOW_FILE}: cannot parse {raw!r}')
    return values, paths


def is_private_ip(value):
    try:
        net = ipaddress.ip_network(value, strict=False)
    except ValueError:
        return False
    return net.subnet_of(ipaddress.ip_network('10.0.0.0/8')) or net.subnet_of(ipaddress.ip_network('172.16.0.0/12'))


def mask(value):
    return value[:4] + '…' if len(value) > 6 else '…'


def scan_line(line, where, allow, out, path=None):
    """where = (label, line number); path (default: the label) is matched against path exceptions."""
    values, paths = allow
    path = where[0] if path is None else path
    for category, pattern in PATTERNS.items():
        if any(fnmatch.fnmatch(path, glob) for glob, c in paths if c == category):
            continue
        for m in pattern.finditer(line):
            value = m.group(0)
            if category == 'private-ip' and not is_private_ip(value):
                continue
            if any(v.fullmatch(value) for v in values):
                continue
            out.append(f'{where[0]}:{where[1]}: {category} {mask(value)}')


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, check=True, capture_output=True, text=True).stdout


def scan_tree(allow, out):
    for path in git('ls-files', '-z').split('\0'):
        if not path or path == ALLOW_FILE:
            continue
        data = (ROOT / path).read_bytes() if (ROOT / path).is_file() else b''
        if b'\0' in data[:8192]:
            continue
        for number, line in enumerate(data.decode('utf-8', 'replace').splitlines(), 1):
            scan_line(line, (path, number), allow, out)


def scan_commit_meta(sha, allow, out):
    """Author, committer and message (Reviewed-by and Co-authored-by trailers included)."""
    identities = git('show', '-s', '--format=%an <%ae>%n%cn <%ce>', sha)
    for number, line in enumerate(identities.splitlines(), 1):
        scan_line(line, (f'commit {sha[:12]} {("author", "committer")[number - 1]}', number), allow, out)
    message = git('show', '-s', '--format=%B', sha)
    for number, line in enumerate(message.splitlines(), 1):
        scan_line(line, (f'commit {sha[:12]} message', number), allow, out)


def scan_range(rev_range, allow, out):
    for sha in git('rev-list', rev_range).split():
        scan_commit_meta(sha, allow, out)
        path = None
        for line in git('show', '--format=', '--unified=0', '--no-color', sha).splitlines():
            if line.startswith('+++ '):
                path = line[6:] if line.startswith('+++ b/') else None
            elif line.startswith('+') and path and path != ALLOW_FILE:
                scan_line(line[1:], (f'commit {sha[:12]} {path}', '+'), allow, out, path)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--range', help='also scan these commits, e.g. origin/main..HEAD')
    parser.add_argument('--last-commit', action='store_true', help='also scan the message and author/committer of HEAD')
    parser.add_argument('--no-tree', action='store_true', help='skip the files at HEAD')
    args = parser.parse_args(argv)
    allow_path = ROOT / ALLOW_FILE
    allow = load_allow(allow_path.read_text() if allow_path.exists() else '')
    out = []
    if not args.no_tree:
        scan_tree(allow, out)
    if args.range:
        scan_range(args.range, allow, out)
    if args.last_commit:
        scan_commit_meta(git('rev-parse', 'HEAD').strip(), allow, out)
    for line in out:
        print(line)
    if out:
        print(f'{len(out)} finding(s): this repository is published to GitHub. Remove them, or add a '
              f'reviewed exception to {ALLOW_FILE} if the value is a documentation example.', file=sys.stderr)
        return 1
    print('publishable: no private keys, tokens, private addresses, node names or personal email addresses')
    return 0


if __name__ == '__main__':
    sys.exit(main())
