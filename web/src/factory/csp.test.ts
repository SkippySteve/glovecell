import {createHash} from 'node:crypto';
import {readdirSync, readFileSync} from 'node:fs';
import {join} from 'node:path';
import {expect, it} from 'vitest';

// The console CSP allows OpenUI's injected spinner stylesheet by hash (internal/web/server.go).
const KEYFRAMES = '@keyframes openui-spin { 0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); } }';
const HASH = 'vai9tBS1dbqDlLt2vYYQwJl16j1hMfOCO5KhCK8LRBg=';

it('matches the stylesheet react-lang injects and the hash the server allows', () => {
  const dist = join(__dirname, '../../node_modules/@openuidev/react-lang/dist');
  const bundle = readdirSync(dist).filter(f => f.endsWith('.mjs')).map(f => readFileSync(join(dist, f), 'utf8')).join('\n');
  expect(bundle).toContain('style.textContent = `' + KEYFRAMES + '`');
  expect(createHash('sha256').update(KEYFRAMES).digest('base64')).toBe(HASH);
  expect(readFileSync(join(__dirname, '../../../internal/web/server.go'), 'utf8')).toContain(`'sha256-${HASH}'`);
});
