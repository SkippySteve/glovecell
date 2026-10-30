import {type ReactNode, useEffect, useState} from 'react';
import {applyTheme, isTheme, readTheme, THEME_KEY, THEMES, type Theme} from './theme';

const ICONS: Record<Theme, ReactNode> = {
  dark: <path d="M13.5 10.2A5.5 5.5 0 0 1 5.8 2.5a5.5 5.5 0 1 0 7.7 7.7Z" />,
  light: <><circle cx="8" cy="8" r="3" /><path d="M8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1 1M11.6 11.6l1 1M3.4 12.6l1-1M11.6 4.4l1-1" /></>,
  system: <><rect x="1.5" y="2.5" width="13" height="9" rx="1.5" /><path d="M5.5 14h5M8 11.5V14" /></>,
};

// Light, dark or system, in the header. The choice is kept in localStorage and
// applied to <html data-theme>; every colour comes from the generated theme.css.
export function ThemeSwitcher() {
  const [theme, setTheme] = useState<Theme>(readTheme);
  useEffect(() => {
    // Another tab changed it.
    const sync = (event: StorageEvent) => { if (event.key === THEME_KEY && isTheme(event.newValue)) { setTheme(event.newValue); document.documentElement.setAttribute('data-theme', event.newValue); } };
    window.addEventListener('storage', sync);
    return () => window.removeEventListener('storage', sync);
  }, []);
  const choose = (next: Theme) => { setTheme(next); applyTheme(next); };
  return (
    <div className="theme-switcher" role="group" aria-label="Colour theme">
      {THEMES.map(([value, label]) => (
        <button key={value} type="button" className="theme-option" aria-pressed={theme === value} title={label + ' theme'} onClick={() => choose(value)}>
          <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">{ICONS[value]}</svg>
          <span className="visually-hidden">{label}</span>
        </button>
      ))}
    </div>
  );
}
