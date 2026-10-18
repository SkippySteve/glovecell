<!-- Generated from web/src/factory/prompt.ts; run `UPDATE_GOLDEN=1 npx vitest run` in web/ after changing the agent library. -->

When your task finishes, write a short report for the operator in OpenUI Lang. Save it as report.openui in the root of your worktree and pass that path to worker_done with --report-path. The console shows it beside the facts the collector records, labelled as written by you and not verified. Say what you changed, what you checked yourself and what is still open. Do not present check or test results as verified: CI and the collector report those separately.

## Syntax Rules

1. Each statement is on its own line: `identifier = Expression`
2. `root` is the entry point — every program must define `root = AgentReport(...)`
3. Expressions are: strings ("..."), numbers, booleans (true/false), null, arrays ([...]), objects ({...}), or component calls TypeName(arg1, arg2, ...)
4. Use references for readability: define `name = ...` on one line, then use `name` later
5. EVERY variable (except root) MUST be referenced by at least one other variable. Unreferenced variables are silently dropped and will NOT render. Always include defined variables in their parent's children/items array.
6. Arguments are POSITIONAL (order matters, not names). Write `SomeComp([children], "row", "l")` NOT `SomeComp([children], direction: "row", gap: "l")` — colon syntax is NOT supported and silently breaks
7. Optional arguments can be omitted from the end
- Strings use double quotes with backslash escaping

## Component Signatures

Arguments marked with ? are optional. Sub-components can be inline or referenced; prefer references for better streaming.

AgentReport(children: (Heading | Paragraph | Bullets | KeyValues | DataTable | Code | Note)[]) — The root of an agent report. Put every other component inside it, in reading order.
Heading(text: string) — A section heading inside the report.
Paragraph(text: string) — A paragraph of plain text. No Markdown or HTML.
Bullets(items: string[]) — A bulleted list of short plain-text items.
KeyValues(items: {label: string, value: string}[]) — Label and value pairs, such as measurements or settings.
DataTable(columns: string[], rows: string[][]) — A table with column headings and rows of plain-text cells.
Code(text: string) — A block of code or command output, shown verbatim.
Note(tone: "info" | "warning", text: string) — A highlighted note: info for context, warning for a risk or open question.

## Hoisting & Streaming (CRITICAL)

openui-lang supports hoisting: a reference can be used BEFORE it is defined. The parser resolves all references after the full input is parsed.

During streaming, the output is re-parsed on every chunk. Undefined references are temporarily unresolved and appear once their definitions stream in. This creates a progressive top-down reveal — structure first, then data fills in.

**Recommended statement order for optimal streaming:**
1. `root = AgentReport(...)` — UI shell appears immediately
2. Component definitions — fill in as they stream
3. Data values — leaf content last

Always write the root = AgentReport(...) statement first so the UI shell appears immediately, even before child data has streamed in.
## Important Rules
- Choose components that best represent the content (tables for comparisons, charts for trends, forms for input, etc.)

## Final Verification
Before finishing, walk your output and verify:
1. root = AgentReport(...) is the FIRST line (for optimal streaming).
2. Every referenced name is defined. Every defined name (other than root) is reachable from root.

- Strings are plain text: no Markdown, HTML or links.
- Keep the report under 60 lines.
- Use only the components listed below; anything else is dropped.
