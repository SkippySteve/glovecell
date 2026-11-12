# Design decisions

Short records of decisions behind [glovecell.md](glovecell.md). Each entry:
date, decision, why, consequence. Newest last. Change a decision by adding a
new entry that supersedes it; do not edit old ones.

## Now / Change / Why this way / Risk and undo / Words

- **Now**: the reasons behind Glovecell's shape are spread over chat, issues
  and pull requests.
- **Change**: one list, one entry per decision.
- **Why this way**: reviewers and future contributors can see what is settled
  and what is still open without rereading history.
- **Risk and undo**: none; it is documentation. Superseding entries keep the
  history.
- **Words**: *ADR* (architecture decision record) is this short
  decision-plus-reason format. *Cell*, *kit*, *window*, *grant* are defined in
  [glovecell.md](glovecell.md#words).

## D1 — One sandbox system (2026-10-26)

Project sandboxes from this controller become Glovecell: one cell per Git
repository (Agent Sandbox `Sandbox` under gVisor, its own egress proxy, its own
CA). The older deployment-specific workspace controller and its workspaces
retire. *Why*: one system to secure, explain and publish.

## D2 — Rename before publishing (2026-10-26)

agent-project-controller → `glovecell`: repository, Go module, images, Helm
chart, CRD group `projects.agentwork.dev` → `glovecell.dev`, kind
`ProjectWorkspace` → `Cell`. Existing objects migrate by adoption
(glovecell.md section 14.2). *Why*: a rename after publication breaks users.

## D3 — Per-cell proxy is the only egress engine (2026-10-26)

Learning/open/locked windows, exact request approvals, package-version rules,
credential injection (providers and forge), blocked reports, dev links and
per-request audit move into the per-cell proxy. Cilium enforces L3/L4 only; no
Cilium L7 TLS interception for cells. *Why*: least privilege (one proxy, CA and
credential set per cell), one decision point, HTTP/2 support, no RE2 rule
limits, avoids known gVisor/SNI interception resets.

## D4 — One LAN-grant model (2026-10-26)

Deployment allowlist (`HOST: PORT,…` or `HOST: all`, required denylist) →
per-cell allowlist (admission-checked) → timed, pod-bound grants (15m, 1h, 4h;
extend; restrict now restarts the pod), learned ports from flow logs, history.
*Why*: merges the time-limited grants and the per-project fixed lists, each
layer narrower than the last.

## D5 — One console (2026-10-26)

Per-cell page with a left sidebar (Overview, Internet, LAN devices, Packages,
Previews & dev links, Credentials, History) and a sticky header showing state
and active grants with countdowns, over one documented JSON API.

## D6 — Agent kit (2026-10-26)

Every cell gets a versioned bundle (skills, instruction fragments, agent
definitions, MCP/tool config) from Git on create and update, mounted read-only,
with per-cell additions. First contents: the plain-language rule and the
`explain` skill. Replaces the marker-based guide a runtime CLI appended.

## D7 — Orca runtime per cell (2026-10-26)

Adopt [project-orca-runtime.md](project-orca-runtime.md) as an optional cell
orchestrator, with egress and credentials through the cell's own proxy and no
shared runtime at the end of migration.

## D8 — Pluggable parts (2026-10-26)

Isolation runtime (gVisor today; gVisor-KVM first MicroVM-class candidate,
then Kata + Cloud Hypervisor), forge (Forgejo; GitHub and GitLab later),
telemetry (none or OTLP; optional OTel Injector app-telemetry module, off by
default), credentials backend (OpenBao).

## D9 — Open-source packaging (2026-10-26)

One Helm chart, documented CRDs, safe defaults, works without a telemetry
backend, no deployment-specific names, addresses or credentials.

## D10 — Repository consolidation (2026-10-26)

After migration the platform is two repositories: `glovecell` (public) and the
operator's private infrastructure repository. Generic runtime CLI and
automation loops move into Glovecell.

## D11 — Runtime audit stays fail-closed, without restart loops (2026-10-26)

Cell pods use non-exec probes; the audit gate refuses on new loss (counter
deltas) rather than any historical loss, and pauses agent work instead of
letting the kubelet restart the pod. Agent work stays fail-closed.

## D12 — Kubernetes-native API (2026-10-30)

The cell's model moves onto Kubernetes primitives, as set out in
[kubernetes-api.md](kubernetes-api.md):
- the audit list moves to Events and conditions;
- invariants become ValidatingAdmissionPolicy;
- access levels become `AccessProfile` presets and timed `AccessGrant`s;
- pending decisions become `AccessRequest`s, approved through RBAC on `/status`;
- `OrchestratorClass`, `ProviderClass` and `ForgeClass` follow the
  GatewayClass/StorageClass pattern;
- isolation is a `RuntimeClass` name;
- persona roles are aggregated.

`AccessGrant` replaces the sketched `EgressWindow` and `LANGrant`, and
`OrchestratorClass` replaces the `orchestrator: none | orca` enum. Supersedes
the admission webhook in section 3 wherever CEL can express the rule.

*Why*:
- tool-agnostic by construction (a new UI is a class and an adapter, with no
  core change);
- the console's levels of control are API objects that GitOps, `kubectl` and
  the console all see;
- security rests on mechanisms reviewers already trust (RBAC, CEL admission,
  API audit) instead of custom Go.

*Consequence*:
- New kinds land in `projects.agentwork.dev/v1alpha1` now and move to
  `glovecell.dev` with the D2 rename.
- Existing `ProjectWorkspace` fields keep working through translation and
  defaults, and migration never widens access.

## D13 — Answers to the open questions (2026-10-30)

Steve's answers to glovecell.md section 16, with the reasoning and the work
each one implies.

1. **Codex access: a central refresher (option B), with the broker standard
   for every secret, provider tokens included.**
   - One refresher holds the real refresh token and keeps the access token
     fresh. Cells only ever see placeholders, and the cell proxy swaps in the
     real bearer on provider hosts.
   - *To verify in a spike before building*:
     - Does Codex accept a placeholder `auth.json`? It reads account claims
       from the ID token locally, so the placeholder must be a well-formed
       token carrying the real, non-secret claims.
     - Does the proxy need to answer or refuse Codex's own token-refresh
       calls, so that it never tries to refresh with the placeholder?
   - Option C (API keys) remains the fallback. Its usage is billed separately,
     which conflicts with the zero-added-cost goal.
2. **Operational records in PostgreSQL, which is required.** Revised the same
   day: custom resources would clutter etcd and `kubectl` output.
   - **What goes in Postgres**, the records that are many or grow without
     limit:
     - exact request approvals;
     - access requests, both pending and decided;
     - history and audit of lifecycle, grants and approvals;
     - per-request proxy summaries.
   - **What stays as custom resources**, the objects that are few and
     declarative and that GitOps, admission and RBAC should see: the cell,
     `AccessProfile`, `AccessGrant`, the Class objects and `Kit`.
   - **Kubernetes Events** remain for `kubectl describe` only. They expire after
     about an hour (`--event-ttl`), so they are never the history.
   - **Who may approve** is enforced by the controller API: an approver group
     from the console's OIDC sign-in, checked on every write, with each
     decision recorded in Postgres and sent to OTel. This replaces D12's "RBAC
     on `accessrequests/status`".
   - **Running Postgres**: the chart either takes an external connection
     string or creates a CloudNativePG `Cluster` (CNCF; backups and failover).
     Schema migrations are versioned and run by the controller on start.
   - **If Postgres is down**, proxies keep enforcing the approvals they have
     cached. New approvals and requests can't be written, so new requests are
     refused (fail closed), and the console says why.
3. **WebSockets through the cell proxy: allowed per exact host, with
   frame-level audit.** Restrictions this brings:
   - The upgrade request is a normal inspected HTTPS request: exact host and
     path rules, and credential injection into its headers. After the upgrade
     the proxy sees frames only, so there are no per-message approvals.
   - Providers that authenticate inside the first frame, not in headers, can't
     use brokered credentials and stay refused.
   - Sockets close when the window or grant that allowed them ends, the level
     changes, or a maximum duration or idle time passes.
   - Byte and connection caps apply per cell. Audit records connection
     open/close, direction, frame counts and bytes, and optionally a bounded
     sample.
   - The proxy negotiates `permessage-deflate` off so frames stay readable.
     HTTP/1.1 upgrade comes first; HTTP/2 WebSockets (RFC 8441) come later.
   - Clients that pin certificates fail against the cell CA. They stay
     refused: there is no blind passthrough.
4. **Kit: a new repository holds the default kit. Signatures are required on
   release tags, not on every commit.**
   - The kit's instructions and skills run in every agent, so a tampered kit
     injects prompts everywhere.
   - A tag signed with Steve's SSH key proves he released that exact content.
     It holds even if a forge account or token that can push (such as the
     automation account) is compromised.
   - Agents may propose kit changes as PRs, but only signed tags ship. The
     check is on by default when a signer is configured.
   - The repository is created by Steve: the automation account cannot create
     repositories.
5. **Name and API group: undecided.** `glovecell.dev` is not owned yet.
   - Until a domain is owned, new kinds stay in the current group.
   - The rename (D2) waits for the purchase or for another domain Steve
     controls, and must happen before the API reaches v1beta1.
6. **Migrated namespaces: recreate as `gc-<uid>`, copying volumes.** The
   migration plan includes the copy and a check that it succeeded before the
   old namespace is removed.
7. **gVisor-KVM: optional, as a separate `RuntimeClass`.**
   - Benefit: gVisor uses hardware virtualisation to switch address spaces
     instead of its default trap mechanism. That gives faster system calls
     for build- and test-heavy cells, and is the first step towards
     MicroVM-class isolation (Kata with Cloud Hypervisor needs `/dev/kvm`
     too).
   - It needs `/dev/kvm` on the node: virtualisation enabled in firmware, or
     nested virtualisation if the node is a VM.
   - Offer it on nodes labelled as KVM-capable, and leave plain gVisor as the
     default.
8. **Dev-link scanner: required, failing closed.**
   - Every dev link is scanned before it can be approved, and there are no
     unscanned previews. When the scanner is down, links wait.
   - Bad results (vulnerabilities, secrets, risky dependencies) show as a
     prominent warning on the approval card, with the findings.
   - The approver decides with the warning in view, and the decision and the
     findings are recorded.
9. **Controller HA: one replica by default, with leader election ready for two.**
   - Proxies enforce the last policy they received and **expire windows and
     grants on their own clock**, so a controller outage can't extend access.
   - Approvals and grants are CRs, so they survive a restart.

## D14 — One gateway per cell (2026-10-30)

Each cell's proxy becomes the **cell gateway**, the only way into and out of
the sandbox. Its egress side is today's inspecting proxy (D3). Its ingress
side is a new reverse-proxy listener in the same pod for agent sessions (Orca,
T3, ACP), files, previews and dev links, and approved tasks.

Every transfer type is a named channel with one route, one authentication
rule, one policy check and one audit record: sessions, model providers,
repositories, web, packages, files, previews, MCP (local, cell tools,
remote), A2A (inbound tasks, outbound calls), LAN grants and telemetry. The
sandbox pod accepts connections only from its gateway, and reaches only its
gateway, DNS for it, and a LAN host while a grant is open.

The full map is in [network.md](network.md), with an interactive version in
[network.html](network.html).

*Why*:
- one place per cell to enforce, audit and explain traffic;
- new protocols arrive as channels, not as new holes;
- people and outside agents always come in through the edge sign-in.

*Consequence*:
- Inbound A2A tasks go to an intake in the controller and need approval,
  never a direct connection.
- An ACP bridge becomes the generic `acp` orchestrator class.
- orca-pilot's commands become the gateway's cell-tools MCP endpoint.
- The building order is in network.md section 7. Until the ingress side
  exists, today's separate inbound paths keep working.

## D15 — Voice control, and voice approval as the merge gate (2026-10-30)

The operator controls agents by voice, through a self-hosted Matrix homeserver
(Synapse) and the Element X app.

- Voice notes in become transcribed tasks for any agent target (Orca, Archon,
  Claude Code or Codex headless, and other orchestrators through D12 classes).
- Results come back as spoken voice notes, and agents can start voice messages
  through an authenticated notify API.
- Speech is self-hosted (speaches: faster-whisper and Kokoro) behind an
  OpenAI-compatible API, so cloud providers can be swapped in.
- **A spoken approval replaces clicking Merge, for every PR including one-way
  doors.** A trusted `approval-gate` merges only when all of these hold:
  - the approval comes from the operator's Matrix user on a verified,
    cross-signed device;
  - it carries a single-use code bound to the repository, PR and head SHA
    announced on the approval card;
  - the PR is unchanged and CI is green (or the operator explicitly overrides).

Agents still never merge. Live conversation (Element Call with a LiveKit agent)
is phase 2. Details: [voice.md](voice.md).

*Why*: the operator's only remaining job is approval, and it should work hands-free.

*Consequence*:
- Branch protection adds the `approval-gate` merger and a required
  `voice-approval` status. Supersedes "only the operator clicks Merge" (12.1).
- Voice is never identity on its own. The verified device is the identity, and
  the spoken code ties the decision to the change that was summarised.

## Provider login source (decided in D13)

Central refresher with broker injection for all provider tokens (D13 item 1),
pending the Codex placeholder spike.
