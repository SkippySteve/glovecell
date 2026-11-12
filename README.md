# Glovecell

**One locked-down Kubernetes sandbox per project** — so coding agents can work
on a real repository without owning your cluster, your Git admin credentials,
or an open path to the internet.

Glovecell sits on top of upstream
[Kubernetes Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox).
Agent Sandbox runs the pod lifecycle. Glovecell owns everything that makes a
sandbox *safe for agents on your code*: Git grants, branch protection, inspected
egress, credential injection outside the cell, pause/revoke with verified
cleanup, and a console operators actually use.

Apache-2.0. Alpha quality — see [qualification](docs/qualification.md) and
[security](docs/security.md). Adoption and swappable-deps plan:
[roadmap](docs/roadmap.md).

## What you get that other stacks don't

Most agent platforms give you a container (or a dense “actor” runtime) and leave
Git, HTTPS, and file movement as exercises. Glovecell is built around **project
cells**:

| Capability | Why it matters |
| --- | --- |
| **One cell per repository** | Compromise or confusion stays inside that project's boundary. Agents that share a cell share its trust boundary — so we don't share by default. |
| **Git host is part of the product** | Disposable, non-admin identities and repository-scoped tokens; main is protected (no direct push; reviewed merges). Admin Forgejo/GitHub credentials never enter the cell. |
| **Fail-closed inspected egress** | Workers talk only to their project proxy (and DNS for that). Cilium blocks bypass. The proxy MITMs HTTPS and allows **exact host + method + path** rules — not “whatever the agent can curl”. |
| **Secrets that never sit in the cell** | Some credentials (e.g. private GitHub read, Muse) stay in the control plane; the proxy injects them on the way out when the worker sends a placeholder. |
| **Operator console for policy** | Locked / Ask / Standard / Open levels, per-project Internet rules, optional LAN grants, Files, history — without giving agents cluster RBAC. |
| **Pause ≠ revoke ≠ delete** | Pause keeps data; removal revokes grants, stops the cell, and tracks cleanup until it is observed. Access revoked is not assumed equal to data gone. |
| **Optional phone relay (hub-and-spoke)** | Pair a phone once with a hub; Ready cells enroll as spokes under gVisor. See [hubspoke](docs/hubspoke.md). |

It **consumes** Agent Sandbox; it does not fork it. It is **not** a multi-tenant
SaaS harness and **not** a replacement for Cilium, gVisor, or your identity
provider — those are the floor Glovecell stands on.

## Security model (short)

```
Operator (OIDC) ──► Controller + console ──► Git host admin API
                         │
                         ├── ProjectWorkspace CR (desired state)
                         ├── Per-cell namespace: worker (gVisor) + inspecting proxy
                         └── Audit / ledger (no secrets in logs)
```

- **Isolation:** qualified profile uses **gVisor** RuntimeClass + **Cilium**
  network policy. Ordinary containers are not that profile.
- **Control plane vs cell:** cluster-admin and Git-admin powers live only in the
  controller namespace. Cells have **no ServiceAccount token** for the API.
- **Egress:** default deny from the worker except to its proxy. Policy that
  cannot be enforced is **rejected**, not silently widened. Certificate-pinned
  and WebSocket clients stay blocked under inspection-required policy.
- **Bodies:** allowed HTTPS requests can still carry source or secrets in the
  body — path filters are not a confidentiality product. Treat telemetry and
  model APIs as outbound channels you explicitly allow.
- **LAN grants (optional):** one IPv4 `/32` + listed TCP ports, off by default,
  never a path around the proxy to the public internet. Uninspected by design;
  grant the narrowest port.

Details and threat notes: [docs/security.md](docs/security.md).

## How egress works

1. The controller installs a **Cilium** policy so the worker can only reach its
   project proxy (and the DNS needed for that name).
2. The worker uses `HTTP(S)_PROXY` to that proxy. The proxy terminates CONNECT,
   re-originates TLS to the real host (verifying upstream certs), and applies
   the project's allow list: host, method, path prefix.
3. Operators edit grants in the console **Internet** page (or API). Reference
   rules can give every project read-only access to a fixed catalog of
   upstream docs/repos without copying them into each cell.
4. **Brokered credentials** (where configured) are swapped at the proxy so the
   cell never holds the real token.

Proxy env vars alone are not the boundary — Cilium is. See
[console](docs/console.md) (Internet / LAN) and [installation](docs/installation.md).

## How file transfers work

Agents must not get a general “download anything” pipe. Transfers are
**operator-gated** and run as the sandbox user under `/workspace`.

**Into the cell (download)** — off until you enable downloads:

1. In **Files**, enter an HTTPS URL (and destination under `~/`).
2. Glovecell **checks the redirect chain** and shows every hop (query strings
   hidden unless you opt in — signed URLs often put tokens there).
3. On approve, a short-lived Cilium grant lets **only that project's proxy**
   reach the exact host(s); the worker fetches through the proxy; the helper
   writes atomically under `~/downloads/…` with size and SHA-256 checks.
4. Grants expire quickly; history records URL, size, hash, outcome — not file
   contents.

**Into the cell (upload):** you download on your own machine, then **Upload
from browser** into `~/uploads/…` (same path rules). Bytes ride the
authenticated console path, not an open agent egress rule.

**Out of the cell:** not a built-in “exfil API”. Ship artifacts the way you
already trust (PR to Git, or your own approved outbound path). Do not rely on
accidental proxy allows for bulk export.

More: [security — downloads](docs/security.md#downloads-into-a-sandbox),
[console — Files](docs/console.md).

## Who benefits

- **Homelab / platform operators** who want agents on private Forgejo (or
  GitHub) without handing them kubeconfig or a flat “allow *” egress policy.
- **Security-minded teams** that need inspectable HTTPS, per-project policy,
  and auditable revoke — not a shared forever-on GPU box.
- **People comparing Substrate / kagent / desktop harnesses** who still need
  **Git lifecycle + cell egress + files** as one product, not three wiki pages.

## Intended workflow

1. Operator signs in (OIDC) and opens a repository in the console.
2. Controller creates the cell, scoped Git identity, storage, clone, proxy.
3. When Ready, work via console, `connect` / `exec`, or an optional orchestrator
   (hub-and-spoke phone relay, T3, etc.).
4. Pause to stop compute and keep data; **Remove** to revoke and clean up.

## Install (alpha)

Digest-pinned images and Helm: [quickstart](docs/quickstart.md),
[installation](docs/installation.md), [examples](docs/examples/).

The chart does **not** install Agent Sandbox, Cilium, gVisor, Forgejo, your
IdP, or the [CloudNativePG](https://cloudnative-pg.io/) operator that runs its
database (or set `database.mode: external` to use a PostgreSQL you already
have). Qualified combination (pilot): Kubernetes 1.36.x, Agent Sandbox 1.0.4,
Cilium 1.19.x, gVisor, Forgejo, OIDC (e.g. Authentik). Deploy digests, not
floating tags.

Published alpha images (rebuild from this tree for current features):

- `ghcr.io/skippysteve/agent-project-controller@sha256:084fdfa09f04a279d6ea11bf168222e388f9f5230ddda38a62dcade5f9b9b9d3`
- `ghcr.io/skippysteve/agent-project-workspace@sha256:caf59a3dac5b216aa670f02cc4db857cf7b214d346fc844722f7a7c602ba9892`

(Release `0.1.0-alpha.1` predated the Glovecell name; current `main` is ahead
of that tag.)

## Development

Use Go 1.27.4 or later, Git and kubectl for manifest rendering. Run `make test vet build render`. Install the published CLI from the release rather than `go install` of this module. Contribution rules are in [CONTRIBUTING.md](CONTRIBUTING.md). Optional local Kubernetes API and disposable Forgejo qualification are described in [qualification](docs/qualification.md).

## License

[Apache-2.0](LICENSE).
