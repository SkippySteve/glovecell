# Current source status

Updated 2026-11-12.

## Repository

This repository is `steve/glovecell` on the maintainer's Forgejo (renamed from
`steve/agent-project-controller` on 2026-11-12; old Forgejo links redirect). Its
`main` is pushed to the public GitHub repository
[SkippySteve/glovecell](https://github.com/SkippySteve/glovecell) on every
commit. Because of that, `CI / checks` runs `scripts/check-publishable.py`
first: it refuses private keys, tokens, private network addresses and node
names in a pull request's commits and in the tree on `main`. Run it before you
push:

```sh
python3 scripts/check-publishable.py --range origin/main..HEAD
python3 scripts/check-publishable.py
```

The Go module path, the binary, the Helm chart directory
(`charts/agent-project-controller`) and the image package names
(`agent-project-controller`, `agent-project-workspace`) keep the old name for
now. The API group rename (`projects.agentwork.dev`) is also still to do.

## Release

The last release is `0.1.0-alpha.1` (commit `6d754c47`). Its images are
anonymously pullable from GHCR. The tag is on GitHub, but its CLI, chart and
`SHA256SUMS` files are not attached there at the moment; the
[quickstart](quickstart.md) says how to build them from the tag. Current
`main` is far ahead of that release; build and pin images from a reviewed
revision before deploying newer features.

## What `main` contains (beyond the alpha)

- Console, API, PostgreSQL history and approvals, files in and out of cells,
  LAN grants, access levels ([console](console.md), [database](database.md)).
- Brokered Grok and Cursor subscriptions with 5-minute leases
  ([brokered providers](brokered-providers.md)); optional per-cell stand-in
  tokens (`PROVIDER_CELL_TOKENS`, off by default).
- GitHub brokering ([design](design/github-brokering.md)) and a forge agent
  that can hold the Forgejo admin token in its own pod (off by default).
- Hub-and-spoke phone relay ([hubspoke](hubspoke.md)); an option binds the hub's
  pod-log reads to each project namespace instead of the whole cluster.
- OpenTelemetry for every long-running process, with credentials redacted
  before logging, and pull-only cell telemetry through a relay container in
  each proxy pod (`CELL_TELEMETRY_SCRAPER_JSON`, off by default;
  [installation](installation.md#cell-telemetry)).
- Proxy drift detection that compares the proxy Deployment in both directions.
- Runtime image with Git 2.52.1 or later (three HIGH CVEs in older Git).

In the maintainer's reference deployment the cell telemetry relay is on and
two qualification cells exist; per-cell provider tokens and the forge agent
are still off. Live qualification status is in [qualification](qualification.md).

## Project boundary

This repository owns the reusable controller, APIs, web console, and Helm chart.
Private cluster provisioning, deployment values and the sandbox runtime belong
to the private infrastructure repository. Glovecell's rule is that everything it
needs runs in the cluster and is installed by the chart; operator machines are
clients only (pull request #133 adds the written rule and its check). The
desktop registrar described in [control-host clients](clients.md) is optional
and off by default.

The current console source is `web/src`, compiled into `internal/web/dist`.

## Working in Orca

Open this repository as its own project. Create new worktrees from the default
branch. Use explicit parent worktrees only for changes that depend on another
unfinished change. Merge finished work into the default branch before deleting
its feature branch.

Run the Go tests and the console tests and build before publishing a new image.
Repository tests must use disposable resources and must not deploy to a cluster.
