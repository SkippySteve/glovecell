import {expect, test} from 'vitest';
import {projectState} from './state';
import type {Project} from './api';

test('gateway quarantine is an incident even when old phase says Ready', () => {
  const p = {metadata: {name: 'test', generation: 1}, spec: {desiredState: 'Running'}, status: {phase: 'Ready', conditions: [{type: 'Ready', status: 'False', reason: 'ProxyQuarantined', message: 'Gateway recovery required', observedGeneration: 1}]}} as Project;
  const state = projectState(p);
  expect(state.label).toBe('Gateway quarantined');
  expect(state.variant).toBe('danger');
  expect(state.busy).toBeUndefined();
  expect(state.attention).toBe('Gateway needs recovery');
});
