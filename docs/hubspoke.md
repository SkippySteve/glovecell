# Hub-and-spoke orchestrators

Agents run inside locked-down **spokes** (project sandboxes). **Phones** pair
once with a **hub** that enrolls each Ready spoke automatically. New spokes
appear on an already-paired phone without another scan.

**Desktops** do not use this path for day-to-day work. They use the control-host
registrar (`register --watch` in [clients.md](clients.md)): FUSE mounts and
Orca/T3/Herdr project registration on Local. Hubspoke public dial and the
console mint API are for phone (and hub enrollment), not for opening a cell in
the desktop app's Host picker.

Operator pairing belongs in the console (**Hub** page), not kubectl: design in
[hub-console-pairing](design/hub-console-pairing.md), tracked as
[APC #113](https://git.cluster.aimingupward.com/steve/agent-project-controller/issues/113).

When hubspoke is enabled, Helm requires **both** `config.PUBLIC_ORIGIN` (console)
and `hubspoke.hub.publicAddress` (hub `wss://…` for pairing). They are separate
settings; do not invent the hub host from the console host.

This is the production phone shape for Orca. T3 and Herdr use the same Go
interfaces when their enrollment paths are verified.

## Words

- **Spoke**: one project sandbox. Runs the Runtime (Orca's `orcad` today) next
  to the agents, under gVisor and least privilege. Holds the repo and worktrees.
- **Hub**: one always-on control client. Relays phones to spokes. Runs no
  project agents. Enrolls spokes with `agent-project-controller hubspoke --watch`.
- **Runtime** (`internal/hubspoke.Runtime`): how to parse a spoke's one-time
  pairing offer.
- **Enroller** (`internal/hubspoke.Enroller`): how the hub adds, connects,
  registers a project path, and revokes a spoke.

## Security

- A compromised spoke cannot open another spoke.
- The hub can drive enrolled spokes' Orca sessions, but does not mount their
  filesystems and holds no Kubernetes exec into them for day-to-day work
  (it only reads worker logs to obtain the one-time offer).
- Pairing URLs are never written to ledgers, status, or logs.
- **Log read only in cells.** With `hubspoke.hub.logsPerProject: true`
  (`HUBSPOKE_HUB_LOGS_CLUSTER_ROLE`), the controller binds ClusterRole
  `<release>-hub-logs` (get/list/watch pods, get pods/log) to the hub's
  ServiceAccount with RoleBinding `orca-hub-logs` in each project namespace,
  so the hub needs no cluster-wide log read. The hub already lists pods per
  project namespace with an uncached client. To move an installation that
  grants the hub cluster-wide read: turn the setting on, check that every
  `apc-*` namespace has `orca-hub-logs`, then remove the cluster-wide binding.
  The binding stays until the project namespace is deleted; turning the
  setting off later does not remove existing bindings.
- Prefer the hub for phones over asking each device to pair every spoke.
  Desktops keep using the FUSE registrar ([clients.md](clients.md)).

## Public spoke dial (hub and phone)

Phones reach spokes through the hub relay. The hub (and optional direct phone
tests) need a **public** WebSocket per project:
`wss://<project>.<HUBSPOKE_SPOKE_DOMAIN>`. Set that domain (and the Gateway
listener) separately from `PUBLIC_ORIGIN` and from the hub's own
`hubspoke.hub.publicAddress`. The controller then creates an HTTPRoute and a
CiliumNetworkPolicy that lets Traefik (and the hub) reach worker port 6768.

Console Hub page / `POST /api/v1/workspaces/{name}/orca-pairing` (OIDC only)
mints a one-time `orca://pair…` offer for **phone** pairing when present in
worker logs. Never logged or stored. Do not document this as the desktop
"open project" workflow. `GET /api/v1/session` reports `orcaSpokePairing: true`
when configured.

## Enable (inert until both flags are set)

Controller:

```yaml
HUBSPOKE_ORCA_ENABLED: "true"
ORCA_RUNTIME_IMAGE: git.cluster.aimingupward.com/image-publisher/orca-pilot@sha256:…
HUBSPOKE_ORCHESTRATOR_CLASS: orca   # optional; default orca
HUBSPOKE_SPOKE_DOMAIN: orca.cluster.example          # desktop dial suffix
HUBSPOKE_SPOKE_GATEWAY_NAME: traefik-gateway
HUBSPOKE_SPOKE_GATEWAY_NAMESPACE: traefik-system
HUBSPOKE_SPOKE_GATEWAY_SECTION: websecure-cluster-aimingupward
```

Hub process (same network as the spokes, with the Orca desktop CLI that can
relay phones — see stablyai/orca#40040 for headless relay):

```bash
CONTROL_NAMESPACE=agent-project-controller \
HUBSPOKE_KIND=orca \
APC_ORCA_BINARY=/path/to/orca-ide \
HUBSPOKE_ORCHESTRATOR_CLASS=orca \
agent-project-controller hubspoke --watch
```

Workspaces select the class:

```yaml
spec:
  orchestratorClassName: orca
```

## Lifecycle

1. Workspace becomes Ready (clone finished). The worker runs `orcad` and
   publishes a Service `orca-runtime` on port 6768.
2. The hub scrapes worker logs for `orca_server_ready`, enrolls with
   `orca environment add`, **connects** (required for phone relay), and
   `orca repo add --path /workspace/repo`.
3. Ledger ConfigMap `hubspoke-<uid>` records `orca.state=enrolled` (never the
   pairing URL). Controller `Ensure` does not wait on that state (that would
   deadlock with Ready); `Remove` does wait for `revoked`.
4. On remove, the hub runs `orca environment rm` and sets `revoked`. Controller
   `Remove` waits for that.

## T3 and Herdr

| Kind | Runtime | Enroller | Status |
| --- | --- | --- | --- |
| `orca` | `OrcaRuntime` | `OrcaEnroller` | Implemented; phone relay needs Orca desktop (or #40040) |
| `t3` | `T3Runtime` | `T3Enroller` | Stub; candidate `t3 environment link` |
| `herdr` | `HerdrRuntime` | — | Stub; no pairing API yet |

## Local spike

A disposable Docker spike under `~/orca-spike` (operator workstation) proved
automatic enrollment, phone visibility of new spokes without rescanning, and
worktrees created on the spoke rather than the hub.
