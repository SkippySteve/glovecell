// Unsaved edits to a project's own HTTPS rules, kept across polling and
// section switches until saved, discarded or the project changes state.
import type {RuleDraft} from '../../Rules';

export const ruleDrafts = new Map<string, {rules: RuleDraft[]; resourceVersion?: string}>();
