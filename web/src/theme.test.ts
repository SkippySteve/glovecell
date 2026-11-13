import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import {runInNewContext} from 'node:vm';
import {describe, expect, it} from 'vitest';
// @ts-expect-error plain ESM script without type declarations
import {checkContrast} from '../scripts/theme-css.mjs';
import {applyTheme, readTheme, THEME_KEY} from './theme';

const store = (initial: Record<string, string> = {}) => {
  const data = {...initial};
  return {data, getItem: (k: string) => (k in data ? data[k] : null), setItem: (k: string, v: string) => { data[k] = v; }};
};
const throwing = {getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); }};
const root = () => { const attrs: Record<string, string> = {}; return {attrs, setAttribute: (k: string, v: string) => { attrs[k] = v; }}; };

describe('theme choice', () => {
  it('defaults to dark and ignores unknown or unreadable values', () => {
    expect(readTheme(store())).toBe('dark');
    expect(readTheme(store({[THEME_KEY]: 'sepia'}))).toBe('dark');
    expect(readTheme(throwing)).toBe('dark');
    expect(readTheme(store({[THEME_KEY]: 'light'}))).toBe('light');
    expect(readTheme(store({[THEME_KEY]: 'system'}))).toBe('system');
  });

  it('applies to <html data-theme> and persists, even when storage is blocked', () => {
    const s = store(), el = root();
    applyTheme('light', el, s);
    expect(el.attrs['data-theme']).toBe('light');
    expect(s.data[THEME_KEY]).toBe('light');
    const blocked = root();
    expect(() => applyTheme('system', blocked, throwing)).not.toThrow();
    expect(blocked.attrs['data-theme']).toBe('system');
  });

  it('pre-paint script agrees with theme.ts', () => {
    const script = readFileSync(join(__dirname, '../public/assets/theme-init.js'), 'utf8');
    for (const [saved, expected] of [[undefined, 'dark'], ['light', 'light'], ['system', 'system'], ['dark', 'dark'], ['<img>', 'dark']] as const) {
      const el = root();
      runInNewContext(script, {localStorage: store(saved ? {[THEME_KEY]: saved} : {}), document: {documentElement: el}});
      expect(el.attrs['data-theme']).toBe(expected);
    }
    const el = root();
    runInNewContext(script, {localStorage: throwing, document: {documentElement: el}});
    expect(el.attrs['data-theme']).toBe('dark');
  });

  it('loads the pre-paint script as a same-origin file before the app', () => {
    const html = readFileSync(join(__dirname, '../index.html'), 'utf8');
    const init = html.indexOf('<script src="/assets/theme-init.js"></script>');
    expect(init).toBeGreaterThan(0);
    expect(init).toBeLessThan(html.indexOf('type="module"'));
    // No inline script or style: the CSP is script-src 'self'.
    expect(html.match(/<script(?![^>]*\bsrc=)[^>]*>/g)).toBeNull();
    expect(html).not.toMatch(/<style|style="/);
  });
});

describe('generated theme', () => {
  const css = readFileSync(join(__dirname, 'theme.css'), 'utf8');
  it('is dark by default, with light and system blocks', () => {
    expect(css).toMatch(/:root \{[^}]*color-scheme: dark;/);
    expect(css).toContain(':root[data-theme="light"] {');
    expect(css).toMatch(/@media \(prefers-color-scheme: light\) \{\s*:root\[data-theme="system"\] \{/);
    expect(css).not.toContain('prefers-color-scheme: dark');
  });
  it('meets WCAG AA for every checked token pair in both themes', () => {
    expect(checkContrast(false)).toEqual([]);
  });
  it('defines every --console-* token that pages use', () => {
    const defined = new Set([...css.matchAll(/--console-([a-z0-9-]+):/g)].map(m => m[1]));
    const sources = ['base.css', 'console.css', 'workspace/workspace.css', 'factory/factory.css'].map(f => readFileSync(join(__dirname, f), 'utf8')).join('\n');
    const used = [...new Set([...sources.matchAll(/var\(--console-([a-z0-9-]+)/g)].map(m => m[1]))];
    expect(used.filter(name => !defined.has(name))).toEqual([]);
  });
});
