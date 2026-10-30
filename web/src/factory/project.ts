// Deterministic projection of a run record into an OpenUI Lang report.
// The same record always produces the same report, and OpenUI's own serializer
// does the quoting, so text from a record can never become components.
import {jsonToOpenUI, type ElementNode} from '@openuidev/lang-core';
import {reportLibrary} from './library';
import type {Gate, RunRecord, Task} from './record';
import {taskState} from './runs';

export {taskState};

const node = (typeName: string, props: Record<string, unknown>): ElementNode => ({type: 'element', typeName, props, partial: false});
const clip = (value: string, max: number) => value.length > max ? value.slice(0, max - 1) + '…' : value;

function taskSection(task: Task, record: RunRecord): ElementNode {
  const last = task.attempts.at(-1);
  const facts: [string, string | undefined][] = [
    ['Agent', last?.agent],
    ['Attempts', task.attempts.length ? String(task.attempts.length) : undefined],
    ['Dispatch', last?.dispatchId],
    ['Depends on', task.deps.length ? task.deps.join(', ') : undefined],
    ['Worktree', task.git?.worktree],
    ['Failed at', last?.failedStage],
    ['Error', last?.lastError],
  ];
  const children: ElementNode[] = [node('Facts', {items: facts.filter((f): f is [string, string] => Boolean(f[1])).map(([label, value]) => ({label, value: clip(value, 1000)}))})];
  if (task.checks.length) children.push(node('Checks', {items: task.checks.map(c => ({name: clip(c.name, 200), status: c.status, source: c.source, ...(c.detail ? {detail: clip(c.detail, 4000)} : {})}))}));
  else children.push(node('Notice', {tone: 'info', title: 'No checks', text: 'No CI or collector check has run for this task, so nothing about its result is verified.'}));
  if (task.git) children.push(node('Changes', {branch: clip(task.git.branch, 200), base: clip(task.git.base, 200), commits: task.git.commits, files: task.git.files}));
  if (last?.summary) children.push(node('Quote', {author: clip(last.agent, 200), text: clip(last.summary, 4000)}));
  for (const report of record.agentReports.filter(r => r.taskId === task.id)) children.push(node('AgentSection', {agent: clip(report.agent, 200), source: report.lang}));
  const state = taskState(task);
  return node('TaskSection', {title: clip(task.title, 200), taskId: task.id, status: state.label, tone: state.tone, children});
}

const gateNode = (gate: Gate) => node('Gate', {gateId: gate.id, taskId: gate.taskId, question: gate.question, options: gate.options, status: gate.status, ...(gate.resolution ? {resolution: gate.resolution} : {})});

export function summarize(record: RunRecord) {
  const states = record.tasks.map(taskState);
  return {
    tasks: record.tasks.length,
    succeeded: states.filter(s => s.tone === 'success').length,
    failed: states.filter(s => s.tone === 'danger').length,
    running: states.filter(s => s.label === 'Running').length,
    pendingGates: record.gates.filter(g => g.status === 'pending').length,
  };
}

export function projectReport(record: RunRecord): string {
  const summary = summarize(record);
  const notices: ElementNode[] = [];
  if (summary.failed) notices.push(node('Notice', {tone: 'danger', title: `${summary.failed} of ${summary.tasks} tasks failed`, text: 'Open each failed task for its error and the stage where it stopped.'}));
  else if (summary.tasks && summary.succeeded === summary.tasks) notices.push(node('Notice', {tone: 'success', title: 'Every task succeeded', text: 'Agents reported success; check results below show what was verified.'}));
  if (summary.pendingGates) notices.push(node('Notice', {tone: 'warning', title: `${summary.pendingGates} decision${summary.pendingGates === 1 ? '' : 's'} waiting`, text: 'Blocked tasks continue after a decision is recorded.'}));
  const unmatched = record.agentReports.filter(r => !record.tasks.some(t => t.id === r.taskId)).length;
  if (unmatched) notices.push(node('Notice', {tone: 'warning', title: 'Unmatched agent reports', text: `${unmatched} agent report${unmatched === 1 ? ' does' : 's do'} not belong to a task in this run and ${unmatched === 1 ? 'was' : 'were'} left out.`}));
  const gates = [...record.gates].sort((a, b) => (a.status === 'pending' ? 0 : 1) - (b.status === 'pending' ? 0 : 1)).map(gateNode);
  const root = node('Report', {
    title: 'Run ' + record.run.id,
    subtitle: clip(`${record.run.objective} · ${record.run.host} · collected ${record.run.collectedAt}`, 1000),
    children: [...notices, ...gates, ...record.tasks.map(t => taskSection(t, record))],
  });
  return jsonToOpenUI(root, reportLibrary);
}
