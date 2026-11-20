// @vitest-environment jsdom
import {accessibleName, byRole, click, flush, press, render} from '../test/dom';
import {act} from 'react';
import {describe, expect, it, vi} from 'vitest';
import {sampleRun} from './fixtures';
import {RunReport} from './Reports';

const toast = vi.fn();
vi.mock('../data', () => ({useData: () => ({toast})}));
vi.mock('./api', async importOriginal => ({...await importOriginal<typeof import('./api')>(), getRun: async () => sampleRun}));

const dialog = () => byRole('dialog')[0];
const button = (name: string, within: ParentNode = document) => [...within.querySelectorAll('button')].find(b => b.textContent?.trim() === name);

async function openDecision() {
  render(<RunReport id="run_sample" />);
  await flush();
  const gate = button('Drop the newline')!;
  expect(gate).toBeTruthy();
  act(() => gate.focus());
  await click(gate);
  expect(dialog()).toBeTruthy();
  return gate;
}

describe('gate decision confirmation', () => {
  it('opens as a named, described modal dialog with focus inside', async () => {
    await openDecision();
    expect(accessibleName(dialog())).toBe('Confirm decision: Drop the newline');
    const described = document.getElementById(dialog().getAttribute('aria-describedby') ?? '');
    expect(described?.textContent).toBe('Should `textkit top` print a trailing newline after the last line?');
    expect(dialog().getAttribute('aria-modal')).toBe('true');
    expect(dialog().contains(document.activeElement)).toBe(true);
    expect(document.activeElement).toBe(button('Copy', dialog()));
    expect(dialog().textContent).toContain("gate-resolve --id 'gate_cli_output' --resolution 'Drop the newline'");
  });

  it('closes on Escape and returns focus to the gate button', async () => {
    const gate = await openDecision();
    await press('Escape');
    expect(dialog()).toBeUndefined();
    expect(document.activeElement).toBe(gate);
  });

  it('closes from its Close button and returns focus to the gate button', async () => {
    const gate = await openDecision();
    await click(button('Close', dialog())!);
    expect(dialog()).toBeUndefined();
    expect(document.activeElement).toBe(gate);
  });

  it('keeps Tab inside while the report behind it is inert', async () => {
    const gate = await openDecision();
    expect(gate.closest('[inert]')).toBeTruthy();
    const [first, last] = [button('Copy', dialog())!, button('Close', dialog())!];
    act(() => last.focus());
    await press('Tab');
    expect(document.activeElement).toBe(first);
    await press('Tab', {shiftKey: true});
    expect(document.activeElement).toBe(last);
  });
});
