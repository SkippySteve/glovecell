import {readdirSync, readFileSync} from 'node:fs';
import {join, relative} from 'node:path';
import {expect, it} from 'vitest';

// .claude/skills/ui-sdk is UI SDK's agent skill as published with the pinned
// @ui-sdk/core. Only LICENSE, NOTICE and SKILL.md's marked local section are ours;
// anything else that differs means the copy drifted from the installed version.
const VENDORED = join(__dirname, '../../../.claude/skills/ui-sdk');
const UPSTREAM = join(__dirname, '../../node_modules/@ui-sdk/core/skills/ui-sdk');
const LOCAL = /\n## Scope in this repository\n\nAdded for this repository; not part of the upstream skill \(see NOTICE\)\.[\s\S]*?\n(?=\n## )/;

const files = (dir: string): string[] => readdirSync(dir, {withFileTypes: true})
  .flatMap(e => e.isDirectory() ? files(join(dir, e.name)) : [join(dir, e.name)]);

it('matches the skill shipped with the installed @ui-sdk/core, apart from the marked local section', () => {
  const ours = files(VENDORED).map(f => relative(VENDORED, f)).filter(f => f !== 'LICENSE' && f !== 'NOTICE').sort();
  const theirs = files(UPSTREAM).map(f => relative(UPSTREAM, f)).sort();
  expect(ours).toEqual(theirs);
  for (const file of theirs) {
    let text = readFileSync(join(VENDORED, file), 'utf8');
    if (file === 'SKILL.md') {
      expect(text).toMatch(LOCAL);
      text = text.replace(LOCAL, '');
    }
    expect(text, file).toBe(readFileSync(join(UPSTREAM, file), 'utf8'));
  }
  const version = JSON.parse(readFileSync(join(UPSTREAM, '../../package.json'), 'utf8')).version;
  expect(readFileSync(join(VENDORED, 'NOTICE'), 'utf8')).toContain(`@ui-sdk/core ${version} release`);
  expect(readFileSync(join(VENDORED, 'LICENSE'), 'utf8')).toBe(readFileSync(join(UPSTREAM, '../../LICENSE'), 'utf8'));
});
