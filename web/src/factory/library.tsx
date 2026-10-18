// Two OpenUI component libraries for factory reports.
//
// agentLibrary is the only vocabulary an agent's own report may use: prose, lists,
// tables and code. It has no links, actions, checks or gates, so an agent cannot
// draw a passing check, a decision or a control that looks like the console's.
//
// reportLibrary renders reports projected from a run record. Its facts come from
// the collector, and agent reports appear only inside a labelled AgentSection,
// rendered with agentLibrary. Gate buttons raise a `factory.gate` action; the
// console checks it against the run record and confirms before anything happens.
import {createLibrary, defineComponent, Renderer, useTriggerAction} from '@openuidev/react-lang';
import {z} from 'zod/v4';
import {Button, Callout, Card, CardHeader, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Tag, type TagVariant} from './ui';

export const GATE_ACTION = 'factory.gate';

const short = z.string().max(200);
const long = z.string().max(4000);

// Agent vocabulary.

const Heading = defineComponent({
  name: 'Heading',
  description: 'A section heading inside the report.',
  props: z.object({text: short}),
  component: ({props}) => <h4 className="factory-heading">{props.text}</h4>,
});
const Paragraph = defineComponent({
  name: 'Paragraph',
  description: 'A paragraph of plain text. No Markdown or HTML.',
  props: z.object({text: long}),
  component: ({props}) => <p className="factory-paragraph">{props.text}</p>,
});
const Bullets = defineComponent({
  name: 'Bullets',
  description: 'A bulleted list of short plain-text items.',
  props: z.object({items: z.array(short).max(50)}),
  component: ({props}) => <ul className="factory-bullets">{props.items.map((item, i) => <li key={i}>{item}</li>)}</ul>,
});
const KeyValues = defineComponent({
  name: 'KeyValues',
  description: 'Label and value pairs, such as measurements or settings.',
  props: z.object({items: z.array(z.object({label: short, value: short})).max(50)}),
  component: ({props}) => <dl className="factory-facts">{props.items.map((item, i) => <div key={i}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}</dl>,
});
const DataTable = defineComponent({
  name: 'DataTable',
  description: 'A table with column headings and rows of plain-text cells.',
  props: z.object({columns: z.array(short).min(1).max(12), rows: z.array(z.array(short).max(12)).max(200)}),
  component: ({props}) => (
    <Table>
      <TableHeader><TableRow>{props.columns.map((column, i) => <TableHead key={i}>{column}</TableHead>)}</TableRow></TableHeader>
      <TableBody>{props.rows.map((row, i) => <TableRow key={i}>{row.map((cell, j) => <TableCell key={j}>{cell}</TableCell>)}</TableRow>)}</TableBody>
    </Table>
  ),
});
const Code = defineComponent({
  name: 'Code',
  description: 'A block of code or command output, shown verbatim.',
  props: z.object({text: z.string().max(8000)}),
  component: ({props}) => <pre className="factory-code"><code>{props.text}</code></pre>,
});
const Note = defineComponent({
  name: 'Note',
  description: 'A highlighted note: info for context, warning for a risk or open question.',
  props: z.object({tone: z.enum(['info', 'warning']), text: long}),
  component: ({props}) => <Callout variant={props.tone} description={props.text} />,
});
const AgentContent = z.union([Heading.ref, Paragraph.ref, Bullets.ref, KeyValues.ref, DataTable.ref, Code.ref, Note.ref]);
const AgentReport = defineComponent({
  name: 'AgentReport',
  description: 'The root of an agent report. Put every other component inside it, in reading order.',
  props: z.object({children: z.array(AgentContent).max(100)}),
  component: ({props, renderNode}) => <div className="factory-agent-report">{props.children.map((child, i) => <div key={i}>{renderNode(child)}</div>)}</div>,
});

export const agentLibrary = createLibrary({components: [AgentReport, Heading, Paragraph, Bullets, KeyValues, DataTable, Code, Note], root: 'AgentReport'});

// Trusted report vocabulary.

const tone = z.enum(['success', 'warning', 'danger', 'info', 'neutral']);

const Facts = defineComponent({
  name: 'Facts',
  description: 'Observed facts about a task.',
  props: z.object({items: z.array(z.object({label: short, value: z.string().max(1000)})).max(50)}),
  component: ({props}) => <dl className="factory-facts">{props.items.map((item, i) => <div key={i}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}</dl>,
});
const checkVariant: Record<string, TagVariant> = {passed: 'success', failed: 'danger', not_run: 'neutral'};
const Checks = defineComponent({
  name: 'Checks',
  description: 'Check results reported by CI or the collector.',
  props: z.object({items: z.array(z.object({name: short, status: z.enum(['passed', 'failed', 'not_run']), source: z.enum(['ci', 'collector']), detail: long.optional()})).max(64)}),
  component: ({props}) => (
    <ul className="factory-checks">
      {props.items.map((check, i) => (
        <li key={i}>
          <Tag size="sm" variant={checkVariant[check.status]} text={check.status === 'not_run' ? 'Not run' : check.status === 'passed' ? 'Passed' : 'Failed'} />
          <span className="factory-check-name">{check.name}</span>
          <span className="subtle">{check.source === 'ci' ? 'CI' : 'Collector'}</span>
          {check.detail ? <pre className="factory-code"><code>{check.detail}</code></pre> : null}
        </li>
      ))}
    </ul>
  ),
});
const Changes = defineComponent({
  name: 'Changes',
  description: 'The Git branch, its commits and changed files.',
  props: z.object({
    branch: short, base: short,
    commits: z.array(z.object({sha: z.string().max(40), subject: z.string().max(300)})).max(200),
    files: z.array(z.object({path: z.string().max(1024), added: z.number(), removed: z.number()})).max(500),
  }),
  component: ({props}) => (
    <div className="factory-changes">
      <p><code>{props.branch}</code> <span className="subtle">from {props.base}</span></p>
      {props.commits.length ? <ul className="factory-commits">{props.commits.map(c => <li key={c.sha}><code>{c.sha.slice(0, 8)}</code> {c.subject}</li>)}</ul> : <p className="subtle">No commits yet.</p>}
      {props.files.length ? (
        <Table>
          <TableHeader><TableRow><TableHead>File</TableHead><TableHead>Added</TableHead><TableHead>Removed</TableHead></TableRow></TableHeader>
          <TableBody>{props.files.map(f => <TableRow key={f.path}><TableCell><code>{f.path}</code></TableCell><TableCell>+{f.added}</TableCell><TableCell>−{f.removed}</TableCell></TableRow>)}</TableBody>
        </Table>
      ) : null}
    </div>
  ),
});
const Quote = defineComponent({
  name: 'Quote',
  description: 'Text an agent wrote, shown as a quotation and never as UI.',
  props: z.object({author: short, text: long}),
  component: ({props}) => <figure className="factory-quote"><blockquote>{props.text}</blockquote><figcaption>{props.author}, unverified</figcaption></figure>,
});
const Notice = defineComponent({
  name: 'Notice',
  description: 'A collector-generated notice about a task or run.',
  props: z.object({tone: z.enum(['info', 'warning', 'danger', 'success']), title: short, text: long}),
  component: ({props}) => <Callout variant={props.tone} title={props.title} description={props.text} />,
});
const AgentSection = defineComponent({
  name: 'AgentSection',
  description: "An agent's own report, rendered with the restricted agent library.",
  props: z.object({agent: short, source: z.string().max(64 * 1024)}),
  // No onAction and no toolProvider: nothing in an agent report can act or query.
  component: ({props}) => (
    <section className="factory-agent-section" aria-label={'Report written by ' + props.agent}>
      <p className="factory-agent-label">Written by {props.agent} · not verified by the collector</p>
      <Renderer response={props.source} library={agentLibrary} isStreaming={false} />
    </section>
  ),
});
const TaskContent = z.union([Facts.ref, Checks.ref, Changes.ref, Quote.ref, Notice.ref, AgentSection.ref]);
const TaskSection = defineComponent({
  name: 'TaskSection',
  description: 'One orchestration task: its status and evidence.',
  props: z.object({title: short, taskId: short, status: short, tone, children: z.array(TaskContent).max(20)}),
  component: ({props, renderNode}) => (
    <Card variant="sunk" width="full" className="factory-task">
      <div className="factory-task-head">
        <CardHeader title={props.title} subtitle={props.taskId} />
        <Tag size="sm" variant={props.tone} text={props.status} />
      </div>
      {props.children.map((child, i) => <div key={i}>{renderNode(child)}</div>)}
    </Card>
  ),
});

function GateButtons({gateId, options}: {gateId: string; options: string[]}) {
  const trigger = useTriggerAction();
  return <div className="factory-gate-actions">{options.map(option => <Button key={option} variant="secondary" size="small" onClick={() => trigger('Resolve ' + gateId + ': ' + option, undefined, {type: GATE_ACTION, params: {gateId, option}})}>{option}</Button>)}</div>;
}
const Gate = defineComponent({
  name: 'Gate',
  description: 'A decision gate that blocks a task until someone resolves it.',
  props: z.object({gateId: short, taskId: short, question: z.string().max(1000), options: z.array(short).min(1).max(8), status: z.enum(['pending', 'resolved']), resolution: short.optional()}),
  component: ({props}) => (
    <Card variant="sunk" width="full" className="factory-gate">
      <div className="factory-task-head">
        <CardHeader title="Decision needed" subtitle={props.taskId} />
        <Tag size="sm" variant={props.status === 'pending' ? 'warning' : 'success'} text={props.status === 'pending' ? 'Waiting' : 'Resolved: ' + (props.resolution || '')} />
      </div>
      <p>{props.question}</p>
      {props.status === 'pending' ? <GateButtons gateId={props.gateId} options={props.options} /> : null}
    </Card>
  ),
});
const Report = defineComponent({
  name: 'Report',
  description: 'The root of a run report.',
  props: z.object({title: short, subtitle: z.string().max(1000), children: z.array(z.union([Notice.ref, Gate.ref, TaskSection.ref])).max(200)}),
  component: ({props, renderNode}) => (
    <article className="factory-report">
      <header><h2>{props.title}</h2><p className="muted">{props.subtitle}</p></header>
      {props.children.map((child, i) => <div key={i}>{renderNode(child)}</div>)}
    </article>
  ),
});

export const reportLibrary = createLibrary({components: [Report, Notice, Gate, TaskSection, Facts, Checks, Changes, Quote, AgentSection], root: 'Report'});
