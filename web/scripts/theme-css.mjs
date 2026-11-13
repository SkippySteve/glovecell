// Writes the console's theme as static CSS (src/theme.css). OpenUI's
// ThemeProvider injects a <style> element at runtime, which the console's
// style-src 'self' policy refuses, so both themes are generated here instead.
//
//   node scripts/theme-css.mjs           write src/theme.css, fail on a contrast miss
//   node scripts/theme-css.mjs --check   print every checked pair as well
//
// Material: Dusk after dark (blue slate, elevation is lightness, periwinkle
// accent) and Cobalt by day (cool near-white paper, blue-grey ink, cobalt
// accent). One hue family (262 to 270) in both, so switching never re-colours
// meaning. Dark is the default. <html data-theme="light|dark|system"> picks the
// theme; public/assets/theme-init.js sets it before first paint.
import {createRequire} from 'node:module';
import {writeFileSync} from 'node:fs';

const require = createRequire(import.meta.url);
const {defaultDarkTheme, defaultLightTheme} = require('../node_modules/@openuidev/react-ui/dist/components/ThemeProvider/index.cjs');

// Console palette. Every colour on the page derives from these.
const H = 264; // neutral hue
const A = 268; // accent hue
const dark = {
  'bg': `oklch(0.165 0.012 ${H})`,
  'surface': `oklch(0.200 0.014 ${H})`,
  'surface-2': `oklch(0.240 0.016 ${H})`,
  'surface-sunk': `oklch(0.140 0.011 ${H})`,
  'border': `oklch(0.300 0.016 ${H})`,
  'border-strong': `oklch(0.520 0.020 ${H})`,
  'overlay': `oklch(0.08 0.02 ${H} / 0.7)`,
  'text': `oklch(0.950 0.006 ${H})`,
  'text-muted': `oklch(0.790 0.014 ${H})`,
  'text-subtle': `oklch(0.665 0.016 ${H})`,
  'text-link': `oklch(0.810 0.095 ${A})`,
  'accent': `oklch(0.540 0.165 ${A})`,
  'accent-hover': `oklch(0.500 0.165 ${A})`,
  'on-accent': `oklch(0.990 0.004 ${A})`,
  'accent-text': `oklch(0.810 0.095 ${A})`,
  'accent-soft': `oklch(0.300 0.055 ${A})`,
  'focus': `oklch(0.800 0.110 ${A})`,
  'ok': 'oklch(0.810 0.120 158)', 'ok-bg': 'oklch(0.255 0.040 158)', 'ok-border': 'oklch(0.480 0.090 158)',
  'warn': 'oklch(0.850 0.115 78)', 'warn-bg': 'oklch(0.265 0.040 78)', 'warn-border': 'oklch(0.520 0.095 78)',
  'danger': 'oklch(0.780 0.120 22)', 'danger-bg': 'oklch(0.245 0.038 22)', 'danger-border': 'oklch(0.520 0.130 22)',
  'info': 'oklch(0.810 0.085 240)', 'info-bg': 'oklch(0.255 0.040 240)', 'info-border': 'oklch(0.500 0.090 240)',
  'neutral': `oklch(0.810 0.014 ${H})`, 'neutral-bg': `oklch(0.255 0.014 ${H})`, 'neutral-border': `oklch(0.400 0.018 ${H})`,
  'danger-solid': 'oklch(0.540 0.170 25)', 'danger-solid-hover': 'oklch(0.500 0.165 25)', 'on-danger': 'oklch(0.990 0.004 25)',
  'shadow-1': 'none',
  'shadow-2': `0 0 0 1px oklch(0.30 0.016 ${H}), 0 12px 32px -8px oklch(0.05 0.02 ${H} / 0.7)`,
};
const light = {
  'bg': `oklch(0.972 0.005 ${H})`,
  'surface': `oklch(0.996 0.002 ${H})`,
  'surface-2': `oklch(0.950 0.007 ${H})`,
  'surface-sunk': `oklch(0.960 0.006 ${H})`,
  'border': `oklch(0.900 0.009 ${H})`,
  'border-strong': `oklch(0.600 0.020 ${H})`,
  'overlay': `oklch(0.21 0.028 ${H} / 0.45)`,
  'text': `oklch(0.210 0.028 ${H})`,
  'text-muted': `oklch(0.390 0.022 ${H})`,
  'text-subtle': `oklch(0.490 0.020 ${H})`,
  'text-link': `oklch(0.480 0.180 ${A})`,
  'accent': `oklch(0.520 0.190 ${A})`,
  'accent-hover': `oklch(0.460 0.190 ${A})`,
  'on-accent': `oklch(0.990 0.004 ${A})`,
  'accent-text': `oklch(0.480 0.180 ${A})`,
  'accent-soft': `oklch(0.935 0.032 ${A})`,
  'focus': `oklch(0.520 0.190 ${A})`,
  'ok': 'oklch(0.450 0.110 155)', 'ok-bg': 'oklch(0.950 0.035 155)', 'ok-border': 'oklch(0.740 0.085 155)',
  'warn': 'oklch(0.480 0.100 62)', 'warn-bg': 'oklch(0.955 0.045 85)', 'warn-border': 'oklch(0.760 0.110 75)',
  'danger': 'oklch(0.500 0.180 27)', 'danger-bg': 'oklch(0.955 0.025 25)', 'danger-border': 'oklch(0.750 0.100 25)',
  'info': 'oklch(0.480 0.130 250)', 'info-bg': 'oklch(0.950 0.025 250)', 'info-border': 'oklch(0.750 0.075 250)',
  'neutral': `oklch(0.390 0.022 ${H})`, 'neutral-bg': `oklch(0.945 0.008 ${H})`, 'neutral-border': `oklch(0.840 0.012 ${H})`,
  'danger-solid': 'oklch(0.530 0.190 27)', 'danger-solid-hover': 'oklch(0.480 0.180 27)', 'on-danger': 'oklch(0.990 0.004 25)',
  'shadow-1': `0 1px 2px oklch(0.21 0.028 ${H} / 0.06)`,
  'shadow-2': `0 0 0 1px oklch(0.21 0.028 ${H} / 0.08), 0 12px 32px -8px oklch(0.21 0.028 ${H} / 0.22)`,
};

// Theme-independent tokens: type, spacing, radii, sizes, motion.
const shared = {
  'font': 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", "Noto Sans", sans-serif',
  'font-mono': 'ui-monospace, "SF Mono", "Cascadia Mono", "JetBrains Mono", Menlo, Consolas, monospace',
  'text-xs': '12px', 'text-sm': '13px', 'text-base': '14px', 'text-md': '16px', 'text-lg': '18px', 'text-xl': '22px',
  'leading': '1.5', 'leading-tight': '1.25', 'weight-medium': '500', 'weight-bold': '600',
  'space-1': '4px', 'space-2': '8px', 'space-3': '12px', 'space-4': '16px', 'space-5': '20px', 'space-6': '24px', 'space-8': '32px', 'space-10': '40px',
  'radius-sm': '4px', 'radius': '6px', 'radius-lg': '8px', 'radius-pill': '999px',
  'control-h': '32px', 'control-h-sm': '28px', 'header-h': '48px',
  'focus-ring': '2px solid var(--console-focus)',
  'dur-micro': '100ms', 'dur-short': '180ms', 'ease': 'cubic-bezier(0.2, 0, 0, 1)',
  'open': 'var(--console-danger)',
};

// OpenUI's variables, re-derived from the palette so its components match.
const c = name => `var(--console-${name})`;
const mix = (name, pct) => `color-mix(in oklab, var(--console-${name}) ${pct}%, transparent)`;
function openuiColours() {
  const ink = 'text';
  return {
    'background': c('bg'), 'foreground': c('surface'), 'popover-background': c('surface-2'),
    'sunk-light': mix(ink, 2), 'sunk': c('surface-sunk'), 'sunk-deep': mix(ink, 10),
    'elevated-light': mix(ink, 3), 'elevated': c('surface-2'), 'elevated-strong': mix(ink, 14), 'elevated-intense': mix(ink, 30),
    'overlay': c('overlay'),
    'highlight-subtle': mix(ink, 2), 'highlight': mix(ink, 6), 'highlight-strong': mix(ink, 10), 'highlight-intense': mix(ink, 30),
    'inverted-background': c('text'),
    'info-background': c('info-bg'), 'success-background': c('ok-bg'), 'alert-background': c('warn-bg'), 'danger-background': c('danger-bg'),
    'text-neutral-primary': c('text'), 'text-neutral-secondary': c('text-muted'), 'text-neutral-tertiary': c('text-subtle'), 'text-neutral-link': c('text-link'),
    'text-brand': c('accent-text'),
    // OpenUI uses text-accent-* as the ink on accent fills (primary buttons, toggles, checkboxes).
    'text-accent-primary': c('on-accent'), 'text-accent-secondary': mix('on-accent', 70), 'text-accent-tertiary': mix('on-accent', 45),
    'text-success-primary': c('ok'), 'text-alert-primary': c('warn'), 'text-danger-primary': c('danger'), 'text-info-primary': c('info'),
    'text-danger-secondary': c('danger'), 'text-danger-tertiary': mix('danger', 60),
    'text-danger-inverted-primary': c('on-danger'), 'text-danger-inverted-secondary': mix('on-danger', 70), 'text-danger-inverted-tertiary': mix('on-danger', 45),
    'interactive-accent-default': c('accent'), 'interactive-accent-hover': c('accent-hover'), 'interactive-accent-pressed': c('accent-hover'), 'interactive-accent-disabled': mix('accent', 45),
    'interactive-destructive-default': 'transparent', 'interactive-destructive-hover': c('danger-bg'), 'interactive-destructive-pressed': c('danger-bg'), 'interactive-destructive-disabled': 'transparent',
    'interactive-destructive-accent-default': c('danger-solid'), 'interactive-destructive-accent-hover': c('danger-solid-hover'), 'interactive-destructive-accent-pressed': c('danger-solid-hover'), 'interactive-destructive-accent-disabled': mix('danger-solid', 45),
    'border-default': c('border'), 'border-interactive': c('border-strong'), 'border-interactive-emphasis': c('text-subtle'), 'border-interactive-selected': c('text'),
    'border-accent': 'transparent', 'border-accent-emphasis': c('focus'),
    'border-info': c('info-border'), 'border-info-emphasis': c('info'), 'border-alert': c('warn-border'), 'border-alert-emphasis': c('warn'),
    'border-success': c('ok-border'), 'border-success-emphasis': c('ok'), 'border-danger': c('danger-border'), 'border-danger-emphasis': c('danger'),
    'shadow-s': c('shadow-1'), 'shadow-m': c('shadow-1'), 'shadow-l': c('shadow-2'), 'shadow-xl': c('shadow-2'), 'shadow-2xl': c('shadow-2'), 'shadow-3xl': c('shadow-2'),
  };
}
// Type: one system family, a 1.125 to 1.2 scale, OpenUI's shorthands rebuilt on it.
const f = (weight, size, lh, family = 'var(--console-font)') => `${weight} var(--console-${size})/${lh} ${family}`;
const openuiShared = {
  'font-body': c('font'), 'font-heading': c('font'), 'font-label': c('font'), 'font-numbers': c('font'), 'font-code': c('font-mono'),
  'font-weight-regular': '400', 'font-weight-medium': '500', 'font-weight-bold': '600', 'font-weight-heavy': '650',
  'radius-s': '4px', 'radius-m': '6px', 'radius-l': '6px', 'radius-xl': '8px', 'radius-2xl': '8px', 'radius-3xl': '10px',
  'text-body-xs': f(400, 'text-xs', 1.5), 'text-body-xs-heavy': f(500, 'text-xs', 1.5),
  'text-body-sm': f(400, 'text-sm', 1.5), 'text-body-sm-heavy': f(500, 'text-sm', 1.5),
  'text-body-default': f(400, 'text-base', 1.5), 'text-body-default-heavy': f(500, 'text-base', 1.5),
  'text-body-lg': f(400, 'text-md', 1.5), 'text-body-lg-heavy': f(500, 'text-md', 1.5),
  'text-heading-xs': f(600, 'text-base', 1.3), 'text-heading-sm': f(600, 'text-md', 1.3),
  'text-heading-md': f(600, 'text-lg', 1.25), 'text-heading-lg': f(600, 'text-xl', 1.2), 'text-heading-xl': f(600, 'text-xl', 1.2),
  'text-label-xs': f(400, 'text-xs', 1.25), 'text-label-xs-heavy': f(500, 'text-xs', 1.25),
  'text-label-sm': f(400, 'text-sm', 1.25), 'text-label-sm-heavy': f(500, 'text-sm', 1.25),
  'text-label-default': f(400, 'text-base', 1.25), 'text-label-default-heavy': f(500, 'text-base', 1.25),
  'text-label-lg': f(400, 'text-md', 1.25), 'text-label-lg-heavy': f(500, 'text-md', 1.25),
  'text-numbers-xs': f(400, 'text-xs', 1.5), 'text-numbers-sm': f(400, 'text-sm', 1.5), 'text-numbers-default': f(400, 'text-base', 1.5),
  'text-code-sm': f(400, 'text-xs', 1.5, c('font-mono')), 'text-code-default': f(400, 'text-sm', 1.5, c('font-mono')),
};

// WCAG 2 contrast from oklch, so the build fails before a token pair does.
function toRgb(value) {
  const m = /oklch\(([\d.]+) ([\d.]+) ([\d.]+)/.exec(value);
  if (!m) throw new Error('not an opaque oklch colour: ' + value);
  const [L, C, h] = [+m[1], +m[2], +m[3] * Math.PI / 180];
  const a = C * Math.cos(h), b = C * Math.sin(h);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3, mm = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3, s = (L - 0.0894841775 * a - 1.2914855480 * b) ** 3;
  return [4.0767416621 * l - 3.3077115913 * mm + 0.2309699292 * s, -1.2684380046 * l + 2.6097574011 * mm - 0.3413193965 * s, -0.0041960863 * l - 0.7034186147 * mm + 1.7076147010 * s].map(v => Math.min(1, Math.max(0, v)));
}
const luminance = rgb => 0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2]; // linear sRGB already
export const contrast = (x, y) => { const [p, q] = [luminance(toRgb(x)), luminance(toRgb(y))].sort((u, v) => v - u); return (p + 0.05) / (q + 0.05); };

const SURFACES = ['bg', 'surface', 'surface-2', 'surface-sunk'];
const PAIRS = [
  ...['text', 'text-muted', 'text-subtle', 'text-link', 'accent-text'].flatMap(t => SURFACES.map(s => [t, s, 4.5])),
  ...['ok', 'warn', 'danger', 'info', 'neutral'].flatMap(t => [`${t}-bg`, 'bg', 'surface', 'surface-2'].map(s => [t, s, 4.5])),
  ['text', 'accent-soft', 4.5], ['accent-text', 'accent-soft', 4.5],
  ['on-accent', 'accent', 4.5], ['on-accent', 'accent-hover', 4.5],
  ['on-danger', 'danger-solid', 4.5], ['on-danger', 'danger-solid-hover', 4.5],
  ...['bg', 'surface', 'surface-2'].map(s => ['focus', s, 3]),
  ...['bg', 'surface', 'surface-sunk'].map(s => ['border-strong', s, 3]),
  ...['bg', 'surface'].map(s => ['accent', s, 3]),
];
export function checkContrast(verbose) {
  const failures = [];
  for (const [name, p] of [['dark', dark], ['light', light]]) for (const [fg, bg, min] of PAIRS) {
    const ratio = contrast(p[fg], p[bg]);
    if (verbose) console.log(`${name.padEnd(5)} ${fg.padEnd(14)} on ${bg.padEnd(13)} ${ratio.toFixed(2).padStart(5)}  (min ${min})${ratio < min ? '  FAIL' : ''}`);
    if (ratio < min) failures.push(`${name}: --console-${fg} on --console-${bg} is ${ratio.toFixed(2)}:1, needs ${min}:1`);
  }
  return failures;
}

const kebab = key => key.replace(/([a-z])([A-Z])/g, '$1-$2').replace(/([A-Z])([A-Z])/g, '$1-$2').replace(/([a-z])(\d)/g, '$1-$2').toLowerCase();
const block = (prefix, entries, indent) => Object.entries(entries).map(([k, v]) => `${indent}--${prefix}-${k}: ${v};`).join('\n');
const openuiDefaults = theme => Object.fromEntries(Object.entries(theme).filter(([, v]) => typeof v === 'string').map(([k, v]) => [kebab(k), v]));
const themeBlock = (p, indent) => `${indent}color-scheme: ${p === dark ? 'dark' : 'light'};\n${block('console', p, indent)}\n${block('openui', openuiColours(), indent)}`;

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) {
  const failures = checkContrast(process.argv.includes('--check'));
  if (failures.length) { console.error('Theme contrast check failed:\n  ' + failures.join('\n  ')); process.exit(1); }
  // OpenUI's own light defaults first, so every variable it reads exists; ours override them.
  const base = {...openuiDefaults(defaultLightTheme), ...openuiShared};
  writeFileSync(new URL('../src/theme.css', import.meta.url), `/* Generated by scripts/theme-css.mjs. Do not edit; change the palette there. */
/* Dark is the default. <html data-theme="light"> or "system" (follows the OS) switches it. */
:root {
${block('openui', base, '  ')}
${block('console', shared, '  ')}
${themeBlock(dark, '  ')}
}
:root[data-theme="light"] {
${themeBlock(light, '  ')}
}
@media (prefers-color-scheme: light) {
  :root[data-theme="system"] {
${themeBlock(light, '    ')}
  }
}
/* Phones: body text 16px and inputs that do not zoom. */
@media (max-width: 720px) {
  :root { --console-text-sm: 14px; --console-text-base: 16px; --console-text-md: 17px; --console-text-lg: 19px; --console-text-xl: 22px; }
}
@media (prefers-reduced-motion: reduce) {
  :root { --console-dur-micro: 0ms; --console-dur-short: 0ms; }
}
`);
}
