// Factory run reports: a list of collected runs and one projected report per run.
// The report is OpenUI Lang rendered with reportLibrary; its only action is a gate
// decision, which is checked against the run record and confirmed here, in console UI.
import {Renderer, type ActionEvent} from '@openuidev/react-lang';
import {useEffect, useMemo, useState} from 'react';
import {useData} from '../data';
import {getRun, gateCommand, listRuns, type RunSummary} from './api';
import {GATE_ACTION, reportLibrary} from './library';
import {projectReport, summarize} from './project';
import {RunTable} from './RunTable';
import {formatDuration, ms, recordFailures, recordSpan, runLabel, runView} from './runs';
import type {RunRecord} from './record';
import {Button, Callout, Card, CardHeader, Tag} from './ui';


export function RunList() {
  const {toast} = useData();
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  useEffect(() => { document.title = 'Runs · Sandboxes'; listRuns().then(setRuns).catch(error => { setRuns([]); toast((error as Error).message, true); }); }, [toast]);
  return (
    <div className="stack">
      <div className="page-head"><div><h1>Runs</h1><p className="muted">Orchestration runs collected from the factory, newest first. Each report is built from the run's recorded facts.</p></div></div>
      {runs === null ? <p className="empty">Loading runs…</p> : !runs.length ? (
        <Card variant="sunk" width="full" className="empty-card"><CardHeader title="No runs collected yet" subtitle="Runs appear here once the collector publishes a run record." /></Card>
      ) : <RunTable runs={runs} />}
    </div>
  );
}

// Above the report: the run's state, how long it ran, and every failure with the
// stage and error its last attempt recorded, so the reason is the first thing read.
function RunStatusPanel({record}: {record: RunRecord}) {
  const summary = summarize(record), span = recordSpan(record), failures = recordFailures(record);
  const now = Date.now();
  const status = summary.failed ? 'failed' : summary.pendingGates ? 'waiting' : span.running ? 'running' : summary.tasks && summary.succeeded === summary.tasks ? 'succeeded' : 'pending';
  const view = runView({id: record.run.id, objective: '', host: '', collectedAt: record.run.collectedAt, tasks: summary.tasks, status, startedAt: span.startedAt, endedAt: span.endedAt,
    lastActivity: [...record.tasks.flatMap(t => t.attempts.flatMap(a => [a.startedAt, a.endedAt]))].filter((v): v is string => Boolean(v)).sort().at(-1)}, now);
  const [label, variant] = runLabel(view);
  const duration = formatDuration(span.startedAt ? (ms(span.endedAt) || now) - ms(span.startedAt) : undefined);
  return (
    <section className={'run-status' + (failures.length ? ' has-failures' : '')} aria-label="Run status">
      <dl className="run-facts">
        <div><dt>Status</dt><dd><Tag size="sm" variant={variant} text={label} /></dd></div>
        <div><dt>Tasks</dt><dd className="num">{summary.succeeded} of {summary.tasks} succeeded{summary.failed ? `, ${summary.failed} failed` : ''}{summary.running ? `, ${summary.running} running` : ''}</dd></div>
        <div><dt>{span.running ? 'Running for' : 'Duration'}</dt><dd className="num">{duration || 'not recorded'}</dd></div>
        <div><dt>Started</dt><dd>{span.startedAt ? new Date(span.startedAt).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'}) : 'not recorded'}</dd></div>
      </dl>
      {failures.length ? (
        <div className="run-failures">
          <h2>Why it failed</h2>
          <ul>{failures.map(f => (
            <li key={f.taskId}>
              <strong>{f.title}</strong>
              {f.error || f.stage ? <code className="run-error">{`${f.stage ? f.stage + ': ' : ''}${f.error || 'no error recorded'}`}</code> : null}
              {f.checks.length ? <span className="subtle-text">{`Failed checks: ${f.checks.join(', ')}`}</span> : null}
            </li>
          ))}</ul>
        </div>
      ) : null}
    </section>
  );
}

interface Decision { gateId: string; question: string; option: string; command: string }

export function RunReport({id}: {id: string}) {
  const {toast} = useData();
  const [record, setRecord] = useState<RunRecord | null>(null);
  const [error, setError] = useState('');
  const [decision, setDecision] = useState<Decision | null>(null);
  const [copied, setCopied] = useState(false);
  useEffect(() => { document.title = id + ' · Runs'; setRecord(null); setError(''); getRun(id).then(setRecord).catch(e => setError((e as Error).message)); }, [id]);
  const report = useMemo(() => record ? projectReport(record) : null, [record]);

  // Report actions are requests, never authority: only a pending gate in this run's
  // record, with one of its own options, gets as far as a console confirmation.
  const onAction = (event: ActionEvent) => {
    if (!record || event.type !== GATE_ACTION) return;
    const {gateId, option} = event.params as {gateId?: unknown; option?: unknown};
    const gate = record.gates.find(g => g.id === gateId && g.status === 'pending');
    if (!gate || typeof option !== 'string' || !gate.options.includes(option)) { toast('Ignored a report action that does not match a pending gate in this run.', true); return; }
    setCopied(false);
    setDecision({gateId: gate.id, question: gate.question, option, command: gateCommand(record, gate.id, option)});
  };

  if (error) return <div className="stack"><Callout variant="danger" title="Run unavailable" description={error} /></div>;
  if (!record || report === null) return <div className="stack"><p className="empty">Loading run…</p></div>;
  const summary = summarize(record);
  return (
    <div className="stack">
      <div className="detail-head">
        <div>
          <div className="title-row"><h1>{record.run.id}</h1>{summary.failed ? <Tag size="sm" variant="danger" text={summary.failed + ' failed'} /> : null}{summary.pendingGates ? <Tag size="sm" variant="warning" text={summary.pendingGates + ' waiting on you'} /> : null}</div>
          <p className="muted">{record.run.objective}</p>
          <p className="subtle">From {record.run.host}, collected {new Date(record.run.collectedAt).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'})}</p>
        </div>
      </div>
      <RunStatusPanel record={record} />
      {decision ? (
        <Card variant="sunk" width="full" className="factory-confirm" role="dialog" aria-labelledby="factory-confirm-title">
          <h2 id="factory-confirm-title">Confirm decision: {decision.option}</h2>
          <p>{decision.question}</p>
          <p className="muted">The console cannot reach the Orca runtime yet. Run this command from a machine with cluster access to record the decision:</p>
          <div className="copy-field"><code>{decision.command}</code>
            <Button size="small" variant="secondary" onClick={async () => { try { await navigator.clipboard.writeText(decision.command); setCopied(true); } catch { toast('Copy failed; select the command instead.', true); } }}>{copied ? 'Copied' : 'Copy'}</Button>
          </div>
          <div className="actions"><Button size="small" variant="tertiary" onClick={() => setDecision(null)}>Close</Button></div>
        </Card>
      ) : null}
      <div className="factory-render"><Renderer response={report} library={reportLibrary} isStreaming={false} onAction={onAction} /></div>
    </div>
  );
}
