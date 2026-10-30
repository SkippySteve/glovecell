// Console colour theme: dark (default), light, or system (follows the OS through
// CSS, see scripts/theme-css.mjs). public/assets/theme-init.js applies the saved
// choice before first paint with the same key and values.
export type Theme = 'dark' | 'light' | 'system';
export const THEMES: [Theme, string][] = [['dark', 'Dark'], ['light', 'Light'], ['system', 'System']];
export const THEME_KEY = 'console-theme';
export const DEFAULT_THEME: Theme = 'dark';

type Store = Pick<Storage, 'getItem' | 'setItem'>;
export const isTheme = (value: unknown): value is Theme => value === 'dark' || value === 'light' || value === 'system';

export function readTheme(store: Store | undefined = globalThis.localStorage): Theme {
  try {
    const saved = store?.getItem(THEME_KEY);
    return isTheme(saved) ? saved : DEFAULT_THEME;
  } catch {
    return DEFAULT_THEME;
  }
}

export function applyTheme(theme: Theme, root: Pick<HTMLElement, 'setAttribute'> = document.documentElement, store: Store | undefined = globalThis.localStorage) {
  root.setAttribute('data-theme', theme);
  try { store?.setItem(THEME_KEY, theme); } catch { /* private mode: the choice lasts for this page */ }
}
