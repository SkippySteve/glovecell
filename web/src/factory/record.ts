// A run record is the factory's source of truth for one Orca orchestration Run.
// The collector fills it from Orca and Git state; reports are projected from it.
// Fields an agent wrote itself (summaries, reports) are kept apart from observed facts.
import {z} from 'zod';

const text = (max: number) => z.string().max(max);
const id = z.string().min(1).max(128);

export const CheckSchema = z.object({
  name: text(120),
  command: text(500).optional(),
  status: z.enum(['passed', 'failed', 'not_run']),
  // Where the result came from: a CI system or the collector itself. Agents never report checks.
  source: z.enum(['ci', 'collector']),
  detail: text(4000).optional(),
});

export const AttemptSchema = z.object({
  dispatchId: id,
  agent: text(64),
  state: text(64),
  failedStage: text(64).optional(),
  lastError: text(2000).optional(),
  outcome: z.enum(['succeeded', 'failed']).optional(),
  startedAt: z.string().max(40).optional(),
  endedAt: z.string().max(40).optional(),
  // Agent-authored: the worker_done summary. Rendered as quoted text, never as UI.
  summary: text(4000).optional(),
});

export const GitSchema = z.object({
  branch: text(255),
  base: text(255),
  worktree: text(1024).optional(),
  commits: z.array(z.object({sha: z.string().regex(/^[0-9a-f]{7,40}$/), subject: text(300)})).max(200),
  files: z.array(z.object({path: text(1024), added: z.number().int().min(0), removed: z.number().int().min(0)})).max(500),
});

export const TaskSchema = z.object({
  id,
  title: text(200),
  spec: text(8000),
  status: text(64),
  deps: z.array(id).max(32).default([]),
  attempts: z.array(AttemptSchema).max(16).default([]),
  git: GitSchema.optional(),
  checks: z.array(CheckSchema).max(64).default([]),
});

export const GateSchema = z.object({
  id,
  taskId: id,
  question: text(1000),
  options: z.array(text(120)).min(1).max(8),
  status: z.enum(['pending', 'resolved']),
  resolution: text(120).optional(),
});

// An agent's own report, written in OpenUI Lang with the restricted agent library.
export const AgentReportSchema = z.object({
  taskId: id,
  dispatchId: id,
  agent: text(64),
  lang: text(64 * 1024),
});

export const RunRecordSchema = z.object({
  version: z.literal(1),
  run: z.object({id, objective: text(1000), host: text(255), createdAt: z.string().max(40), collectedAt: z.string().max(40)}),
  tasks: z.array(TaskSchema).max(64),
  gates: z.array(GateSchema).max(64).default([]),
  agentReports: z.array(AgentReportSchema).max(64).default([]),
});

export type Check = z.infer<typeof CheckSchema>;
export type Attempt = z.infer<typeof AttemptSchema>;
export type Task = z.infer<typeof TaskSchema>;
export type Gate = z.infer<typeof GateSchema>;
export type AgentReport = z.infer<typeof AgentReportSchema>;
export type RunRecord = z.infer<typeof RunRecordSchema>;
