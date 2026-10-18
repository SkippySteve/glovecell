import {readFileSync, writeFileSync} from 'node:fs';
import {expect, it} from 'vitest';
import {agentReportPrompt} from './prompt';

const golden = new URL('../../../docs/factory/agent-report-prompt.md', import.meta.url);
const header = '<!-- Generated from web/src/factory/prompt.ts; run `UPDATE_GOLDEN=1 npx vitest run` in web/ after changing the agent library. -->\n\n';

it('matches the committed agent report prompt', () => {
  const text = header + agentReportPrompt().trimEnd() + '\n';
  if (process.env.UPDATE_GOLDEN === '1') writeFileSync(golden, text);
  expect(readFileSync(golden, 'utf8')).toBe(text);
});
