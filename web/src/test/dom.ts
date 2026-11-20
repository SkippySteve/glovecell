// Browser-like tests for the console's interactive widgets: jsdom plus the stubs
// UI SDK's testing guide lists (jsdom lacks popovers, animations and layout).
// Import this first in a test file that starts with `// @vitest-environment jsdom`.
import {settle} from '@ui-sdk/core/testing';
import {act, type ReactElement} from 'react';
import {createRoot, type Root} from 'react-dom/client';
import {afterEach} from 'vitest';

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
  var __UI_SDK_STRICT__: boolean | undefined;
}
globalThis.IS_REACT_ACT_ENVIRONMENT = true;
// Development warnings from UI SDK (an unnamed control, a bad nesting) fail the test.
globalThis.__UI_SDK_STRICT__ = true;

// Node 25+ has its own localStorage global, which hides jsdom's and needs a file.
const jsdomWindow = (globalThis as {jsdom?: {window: Window}}).jsdom?.window;
if (jsdomWindow) Object.defineProperty(globalThis, 'localStorage', {value: jsdomWindow.localStorage, configurable: true});

HTMLElement.prototype.showPopover = function () {};
HTMLElement.prototype.hidePopover = function () {};
Element.prototype.getAnimations = () => [];
Element.prototype.scrollIntoView = function () {};
Element.prototype.checkVisibility = () => true;
// jsdom has no top layer: an open popover would stay display: none and refuse focus.
document.head.append(Object.assign(document.createElement('style'), {textContent: '[popover] { display: block !important }'}));
Element.prototype.getClientRects = () => [] as unknown as DOMRectList;
document.elementFromPoint = () => null;
document.elementsFromPoint = () => [];
// jsdom has no inert property: UI SDK sets el.inert on the page behind a modal
// dialog. Reflect it to the attribute so a test can see the page is inert.
if (!('inert' in HTMLElement.prototype)) {
  Object.defineProperty(HTMLElement.prototype, 'inert', {configurable: true,
    get(this: HTMLElement) { return this.hasAttribute('inert'); },
    set(this: HTMLElement, on: boolean) { this.toggleAttribute('inert', Boolean(on)); }});
}
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };

let mounted: {root: Root; host: HTMLElement}[] = [];
afterEach(() => {
  for (const {root, host} of mounted) { act(() => root.unmount()); host.remove(); }
  mounted = [];
  document.body.innerHTML = '';
});

export function render(ui: ReactElement): HTMLElement {
  const host = document.body.appendChild(document.createElement('div'));
  const root = createRoot(host);
  act(() => root.render(ui));
  mounted.push({root, host});
  return host;
}

// Let UI SDK finish focus moves and other work it schedules after an event.
export const flush = () => act(() => settle());

export async function press(key: string, init: KeyboardEventInit = {}, target: Element | null = document.activeElement) {
  if (!target) throw new Error('nothing has focus to press ' + key + ' on');
  act(() => { target.dispatchEvent(new KeyboardEvent('keydown', {key, bubbles: true, cancelable: true, composed: true, ...init})); });
  act(() => { target.dispatchEvent(new KeyboardEvent('keyup', {key, bubbles: true, cancelable: true, composed: true, ...init})); });
  await flush();
}

export async function click(target: Element, pointerType: 'mouse' | 'touch' = 'mouse') {
  act(() => {
    target.dispatchEvent(new PointerEvent('pointerdown', {bubbles: true, cancelable: true, button: 0, pointerType}));
    target.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, button: 0}));
    target.dispatchEvent(new PointerEvent('pointerup', {bubbles: true, cancelable: true, button: 0, pointerType}));
    target.dispatchEvent(new MouseEvent('mouseup', {bubbles: true, cancelable: true, button: 0}));
    target.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true, button: 0}));
  });
  await flush();
}

// Types into a text field the way React sees a keystroke: the native value setter, then input.
export async function type(input: HTMLInputElement, text: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  act(() => { set.call(input, text); input.dispatchEvent(new InputEvent('input', {bubbles: true, data: text, inputType: 'insertText'})); });
  await flush();
}

// The accessible name from aria-labelledby or aria-label, as a screen reader reads it.
export function accessibleName(el: Element): string {
  const by = el.getAttribute('aria-labelledby');
  if (by) return by.split(/\s+/).map(id => document.getElementById(id)?.textContent?.trim() ?? '').join(' ').trim();
  return el.getAttribute('aria-label')?.trim() ?? '';
}

export const byRole = (role: string, within: ParentNode = document) => [...within.querySelectorAll(`[role="${role}"]`)] as HTMLElement[];
