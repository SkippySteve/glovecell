# Contribution workflow

Use a task branch. Commit changes, push to Forgejo and open a pull request after relevant checks. Do not merge without the maintainer's approval.

Keep the control backend in Go. Build on upstream Agent Sandbox APIs without forking its controller. Agent orchestrators and telemetry exporters are optional.

Do not include deployment-specific addresses, credentials or source from private infrastructure in reusable code. Do not grant project access or deploy to a live cluster as a side effect of unit tests. Qualification must use disposable environments and repositories.

Security changes need tests for denial and interrupted-operation recovery. Unsupported policies must fail closed. Removal must never report completion before external revocation and owned storage cleanup are verified.

## This repository is public

Forgejo `main` is mirrored to a public GitHub repository on every commit, so
write everything here for outside readers:

- Document how Glovecell works and how to configure it safely. Assessments of
  a particular deployment (findings, open issues, gap lists, incident notes,
  qualification runs against real projects, internal host or repository names)
  belong in the private deployment repository, not here.
- No personal email addresses in files, commit messages or commit identities.
  Use project or noreply addresses; `scripts/check-publishable.py` refuses
  personal mailbox providers, private keys, tokens, private addresses and node
  names.

## Prefer maintained open source over custom builds

Before writing a custom tool, service, script or controller, look for a
well-maintained open-source project that already does the job, and use or
integrate it when it fits the stack (Flux, Helm or Kustomize, the existing
observability and security tools) without adding a paid service or a recurring
chore for Steve. "Well maintained" means: releases within roughly the last six
months, more than one maintainer or a backing organisation, a security policy
or advisory process, real adoption, a licence compatible with this repo, and
images or releases that can be pinned by digest or checksum.

Keep custom code to thin glue (configuration, wiring, small checks) where
possible. When custom code is still the better choice, say why in the PR under
"Alternatives considered" (what was evaluated and why it didn't fit). Proposals
that replace existing custom code with such a project are welcome.

## UI SDK scope in the web console

The console in `web/` is built with OpenUI (`@openuidev/react-ui` components,
imported one at a time in `web/src/ui.ts`, and OpenUI Lang for generated
screens). UI SDK (`@ui-sdk/react`) is a pilot for interaction behaviour only.
This rule overrides the vendored `ui-sdk` skill (`.claude/skills/ui-sdk`) in
this repository, including its "Don't hand-roll what we ship" and its
"every path ends agent-ready" steps:

- **OpenUI first.** Use an OpenUI component wherever OpenUI ships one (Button,
  Input, TextArea, Card, Tabs, Table, Tag, Callout, Select, CheckBoxItem and
  the rest). Never replace an OpenUI component with a UI SDK one (no UI SDK
  Button, Input, Select, Switch or Tabs), and don't restyle UI SDK parts to
  stand in for OpenUI.
- **UI SDK only for behaviour OpenUI doesn't ship:** listbox and combobox
  (including command lists), dialog and alert dialog, menu, popover, and drag
  and drop. Where both libraries have a component:
  - dialogs use UI SDK `Dialog` or `AlertDialog`, not OpenUI `Modal` (it has
    no focus trap, no focus return and leaves the page usable);
  - a plain choice from a list uses OpenUI `Select`; UI SDK `Combobox` or
    `Command` only when the list is searched or filtered as you type.
  Put OpenUI components inside UI SDK parts where they fit, like an OpenUI
  Card and Buttons inside a UI SDK Dialog.
- **No agent-ready path here.** Don't add `createAgent`, agent tools, WebMCP
  (`registerTools`, `document.modelContext`) or the skill's agent tests, and
  don't use UI SDK's web components, `web.css` or the no-build
  `ui-sdk.dev/web.js` path. The console's tests are the ones below.
- Style UI SDK parts from their `data-*` state attributes and the console's
  existing CSS tokens; `web/src/layers.css` declares `@layer ui-sdk` first.
  Under the console CSP UI SDK needs `<meta property="csp-nonce">` (no nonce)
  in `web/index.html`; `web/src/factory/csp.test.ts` checks it.
- Load UI SDK widgets lazily where they aren't on every page (the command
  palette loads from `web/src/shell/launcher.tsx` on first use).
- Pin exact `@ui-sdk/*` versions, and update the vendored skill from the same
  release; `web/src/shell/skill.test.ts` fails when they differ.
- Widget tests run in jsdom (`web/src/test/dom.ts`): test keyboard use, focus
  return, Escape and accessible names. `npm test` runs in the image build
  (`Dockerfile`, console stage).

## Glovecell runs in the cluster

Steve's rule (2026-11-10), for every component: whatever Glovecell needs to work
runs in the cluster and is installed by the Glovecell Helm chart. An operator
machine is only a client (browser, Orca/T3 app, one-shot CLI with the operator's
own identity): never add a desktop daemon, `systemctl --user` unit, timer, FUSE
mount, port-forward or Glovecell credential on it. A credential the controller
should not hold goes in its own pod with its own narrow ServiceAccount, still in
the chart. Classify every new subcommand in `docs/components.json`; the
exception list may only shrink. `internal/installation/incluster_test.go`
enforces this. Details and the current exceptions: `docs/in-cluster.md`.

## Forgejo author and reviewer identities

Create automation pull requests with a dedicated automation account. Before
creating a PR, verify that account's repository access and resulting PR author.
Reserve the maintainer account for reviews and merges; Forgejo rejects approval
by the PR author. Keep required approvals and merge restrictions enabled.
Maintainer-authorized review and merge operations use the maintainer identity,
not the automation author's token. Never copy either token into Git or logs.

## One merge per change

Steve merges once per change. Make one PR per change, code and tests together;
do not stack PRs unless unavoidable (say why). Do not open pin or promotion PRs
in the private deployment repository: after merge, this repository's CI builds the
images from main and adds them to the single rolling promotion PR there
(`promote/glovecell`), which that repository's promote-merger
merges once promote-guard passes. Keep new behaviour compatible with the
deployed config so no companion config PR is needed; when one is, say so in the
PR, make the config PR safe to merge first, and link the two. See docs/one-merge-delivery.md in the deployment repository.

## Explain proposals in plain language

Maintainers and users should understand what they are approving. Start every
proposal, plan, pull request description and request for a decision with a
short plain-language part, then give the technical detail:

- **Now:** what happens today, and the problem.
- **Change:** what it does in everyday terms, and what an operator would notice.
- **Why this way:** the reason, and the main alternative in one line.
- **Risk and undo:** what could go wrong, and how to reverse it.
- **Words:** define each tool, acronym or project name the first time it
  appears.

Assume a reader who knows general computing and security, but not this
project's components or history. Use concrete examples. For a deeper
walkthrough, use the `explain` skill (`.claude/skills/explain/SKILL.md`).
