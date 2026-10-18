# Factory run reports

The console's **Runs** page shows one report per orchestration Run. Each report is
built from a run record, the factory's source of truth for that Run: tasks, attempts,
worktrees, commits, check results, decision gates and the agents' own reports.

## Run records

Set `RUN_RECORDS_DIR` to an absolute directory to enable the page. Each Run is one
`<run-id>.json` file, at most 2 MiB, whose `run.id` matches its file name. Run ids
use letters, digits, `_` and `-`. The controller opens records inside that directory
only, so links and other paths are refused, and it leaves invalid records out of the
list. `GET /api/v1/runs` lists runs, newest first; `GET /api/v1/runs/{id}` returns
one record. Both need the console login.

The schema is `web/src/factory/record.ts`. The console validates every record
against it before rendering and refuses one that fails. Check results must come
from CI or the collector; an agent cannot report them.

## How a report is built

`web/src/factory/project.ts` projects a record into OpenUI Lang with OpenUI's own
serializer, so the same record always produces the same report and text from a
record can never become components. The console renders it with `reportLibrary`.

Agents write their own report in OpenUI Lang with the smaller `agentLibrary`:
headings, paragraphs, lists, tables, code and notes. It has no links, actions,
checks or gates, so an agent report cannot show a passing check or a control. It
appears inside a dashed frame labelled "Written by <agent> · not verified by the
collector". Agents follow `docs/factory/agent-report-prompt.md`, which is generated
from the agent library.

Gate buttons raise an action only. The console accepts it when it names a pending
gate in the record and one of that gate's options, then shows a confirmation with
the `orca orchestration gate-resolve` command to run. The console does not reach
the Orca runtime itself.

## Content Security Policy

OpenUI's renderer injects one stylesheet, its query-loader keyframes, on mount. The
console CSP allows exactly that stylesheet by hash; `web/src/factory/csp.test.ts`
fails if a react-lang upgrade changes it. Queries and tool calls are not enabled.

## Tests

`npm test` in `web/` runs the projection, injection, agent-library, schema, CSP and
prompt tests. `APC_BROWSER_TEST=true go test ./internal/web -run TestBrowserRuns`
drives the Runs page in Chromium with the fixtures in `internal/web/testdata/runs`.
