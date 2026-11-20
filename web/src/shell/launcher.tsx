// The command palette's always-loaded part: the Ctrl-K (⌘K) listener and openPalette.
// The palette itself, with UI SDK, is a separate chunk loaded on the first request
// and mounted already open; until then pages carry none of it. If that chunk is
// gone (the console was upgraded since the page loaded), the page reloads once.
import {Suspense, useEffect, useState} from 'react';
import {ChunkBoundary, lazyChunk} from '../chunk';
import type {Route} from '../router';
import {isPaletteShortcut, shortcutLabel} from './palette';

export const OPEN_EVENT = 'console-palette-open';
export const openPalette = () => window.dispatchEvent(new Event(OPEN_EVENT));
export const paletteShortcut = () => shortcutLabel(typeof navigator === 'undefined' ? '' : navigator.platform || navigator.userAgent);

const CommandPalette = lazyChunk(() => import('./Palette').then(m => ({default: m.CommandPalette})));

export function PaletteLauncher({route}: {route: Route}) {
  // The first request, and where focus was then; the palette takes over from there.
  const [first, setFirst] = useState<{from: HTMLElement | null} | null>(null);
  useEffect(() => {
    if (first) return;
    const request = () => setFirst({from: document.activeElement as HTMLElement | null});
    const onKey = (e: KeyboardEvent) => { if (isPaletteShortcut(e)) { e.preventDefault(); request(); } };
    window.addEventListener('keydown', onKey);
    window.addEventListener(OPEN_EVENT, request);
    return () => { window.removeEventListener('keydown', onKey); window.removeEventListener(OPEN_EVENT, request); };
  }, [first]);
  return first ? <ChunkBoundary what="command palette" floating><Suspense fallback={null}><CommandPalette route={route} openedFrom={first.from} /></Suspense></ChunkBoundary> : null;
}
