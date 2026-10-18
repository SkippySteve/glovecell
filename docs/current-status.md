# Current source status

Updated 2026-10-18.

The current source combines the OpenUI console, native sandbox pages, repository
access, provider renewal, and factory run reports. The console uses the Go
sandbox-control API for existing sandbox summaries and access controls. Factory
reports use a read-only mailbox and validate records before storing them.

This consolidation is on `SkippySteve/cleanup-controller-current` until its
Forgejo pull request reaches `main`. Earlier PRs targeted feature branches;
their merged status alone did not mean the changes were on `main`.

The published `0.1.0-alpha.1` images predate this console. Build and pin an image
from the reviewed source revision before deploying these features. Factory
mailbox rollout also needs the runtime-side service and the settings described
in [factory reports](factory.md).

## Project boundary

This repository owns the reusable controller, APIs, web console, and Helm chart.
Private cluster provisioning and the sandbox runtime belong to the infrastructure
repository. The deployment-source repository owns the reviewed Flux copy, and
the dashboards repository owns Grafana dashboards. The factory project documents
how those components work together.

The earlier static dashboard and UI design branches are historical experiments.
The current console source is `web/src`, compiled into `internal/web/dist`.
The separate `checkpoint/project-access-20261018` branch preserves unfinished
local client-registration and mount work. `checkpoint/access-concepts-20261018`
preserves retired design mockups; its preservation PR is closed. Client
registration remains draft PR 28 because its older dashboard changes conflict
with the current console. Neither checkpoint is part of this consolidation.

## Working in Orca

Open this repository as its own project. Create new worktrees from the default
branch once the consolidation PR is merged. Use explicit parent worktrees only
for changes that depend on another unfinished change. Merge finished work into
the default branch before deleting its feature branch.

Run the Go tests and the console tests and build before publishing a new image.
Repository tests must use disposable resources and must not deploy to a cluster.
