import {useId} from 'react';
import type {HTTPSRule, Method} from './api';
import {Button, CheckBoxItem, Input, TextArea} from './ui';

export const METHODS: Method[] = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'];
// Paths are edited as text so a trailing blank line does not vanish while typing.
export interface RuleDraft { key: number; host: string; methods: Method[]; paths: string }
let nextKey = 0;
export const newRule = (): RuleDraft => ({key: nextKey++, host: '', methods: ['GET', 'HEAD'], paths: '/'});
export const toDraft = (rule: HTTPSRule): RuleDraft => ({key: nextKey++, host: rule.host, methods: rule.methods, paths: rule.paths.join('\n')});
export const toRule = (draft: RuleDraft): HTTPSRule => ({host: draft.host.trim().toLowerCase(), methods: draft.methods, paths: draft.paths.split('\n').map(s => s.trim()).filter(Boolean)});
export function rulesError(drafts: RuleDraft[]) {
  const missing = drafts.find(rule => !rule.methods.length);
  return missing ? (missing.host || 'Each destination') + ': choose at least one method.' : '';
}

function RuleEditor({rule, disabled, onChange, onRemove}: {rule: RuleDraft; disabled: boolean; onChange: (rule: RuleDraft) => void; onRemove: () => void}) {
  const id = useId();
  return (
    <div className="rule">
      <div className="field">
        <label className="field-label" htmlFor={id + 'host'}>Hostname</label>
        <Input id={id + 'host'} value={rule.host} required disabled={disabled} placeholder="api.example.com" autoCapitalize="off" spellCheck={false} onChange={event => onChange({...rule, host: event.target.value})} />
      </div>
      <div className="field">
        <label className="field-label" htmlFor={id + 'paths'}>Path prefixes, one per line</label>
        <TextArea id={id + 'paths'} rows={1} value={rule.paths} required disabled={disabled} onChange={event => onChange({...rule, paths: event.target.value})} />
      </div>
      {disabled ? <span /> : <Button type="button" variant="tertiary" size="small" className="remove" aria-label={'Remove destination ' + rule.host} onClick={onRemove}>Remove</Button>}
      <div className="methods field" role="group" aria-label="Methods">
        <span className="field-label">Methods</span>
        <div className="chips">
          {METHODS.map(method => <CheckBoxItem key={method} label={method} disabled={disabled} checked={rule.methods.includes(method)}
            onChange={checked => onChange({...rule, methods: checked ? [...rule.methods, method] : rule.methods.filter(m => m !== method)})} />)}
        </div>
      </div>
    </div>
  );
}

export function RuleList({rules, disabled = false, onChange, empty}: {rules: RuleDraft[]; disabled?: boolean; onChange: (rules: RuleDraft[]) => void; empty: string}) {
  return (
    <div className="rules">
      {rules.map((rule, i) => <RuleEditor key={rule.key} rule={rule} disabled={disabled}
        onChange={next => onChange(rules.map((r, j) => j === i ? next : r))}
        onRemove={() => onChange(rules.filter((_, j) => j !== i))} />)}
      {rules.length ? null : <p className="subtle">{empty}</p>}
    </div>
  );
}
