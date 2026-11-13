// The access level control: one row of named levels per sandbox, what the
// chosen level allows in plain words, the exact changes before anything is
// applied, and Lock now whenever something is open.
import {useEffect, useId, useState, type ReactNode} from 'react';
import {accessProfiles, api, sandboxAction, type AccessProfileView, type Observation, type Project, type WebRule} from '../api';
import {useData} from '../data';
import {pausedForEdits} from '../state';
import {Button, Tag, type TagVariant} from '../ui';
import {useConfirm} from '../workspace/Confirm';
import {loadRequests, removeRule, webLock, type Requests} from '../workspace/sources/internet';
import {Countdown, WindowBar} from '../workspace/bits';
import {clockAt} from '../workspace/time';
import type {SandboxState} from '../workspace/useSandbox';
import {LEVEL_NAMES, LEVELS, levelText, projectLevel, sandboxLevel, type AccessLevel, type BackendKind, type LevelState} from './levels';
import {MAX_REVOKES, OPEN_WINDOWS, projectPlan, rollbackTargets, runPlan, sandboxPlan, stepForm, type Change, type Plan} from './plan';
import './access.css';

const TONE: Record<AccessLevel, TagVariant> = {locked: 'success', ask: 'info', standard: 'info', open: 'danger', custom: 'neutral'};

// A compact tag naming the current level, for headers, lists and dashboards.
export function AccessLevelTag({state, now}: {state: LevelState; now: number}) {
  if (!state.supported) return <Tag size="sm" variant="neutral" text={state.label} />;
  if (!state.level) return <Tag size="sm" variant="neutral" text={state.label} />;
  const text = state.level === 'open' && state.expires
    ? <>{state.learning ? 'Open, learning' : 'Open'} <Countdown until={state.expires} now={now} /></>
    : LEVEL_NAMES[state.level];
  return <Tag size="sm" variant={TONE[state.level]} text={text} />;
}

function ChangeList({changes}: {changes: Change[]}) {
  const groups: [Change['kind'], string][] = [['remove', 'Stops'], ['add', 'Starts'], ['keep', 'Stays']];
  return <>{groups.map(([kind, title]) => {
    const items = changes.filter(c => c.kind === kind);
    if (!items.length) return null;
    const head = items.slice(0, 8), rest = items.slice(8);
    return (
      <div key={kind} className={'change-group is-' + kind}>
        <h3>{title} ({items.length})</h3>
        <ul>{head.map((c, i) => <li key={i}>{c.text}</li>)}</ul>
        {rest.length ? <details><summary>{rest.length} more</summary><ul>{rest.map((c, i) => <li key={i}>{c.text}</li>)}</ul></details> : null}
      </div>
    );
  })}</>;
}

interface ScaleProps {
  kind: BackendKind;
  state: LevelState;
  now: number;
  planFor: (level: AccessLevel) => Plan;
  // Loads what a plan needs before it can be shown (approved requests for Locked).
  prepare?: (level: AccessLevel) => void;
  apply: (plan: Plan) => Promise<void>;
  lockNow?: () => Promise<void>;
  openControls?: ReactNode;
  also?: ReactNode;
  links?: ReactNode;
  windowTotal?: number;
}

export function LevelScale({kind, state, now, planFor, prepare, apply, lockNow, openControls, also, links, windowTotal}: ScaleProps) {
  const ask = useConfirm();
  const id = useId();
  const [chosen, setPicked] = useState<AccessLevel | null>(null);
  // Until the operator picks one, the current level is selected.
  const picked: AccessLevel = chosen || state.level || 'locked';
  const [busy, setBusy] = useState(false);
  const current = state.level;
  useEffect(() => { prepare?.(picked); }, [picked]); // prepare is recreated each render
  const plan = planFor(picked);
  const text = levelText(picked, kind);
  const open = current === 'open';
  const review = async () => {
    const verb = picked === 'open' ? 'Open' : 'Change to ' + LEVEL_NAMES[picked];
    const ok = await ask(`${verb}?`, <div className="plan-body">
      <p>{text.summary} {plan.widens ? 'This widens what the sandbox can reach.' : 'This only narrows what the sandbox can reach.'}</p>
      <ChangeList changes={plan.changes} />
      {kind === 'project' && plan.after ? <p className="muted">Applied in one update and recorded in History. {picked === 'custom' ? '' : 'Profile rules apply while the sandbox runs; the proxy restarts to end connections opened under the old level.'}</p> : null}
      {kind === 'sandbox' && plan.steps.length > 1 ? <p className="muted">Applied as {plan.steps.length} changes, narrowing first. If one fails, the rest stop and rules added by this change are removed again.</p> : null}
    </div>, picked === 'open' ? 'Open' : 'Apply ' + LEVEL_NAMES[picked]);
    if (!ok) return;
    setBusy(true);
    try { await apply(plan); } finally { setBusy(false); }
  };
  return (
    <section className={'card level-card' + (open ? ' is-open' : '')} aria-labelledby={id + 'title'} data-testid="access-level">
      <div className="card-hd">
        <h2 id={id + 'title'}>Access level</h2>
        <AccessLevelTag state={state} now={now} />
        {open && state.expires ? <span className="end level-expiry">
          <span className="big-countdown" aria-label="Time left"><Countdown until={state.expires} now={now} /></span>
          <span className="subtle">until {clockAt(state.expires)}</span>
        </span> : null}
        {lockNow && (open || kind === 'project') ? <span className={open && state.expires ? '' : 'end'}>
          <Button variant={open ? 'primary' : 'secondary'} buttonType="destructive" size="small" disabled={busy} onClick={async () => { setBusy(true); try { await lockNow(); } finally { setBusy(false); } }}>Lock now</Button>
        </span> : null}
      </div>
      <div className="card-bd">
        {open && state.expires && windowTotal ? <WindowBar until={state.expires} total={windowTotal} now={now} tone={state.learning ? 'warn' : 'bad'} label="Share of the open window left" /> : null}
        {open ? <p><strong>{state.learning ? 'Every request is allowed and recorded as an approval' : 'Every public website is reachable'} until {clockAt(state.expires)}.</strong> <span className="muted">Then it returns to {state.standing ? LEVEL_NAMES[state.standing] : 'its rules'}.</span></p> : null}
        <fieldset className="level-scale">
          <legend className="visually-hidden">Choose an access level</legend>
          {LEVELS.map(level => {
            const p = level === picked ? plan : planFor(level);
            const isCurrent = current === level;
            return (
              <label key={level} className={'level-tile is-' + level + (isCurrent ? ' is-current' : '') + (picked === level ? ' is-picked' : '') + (!p.available && !isCurrent && level !== 'custom' ? ' is-unavailable' : '')}>
                <input type="radio" name={id + 'level'} value={level} checked={picked === level} onChange={() => setPicked(level)} />
                <span className="level-name">{LEVEL_NAMES[level]}{isCurrent ? <span className="level-now">current</span> : null}</span>
                <span className="level-sum">{levelText(level, kind).summary}</span>
              </label>
            );
          })}
        </fieldset>
        <div className="level-detail" aria-live="polite">
          <div className="level-lists">
            <div><h3>Allows</h3><ul>{text.allows.map(a => <li key={a}>{a}</li>)}</ul></div>
            <div><h3>Stays blocked</h3><ul>{text.blocks.map(b => <li key={b}>{b}</li>)}</ul></div>
          </div>
          {picked === 'open' && plan.available ? openControls : null}
          {plan.available
            ? <div className="actions"><Button variant="primary" disabled={busy} onClick={review}>{picked === 'open' ? 'Review and open…' : `Review changes for ${LEVEL_NAMES[picked]}…`}</Button></div>
            : <p className={'level-reason' + (current === picked ? ' is-current' : '')}>{current === picked ? (picked === 'custom' ? 'This is the current level. Edit the rules below; the level follows them.' : `This is the current level. ${plan.reason?.startsWith('Already') ? '' : plan.reason || ''}`) : plan.reason}</p>}
        </div>
        {also ? <div className="level-also">{also}</div> : null}
      </div>
      {links ? <div className="card-ft level-links">{links}</div> : null}
    </section>
  );
}

// --- sandbox-control sandboxes -------------------------------------------------

// Every approved request, page by page, for Locked; null when there are more than MAX_REVOKES.
async function loadAllApproved(id: string, total: number): Promise<Observation[] | null> {
  if (total > MAX_REVOKES) return null;
  const out: Observation[] = [];
  for (let page = 1; out.length < total && page <= Math.ceil(MAX_REVOKES / 100) + 1; page++) {
    const r = await loadRequests(id, '', page, 100);
    out.push(...r.approved.items.filter(o => !out.some(x => x.observation === o.observation)));
    if (!r.approved.paging || r.approved.paging.page >= r.approved.paging.pages) break;
  }
  return out;
}

export function SandboxAccessLevels({ws, now, data, onChanged, fineGrained}: {ws: SandboxState; now: number; data: Requests | null; onChanged: () => void; fineGrained: ReactNode}) {
  const {toast, reload: reloadList} = useData();
  const d = ws.detail!;
  const [minutes, setMinutes] = useState(60);
  const [learn, setLearn] = useState(false);
  const [approved, setApproved] = useState<Observation[] | undefined>(undefined);
  const rules: WebRule[] | null | undefined = data ? (data.rules ?? null) : undefined;
  const approvedTotal = d.egressCounts?.approved ?? 0;
  const state = sandboxLevel({learningEgress: d.learningEgress, phase: d.learning?.phase, expires: d.learning && d.learning.phase !== 'Locked' ? d.learning.expires * 1000 : 0}, {rules, approved: d.egressCounts?.approved});
  const planFor = (level: AccessLevel) => sandboxPlan(level, {state, rules: rules ?? null, approvedTotal, approved, open: {minutes, learn}});
  const prepare = (level: AccessLevel) => {
    if (level === 'locked' && approvedTotal > 0 && approvedTotal <= MAX_REVOKES && !approved) loadAllApproved(d.id, approvedTotal).then(list => setApproved(list || []), e => toast((e as Error).message, true));
  };
  const finish = async (message: string, error = false) => {
    toast(d.id + ': ' + message, error);
    setApproved(undefined);
    await Promise.all([ws.reload(), reloadList()]);
    onChanged();
  };
  const apply = async (plan: Plan) => {
    const result = await runPlan(plan.steps, async s => { const f = stepForm(s); await sandboxAction(f.path, d.id, f.fields); }, async added => {
      const fresh = await loadRequests(d.id, '', 1, 10);
      const targets = rollbackTargets(fresh.rules || [], added);
      for (const r of targets) { const f = removeRule(r); await sandboxAction(f.path, d.id, f.fields); }
      return targets.length;
    });
    if (!result.error) return finish(plan.target === 'open' ? `open until ${clockAt(Date.now() + minutes * 60000)}` : `now ${LEVEL_NAMES[plan.target]}`);
    const rolled = result.rolledBack ? ` The ${result.rolledBack} rules it had added were removed again.` : '';
    const failedRollback = result.rollbackError ? ` Removing the rules it added also failed (${result.rollbackError}); check Web rules.` : '';
    return finish(`stopped after ${result.done} of ${result.total} changes: ${result.error}.${rolled}${failedRollback}`, true);
  };
  const lockNow = async () => {
    const f = webLock(60);
    if (await ws.act(f.path, f.fields, 'web access locked')) onChanged();
  };
  const openControls = (
    <div className="open-controls">
      <div className="field"><span className="field-label" id="open-window">Window</span>
        <div className="seg" role="radiogroup" aria-labelledby="open-window">
          {OPEN_WINDOWS.map(([m, l]) => <button key={m} type="button" role="radio" aria-checked={minutes === m} className="seg-item" onClick={() => setMinutes(m)}>{l}</button>)}
        </div></div>
      <label className="check"><input type="checkbox" checked={learn} onChange={e => setLearn(e.target.checked)} /> Learn: record each request as a permanent approval, to keep after the window ends</label>
      <p className="muted">Ends at {clockAt(Date.now() + minutes * 60000)} on its own. Lock now ends it earlier.</p>
    </div>
  );
  const also = <>
    <h3>At every level</h3>
    <ul>
      {d.providerHosts.length ? <li>Model providers: {d.providerHosts.join(', ')}</li> : null}
      {approvedTotal && state.level !== 'locked' ? <li>{approvedTotal.toLocaleString()} approved requests</li> : null}
      {ws.lan?.open ? <li className="warn">{ws.lan.open === 1 ? '1 LAN grant' : ws.lan.open + ' LAN grants'} open (LAN devices)</li> : null}
      <li>Files you download or upload from the Files page</li>
    </ul>
  </>;
  return <LevelScale kind="sandbox" state={state} now={now} planFor={planFor} prepare={prepare} apply={apply} lockNow={state.level === 'open' ? lockNow : undefined}
    openControls={openControls} also={also} links={fineGrained} windowTotal={d.learning ? d.learning.minutes * 60000 : 0} />;
}

// --- project sandboxes -----------------------------------------------------------

export function useProfiles() {
  const [profiles, setProfiles] = useState<AccessProfileView[] | null | undefined>(undefined);
  useEffect(() => {
    let live = true;
    accessProfiles().then(p => { if (live) setProfiles(p); }, () => { if (live) setProfiles(null); });
    return () => { live = false; };
  }, []);
  return profiles;
}

export function ProjectAccessLevels({p, now, profiles, fineGrained, also}: {p: Project; now: number; profiles: AccessProfileView[] | null | undefined; fineGrained?: ReactNode; also?: ReactNode}) {
  const {reload, toast} = useData();
  const ask = useConfirm();
  const state = projectLevel(p, profiles);
  const paused = pausedForEdits(p);
  const name = p.metadata.name;
  const planFor = (level: AccessLevel) => profiles === undefined ? {target: level, available: false, reason: 'Loading levels…', steps: [], changes: [], widens: false} : projectPlan(level, p, profiles, paused);
  const apply = async (plan: Plan) => {
    try {
      await api('workspaces/' + encodeURIComponent(name) + '/access-level', 'PATCH', {resourceVersion: p.metadata.resourceVersion, profile: plan.profile});
      toast(`${name}: ${LEVEL_NAMES[plan.target]} requested. The controller applies it${plan.target === 'custom' ? '' : ' while the sandbox runs'}.`);
      await reload();
    } catch (e) { toast((e as Error).message, true); }
  };
  const lockNow = async () => {
    if (!await ask(`Lock ${name} now?`, <div className="plan-body"><p>Removes every LAN and other grant of this sandbox and sets the Locked level. Its own HTTPS rules and LAN hosts stay until you remove them; HTTPS rules need the sandbox paused.</p></div>, 'Lock now')) return;
    try {
      const out = await api<{grantsRemoved: number; ownRules: number; lanHosts: number}>('workspaces/' + encodeURIComponent(name) + '/lock', 'POST', {resourceVersion: p.metadata.resourceVersion});
      const left = [out.ownRules ? `${out.ownRules} own HTTPS rules` : '', out.lanHosts ? `${out.lanHosts} LAN hosts` : ''].filter(Boolean).join(' and ');
      toast(`${name}: locked, ${out.grantsRemoved} grants removed.${left ? ' Still allowed: ' + left + '.' : ''}`, Boolean(left));
      await reload();
    } catch (e) { toast((e as Error).message, true); }
  };
  return <LevelScale kind="project" state={state} now={now} planFor={planFor} apply={apply} lockNow={profiles ? lockNow : undefined} also={also} links={fineGrained} />;
}


