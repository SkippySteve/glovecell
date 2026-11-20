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

// UI SDK adds a few behaviour rules (closed parts stay hidden, popover chrome reset) at
// runtime. With <meta property="csp-nonce"> and no nonce it adds them as constructed
// style sheets, which style-src does not govern; without the tag it would write <style>
// elements, which the policy refuses. Nothing it ships may need eval or inline styles.
it('lets UI SDK run under the console CSP without unsafe-inline or unsafe-eval', () => {
  const html = readFileSync(join(__dirname, '../../index.html'), 'utf8');
  expect(html).toMatch(/<meta property="csp-nonce">/);
  expect(html).not.toMatch(/csp-nonce"[^>]*nonce=/);
  const server = readFileSync(join(__dirname, '../../../internal/web/server.go'), 'utf8');
  const policy = server.match(/"Content-Security-Policy", "([^"]+)"/)?.[1] ?? '';
  expect(policy).toContain("style-src 'self'");
  expect(policy).not.toMatch(/unsafe-inline|unsafe-eval/);
  const files = (dir: string): string[] => readdirSync(dir, {withFileTypes: true}).flatMap(e => e.isDirectory() ? files(join(dir, e.name)) : e.name.endsWith('.js') ? [join(dir, e.name)] : []);
  const shipped = ['core', 'react'].flatMap(p => files(join(__dirname, '../../node_modules/@ui-sdk', p, 'dist')).filter(f => !f.includes('/testing')));
  expect(shipped.length).toBeGreaterThan(100);
  for (const file of shipped) {
    const code = readFileSync(file, 'utf8');
    expect(code, file).not.toMatch(/\beval\(|new Function\(|setAttribute\(\s*["']style["']/);
  }
  const style = readFileSync(join(__dirname, '../../node_modules/@ui-sdk/core/dist/foundation/platform/style.js'), 'utf8');
  expect(style).toContain('adoptedStyleSheets');
  expect(style).toContain('meta[property="csp-nonce"]');
});
