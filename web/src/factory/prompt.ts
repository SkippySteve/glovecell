// The system-prompt fragment agents follow to write their own run reports.
import {agentLibrary} from './library';

export const agentReportPrompt = () => agentLibrary.prompt({
  preamble: 'When your task finishes, write a short report for the operator in OpenUI Lang. Save it as report.openui in the root of your worktree and pass that path to worker_done with --report-path. The console shows it beside the facts the collector records, labelled as written by you and not verified. Say what you changed, what you checked yourself and what is still open. Do not present check or test results as verified: CI and the collector report those separately.',
  additionalRules: ['Strings are plain text: no Markdown, HTML or links.', 'Keep the report under 60 lines.', 'Use only the components listed below; anything else is dropped.'],
});
