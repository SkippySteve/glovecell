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
const WEB = join(__dirname, '../../../internal/web');

// Every Content-Security-Policy the Go server sets, as the whole Go expression on its line
// (the console's policy concatenates the spinner hash, so it spans several literals).
function policies(): {file: string; expr: string}[] {
  const found: {file: string; expr: string}[] = [];
  for (const file of readdirSync(WEB).filter(f => f.endsWith('.go') && !f.endsWith('_test.go'))) {
    for (const line of readFileSync(join(WEB, file), 'utf8').split('\n')) {
      const m = line.match(/Header(?:\(\))?\.(?:Set|Add)\("Content-Security-Policy",\s*(.+)\)\s*$/);
      if (m) found.push({file, expr: m[1]});
    }
  }
  return found;
}

it('keeps every console policy free of unsafe-inline and unsafe-eval', () => {
  const found = policies();
  // server.go (the console) and sandbox_control.go (the proxied sandbox-control pages).
  expect(found.map(p => p.file).sort()).toEqual(expect.arrayContaining(['sandbox_control.go', 'server.go']));
  for (const {file, expr} of found) {
    expect(expr, file).toMatch(/style-src 'self'/);
    expect(expr, file).toMatch(/script-src 'self'/);
    expect(expr, file).not.toMatch(/unsafe-inline|unsafe-eval|unsafe-hashes|style-src-attr|style-src-elem|'nonce-|\*/);
  }
});

it('lets UI SDK add its rules as constructed style sheets, in the source and the built page', () => {
  for (const page of [join(__dirname, '../../index.html'), join(WEB, 'dist/index.html')]) {
    const html = readFileSync(page, 'utf8');
    expect(html, page).toMatch(/<meta property="csp-nonce">/);
    expect(html, page).not.toMatch(/csp-nonce"[^>]*nonce=/);
  }
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
