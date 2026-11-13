# Kubernetes-native API (decision D12)

How the cell's model maps onto Kubernetes primitives. This refines
[glovecell.md section 3](glovecell.md#3-resources-crds); the reasons are recorded in
[decisions.md, D12](decisions.md#d12--kubernetes-native-api-2026-10-30).
Field sketches, not final schemas.

## Now / Change / Why this way / Risk and undo / Words

- **Now**: one `ProjectWorkspace` resource holds HTTPS rules, repositories,
  LAN hosts and provider grants in its spec. A growing audit list lives in its
  status, and the controller's Go code checks every invariant.
- **Change**: split the model into small resources that follow patterns
  Kubernetes users already know (Class objects, policy attachment, conditions,
  Events, CEL admission, RBAC on `/status`).
- **Why this way**:
  - A new agent UI is a class plus an adapter, with no change to the core.
  - The console's levels of control are API objects, so GitOps, `kubectl` and
    the console all see the same state.
  - Reviewers can check security in mechanisms they already trust.
- **Risk and undo**:
  - Existing objects keep working through translation and defaults, and
    migration never widens access.
  - Each step is its own pull request and can be reverted alone.
- **Words**:
  - **Class object**: a cluster-scoped resource naming an implementation that
    other resources select by name (like `StorageClass`, `GatewayClass`).
  - **Policy attachment**: a policy object pointing at the resources it
    applies to with `targetRefs` (Gateway API GEP-713).
  - **CEL**: the Common Expression Language that Kubernetes evaluates
    in-process for validation.

## Resources

| Concern | Kubernetes shape | Pattern |
|---|---|---|
| Cell | Thin cell spec with repository, template, profile, orchestrator, providers and kit references. It creates an Agent Sandbox `Sandbox`/`SandboxTemplate` and never forks it. Isolation is a `RuntimeClass` name. | Composition |
| Levels of control | Cluster-scoped `AccessProfile` presets (`locked`, `ask`, `standard`, `open`, `custom`). Defaults inherit cluster → project namespace → cell. | Policy attachment |
| Timed access | `AccessGrant`, a union of `internet` window, `lan` host+ports, and `package`, with `expiresAt`, owned by the cell. Replaces `EgressWindow` and `LANGrant`. | TTL objects |
| Exact approvals | Rows in PostgreSQL, not custom resources (D13 item 2) | Database |
| Pending decisions | Access requests are rows in PostgreSQL, created by the cell proxy only when a human must decide. The controller API checks the approver's OIDC group on every decision and records it (D13 item 2). | Database plus API authorisation |
| LAN floor | Deployment allowlist and denylist as policy parameters. The controller generates `NetworkPolicy`, plus Cilium policy where present, for L3/L4. The per-cell proxy stays the only L7 engine (D3). | NetworkPolicy |
| Orchestrator | Cluster-scoped `OrchestratorClass` (`controllerName`, `parameters`). The cell sets `orchestratorClassName`, immutable once set; empty means none. Adapters implement Ensure/Suspend/Remove. | GatewayClass |
| Credentials | Cluster-scoped `ProviderClass` (`driver`, `provider`, lease bounds). Secrets never reach the pod. | StorageClass |
| Forge | Cluster-scoped `ForgeClass` (`driver`: forgejo now; github and gitlab later) | StorageClass |
| Agent kit | `Kit` as in glovecell.md section 8, with OCI image volumes as a later source | OCI artifacts |
| Previews | Later: `Preview` generating a Gateway API `HTTPRoute` behind an `Approved` condition | Gateway API |
| Invariants | `ValidatingAdmissionPolicy` and bindings with `paramKind` for deployment configuration, failure policy Fail. A webhook remains only for rules CEL cannot express. | Native admission |
| Personas | Aggregated ClusterRoles `…-viewer`, `…-approver`, `…-admin`. Agent ServiceAccounts get no write verbs on these kinds. | RBAC aggregation |
| Concurrency | `ResourceQuota` object counts per project namespace, plus `SandboxWarmPool` for fast start | Quota |
| History | PostgreSQL holds the durable history, also sent to OTel logs. Kubernetes Events are for `kubectl describe` recency only, since they expire after about an hour. Status keeps `conditions` and `observedGeneration`. | Conditions, Events, database |

What stays out of the API server:
- approvals, access requests, history and per-request proxy observations,
  which live in PostgreSQL (D13 item 2) and OTel;
- the proxy decision engine, broker token minting and the console;
- telemetry settings, which are chart values.

## Example

```yaml
apiVersion: glovecell.dev/v1alpha1     # projects.agentwork.dev/v1alpha1 until the D2 rename
kind: Cell
metadata: {name: bookorbit, namespace: proj-bookorbit}
spec:
  repository: {forgeClassName: forgejo, name: steve/bookorbit}
  sandboxTemplateRef: {name: default-gvisor}
  accessProfileName: ask          # locked | ask | standard | open | custom
  orchestratorClassName: orca     # or t3, herdr, none
  providers: [{providerClassName: claude}, {providerClassName: codex}]
  kitRef: {name: base, version: v3}
---
apiVersion: glovecell.dev/v1alpha1
kind: AccessGrant
metadata: {name: bookorbit-open-1, namespace: proj-bookorbit}
spec:
  cellRef: {name: bookorbit}
  internet: {mode: open}
  expiresAt: "2026-10-30T14:00:00Z"   # admission requires it, at most 8h
```

## Steps

Each step is one pull request, in this order:
1. Stop the growing audit list: conditions plus Events, with history read
   through an interface that PostgreSQL backs later.
2. Add ValidatingAdmissionPolicies for the invariants the Go code checks.
3. Add `AccessProfile` and `AccessGrant`, together with the console's levels
   of control.
4. Add `OrchestratorClass`, porting T3 first, then the existing Orca
   integration (the per-cell Orca runtime stays issue #51).
5. Add `ProviderClass` and `ForgeClass`.
6. Add PostgreSQL storage for approvals, access requests and history (D13).

New kinds land in `projects.agentwork.dev/v1alpha1` and move to
`glovecell.dev` with the D2 rename.
