# Glovecell design: one agent-sandbox system

Status: proposed design, 2026-10-26. Decisions are recorded in
[decisions.md](decisions.md). This document is generic: it names no deployment,
address or credential. A deployment's own migration plan lives in that
deployment's infrastructure repository.

## Now

Two systems run AI coding agents in isolated pods today:

- **Project sandboxes** from this repository (agent-project-controller, "APC").
  One `ProjectWorkspace` object per Git repository gives a gVisor pod, its own
  inspecting HTTPS proxy, its own certificate authority and a fixed list of
  allowed hosts, methods and paths.
- **Workspaces** from an older deployment-specific controller
  ("sandbox-control"). It has richer controls (learning windows, exact request
  approvals, package-version rules, credential injection, blocked reports, dev
  links, time-limited LAN grants) but shares one proxy, one CA and one Cilium
  Envoy policy set between workspaces, and it cannot be published.

## Change

Keep one system, renamed **Glovecell**. Every Git repository gets one **cell**:
an isolated pod, its own egress proxy, its own CA and its own credentials. The
useful sandbox-control features move into the per-cell proxy and the console.
Every cell gets a versioned **agent kit** (skills, instructions, tool config).
Each cell can run its own Orca runtime. The isolation runtime, Git forge,
telemetry and credentials backend are pluggable. The project ships as one Helm
chart with documented CRDs.

## Why this way

- **Least privilege.** One proxy, CA and credential set per cell means one
  compromised cell cannot read or reuse another cell's traffic or secrets.
- **One thing to secure and explain.** Two controllers with overlapping
  policy models double the review surface.
- **Publishable.** The project-sandbox code is already generic and Go-only;
  sandbox-control is tied to one deployment.
- **Proxy over network-layer TLS interception.** The per-cell proxy already
  handles HTTP/2, exact request matching and credential injection; the
  network layer only needs to force traffic through it (section 6).

## Risk and undo

- The rename touches the API group of existing objects. A conversion step
  (section 14) keeps old objects readable until they are migrated; until the
  old group is removed, rolling back is reinstalling the previous chart.
- Porting features into the proxy could weaken it. Every ported feature keeps
  the existing rules: fail closed, deny private destinations, tests for denial
  and interrupted operations.
- Phases are independent and each has an exit check (section 15). Nothing
  retires until its replacement passes its check.

## Words

- **Cell**: one isolated agent environment for one Git repository: a
  namespace with a sandbox pod, a storage volume, an egress proxy and a CA.
- **Agent Sandbox**: the upstream Kubernetes project (`Sandbox` resource) that
  creates and suspends the pod. Glovecell builds on it without forking it.
- **gVisor**: a user-space kernel that intercepts the pod's system calls, so
  the agent never talks to the host kernel directly. `runsc` is its runtime
  binary.
- **MicroVM**: a very small virtual machine (Kata, Firecracker, Cloud
  Hypervisor) that gives each pod its own kernel.
- **Egress proxy**: the per-cell process every outbound web request must pass
  through. It decrypts HTTPS with the cell's CA, decides, and forwards.
- **CA (certificate authority)**: the key that signs the proxy's short-lived
  certificates so tools in the cell trust it. Each cell has its own.
- **Cilium**: the cluster network plugin. It enforces which IP addresses and
  ports a pod may reach (L3/L4, "network and transport layer"). It can also
  run **Envoy**, an HTTP proxy, to inspect traffic (L7, "application layer").
- **Window**: a time-limited egress mode: *learning* (allow and remember),
  *open* (allow, do not remember) or *locked* (only approved requests).
- **Approval**: one remembered, exact web request: method, host, path and a
  fingerprint of query and body.
- **Grant**: a time-limited permission, for example a LAN grant that lets one
  cell open TCP to one LAN host on some ports for one hour.
- **Kit**: the versioned bundle of agent skills, instruction fragments, agent
  definitions and tool config placed read-only into every cell.
- **Forge**: the Git hosting service (Forgejo today; GitHub or GitLab later).
- **Broker**: the part of the proxy that adds real credentials to approved
  requests, so agents only ever hold placeholders.
- **CR / CRD**: a custom resource and its definition, a Kubernetes object type
  that this project adds.
- **Orca runtime**: the agent orchestrator server (`orcad`) a desktop or phone
  client pairs with; in Glovecell it may run inside a cell.

## 1. Purpose and scope

Glovecell gives each Git repository a disposable, isolated place where AI
coding agents work, with the operator in control of everything that leaves it.

Goals:

- One cell per repository, created, suspended, resumed and deleted through one
  API and one console.
- Default deny for every outbound connection; every allowed request is
  attributable to a rule the operator can see and revoke.
- Agents never hold real credentials for the forge, model providers or
  package registries.
- Works with no telemetry backend; audit records are a core feature.
- One Helm chart, documented CRDs, no deployment-specific names.

Non-goals: being a general multi-tenant PaaS; running untrusted users' code for
the public; replacing the forge's review and merge controls; forking Agent
Sandbox.

## 2. Architecture

```
                 operator (browser, Orca desktop/phone)
                        |  OIDC login, CSRF-protected JSON API
                        v
 +---------------- glovecell-system namespace (trusted) -----------------+
 |  controller  ---- reconciles Cell, LANGrant, EgressWindow, Kit ------+ |
 |  console + API  (one Go binary with the controller, HA optional)     | |
 |  kit renderer   (fetches kit from Git, verifies, renders)            | |
 |  audit store    (bounded outbox -> optional OTLP / log export)       | |
 +-------|---------------------------------|----------------------------+
         | creates                         | reads/writes credentials
         v                                 v
 +--------------- cell namespace gc-<id> (one per repository) -----------+
 |                                                                       |
 |  Sandbox pod (gVisor or MicroVM)          egress proxy pod            |
 |  +---------------------------+   HTTP(S) +---------------------------+ |
 |  | agents, Orca runtime opt. |---------->| window/approval engine    | |
 |  | /workspace volume (PVC)   |  proxy    | package rules, broker     | |
 |  | kit (read-only mount)     |  only     | cell CA (leaf signing)    | |
 |  +---------------------------+           | audit -> controller       | |
 |        |  LAN grant only                 +-------------|-------------+ |
 |        |  (L3/L4, exact host:port, timed)              |               |
 +--------|----------------------------------------------|---------------+
          v                                              v
     one LAN host                       internet, forge, model providers
```

Enforcement layers, from outside in:

1. **Network policy (Cilium L3/L4).** The sandbox pod may reach only its own
   proxy, its DNS answer for that proxy, and LAN hosts with an active grant.
   The proxy may reach public addresses on 80/443, the forge and the
   controller. Nothing else.
2. **Egress proxy (L7).** Decrypts, matches the request against the current
   window, approvals and rules, injects credentials, records the decision.
3. **Isolation runtime.** gVisor (or a MicroVM) stops the agent from
   reaching the host kernel, other pods' files or the node network.
4. **Runtime audit.** System-call audit from the runtime (gVisor seccheck
   today) goes to a per-cell receiver; agent work stops if the audit path is
   broken (section 10).

The controller is the only component with Kubernetes write access to cell
namespaces. The proxy has no Kubernetes credentials; it reads its policy from
a projected, controller-written ConfigMap and its live state (windows,
approvals) from the controller's API over mTLS.

## 3. Resources (CRDs)

> **Refined by [D12](decisions.md#d12--kubernetes-native-api-2026-10-30)
> ([kubernetes-api.md](kubernetes-api.md)).** `AccessGrant` replaces
> `EgressWindow` and `LANGrant` below, `OrchestratorClass` replaces the
> `orchestrator` enum, and ValidatingAdmissionPolicy replaces the webhook
> wherever CEL can express the rule.

API group `glovecell.dev`, version `v1alpha1`. All are namespaced in the
system namespace unless noted; the controller owns everything it creates in a
cell namespace. Field sketches, not final schemas.

### Cell (replaces ProjectWorkspace)

```yaml
apiVersion: glovecell.dev/v1alpha1
kind: Cell
metadata: {name: my-repo}
spec:
  repository: owner/repo           # on the configured forge
  forge: default                   # name of a Forge config (pluggable)
  desiredState: Running            # Running | Suspended | Deleted
  runtime:
    isolation: gvisor              # gvisor | gvisor-kvm | kata-clh | firecracker
    orchestrator: none             # none | orca   (immutable once set)
    resources: {cpu: "4", memory: 8Gi}
  repositories: []                 # extra checkouts (as today)
  egress:
    https: []                      # fixed rules {host, methods, paths}, as today
    defaultWindow: Locked          # Locked | Learning (first start only)
    packages: {}                   # see section 4.4
    providers: [claude, codex]     # credential profiles the broker may inject
  kit:
    ref: default                   # Kit name
    additions: []                  # per-cell extra files (read-only to agent)
  lan:
    allowed: []                    # this cell's LAN allowlist subset (section 5)
  telemetry:
    export: none                   # none | otlp
    appTelemetry: false            # OTel Injector module, section 11
status:
  phase: Ready                     # Provisioning|Ready|Suspended|Failed|Deleting
  namespace: gc-3f9a…
  egress: {mode: Locked, expires: ""}
  kit: {version: "2026.10.1", digest: sha256:…}
  conditions: []                   # ProxyReady, AuditReady, KitApplied, …
  audit: []                        # bounded lifecycle events, as today
```

### EgressWindow and Approval

Live state that changes often; kept as CRs so it survives controller restarts
and is visible to `kubectl`, but written only by the controller API.

```yaml
kind: EgressWindow            # one per cell, name = cell name
spec: {mode: Learning, expires: "2026-10-26T12:30:00Z", openedBy: operator}
---
kind: Approval                # one per approved exact request
spec:
  cell: my-repo
  scheme: https
  host: registry.npmjs.org
  port: 443
  method: GET
  path: /left-pad/-/left-pad-1.3.0.tgz      # escaped path, exact
  fingerprint: sha256:…     # canonical query + bounded body; never the values
  package: {ecosystem: npm, name: left-pad, version: 1.3.0}   # when known
  source: learned           # learned | approved | rule
status: {lastUsed: …, uses: 12}
```

Approvals are capped per cell (default 4096, as today). At very large counts
the store moves to the controller database with the CR as a summary; the API
does not change.

### LANGrant

```yaml
kind: LANGrant
spec:
  cell: my-repo
  address: 192.0.2.10
  mode: ports                # ports | all
  ports: [1337, 2121]
  duration: 1h               # 15m | 1h | 4h
  openedBy: operator
status:
  expires: …
  state: Active              # Active | Expired | Revoked
  podUid: …                  # grant bound to one pod; restart invalidates it
```

The deployment-wide allowlist and denylist are chart values (section 5), not
CRs, so they change only through reviewed Git.

### Kit

```yaml
kind: Kit
spec:
  source: {git: https://forge.example/org/agent-kit, ref: v2026.10.1, path: kit/}
  verify: {commitSigner: …}     # optional signature check
status: {version: 2026.10.1, digest: sha256:…, files: 14}
```

### Admission

A validating webhook (fail closed) rejects: LAN addresses outside the
allowlist or inside the denylist; unknown isolation runtimes; orchestrator
changes after creation; https rules with wildcards or regular expressions;
writes to EgressWindow, Approval and LANGrant by anyone other than the
controller service account.

## 4. Per-cell egress proxy

Each cell runs its own proxy Deployment (one replica, no Kubernetes token) in
the cell namespace. It holds the cell's CA intermediate key (issued by the
controller, never mounted in the sandbox pod), the cell's policy and the
cell's credential handles. It is today's APC inspecting proxy extended with
the features below. Only CONNECT to 443 and plain HTTP to 80 are accepted;
every CONNECT is decrypted and parsed as HTTP/1.1 or HTTP/2. There is no raw
TCP fallback; WebSocket and certificate-pinned clients stay blocked unless a
rule names them explicitly (open question 6).

### 4.1 Request decision flow

```
request arrives (CONNECT host:443 or http://host)
  |
  +- 1. parse: reject escaped paths, alternate authorities, upgrades,
  |     bodies > 128 MiB, malformed HTTP/2                     -> DENY (400)
  +- 2. resolve host; any private, special-use, cluster or
  |     mixed public/private answer                            -> DENY
  |     (connect later uses the checked literal addresses only)
  +- 3. provider/forge route? (exact host + reviewed paths of an
  |     enrolled credential profile)                -> ALLOW + inject credential
  +- 4. fixed rule (spec.egress.https host/method/path prefix)  -> ALLOW
  +- 5. package rule (registry template + approved name@version) -> ALLOW
  +- 6. exact approval match (scheme, host, port, method, escaped
  |     path, query+body fingerprint)                          -> ALLOW
  +- 7. window:
  |       Learning -> ALLOW, record as Approval(source=learned)
  |       Open     -> ALLOW, record observation unapproved
  |       Locked   -> DENY, record observation as pending approval
  +- every branch: write one audit record to the outbox; if the outbox
     is full, DENY everything except step 3                  (fail closed)
```

The window deadline is evaluated on every request against the proxy's own
clock, so an expired window locks even if the controller is down. Redirects
are new requests. HTTP/2 streams are decided one by one on one connection.

### 4.2 Windows (learning, open, locked)

Ported from sandbox-control's learning engine:

- `Learning`: allow every public request and remember it as an Approval.
- `Open`: allow every public request, remember nothing (still listed).
- `Locked`: only rules, package rules, approvals and provider routes.

Default window 30 minutes, settable 1–1440 minutes; extend, shorten, lock now,
start fresh (clears learned approvals), resume (keeps them). A Learning window
denies new requests once the approval cap is hit; an Open window keeps
allowing but stops listing. Private, LAN, cluster and metadata destinations
are refused in every mode. New cells start `Locked` unless
`spec.egress.defaultWindow: Learning` asks for a first window.

### 4.3 Exact approvals

An approval matches scheme, host, port, method, escaped path and a canonical
fingerprint of sorted query parameters and, for non-provider requests, a
bounded (1 MiB) request body. Values are hashed, never stored. Changing a
download selector in a POST body therefore needs a new approval. Approvals can
be revoked one by one, promoted to a fixed rule (prefix), or exported as YAML
for Git review.

### 4.4 Package-version rules

Registry URL templates for npm, PyPI, Go modules, crates.io, Maven and
container registries are code-owned (no operator-supplied regular
expressions). An operator approves `ecosystem/name@version`; the proxy derives
the exact metadata and download paths. A lockfile import (npm, uv/pip, Go sum)
produces a draft list to review; nothing is saved from an upload until the
operator confirms. Metadata reads do not approve another version's download.
Package rules are network controls only; they cannot constrain what is already
cached in the cell.

### 4.5 Credential broker

Agents hold inert placeholders; the proxy replaces them on reviewed routes
only:

| Profile | Injected on | Source |
| --- | --- | --- |
| forge | the cell's own repository paths on the forge (Git smart HTTP, API) | short-lived forge token minted per cell by the controller |
| claude | model API and OAuth refresh paths | per-cell provider login (section 12.3) |
| codex | model API and OAuth refresh paths | per-cell provider login |
| registry | private registry pull paths, when configured | credentials backend |

A profile is an exact host list plus method/path rules, shipped in the chart
and visible in the console. It stays available when the window is Locked and
can never point at a private address. Response bodies that carry refreshed
tokens are rewritten so the agent sees a placeholder, and the real value goes
to the credentials backend. Provider request bodies stream without the 1 MiB
fingerprint bound.

### 4.6 Blocked reports

A cell-local CLI, `glovecell blocked --kind web|package|repository|lan|
credential|other --resource WHAT --reason WHY`, posts to the proxy's
`/glovecell/blocked` endpoint (reachable only from the sandbox pod, authenticated
by a per-pod token). The proxy forwards to the controller. Text is untrusted:
one printable line, 200/500 characters, 25 open reports per cell, a repeat
updates count and reason. Reports never grant anything; the console offers the
matching one-click action (approve request, approve package, open LAN grant).
A denied request automatically creates a pending entry the report can link to.

### 4.7 Previews and dev links

- **Preview**: a private URL to one running process in the cell, valid while
  that process lives.
- **Dev link**: a stable private URL for one scanned build of an app
  directory; survives restarts while files and dependencies are unchanged.

The agent runs `glovecell dev-link publish --name N --port P --note "..."`.
The controller starts a scan Job (read-only volume mount, gVisor, no token,
egress only to the scanner) that produces an SBOM and vulnerability/secret
findings, and the operator approves in the console. The gateway route then
points at the sandbox pod's port behind console login and a verification
card. A change to the directory's dependencies or files requires a new
approval. Ingress to the sandbox pod is allowed only from the gateway, and
only for approved ports.

### 4.8 Audit of each request

One metadata record per decision: time, cell, request ID, scheme, host, port,
method, path (no query values), fingerprint, decision, matched rule/approval,
window, bytes, duration, credential profile name (never the value). Records go
to a bounded durable outbox in the proxy (persistent volume), then to the
controller's audit store; export to OTLP/logs is optional. If the outbox
cannot accept a record, non-provider forwarding stops. Prometheus metrics:
`glovecell_egress_requests_total{decision}`, `glovecell_egress_window{mode}`,
`glovecell_audit_outbox_age_seconds`; per-cell identity is a label only when
the operator enables it (bounded cardinality).

## 5. LAN model

A LAN grant lets one cell open plain TCP to one host on the operator's home
network (a games console, a printer, a lab device) for a limited time. It does
not go through the egress proxy, which keeps refusing every private address;
it is a Cilium L3/L4 rule bound to one pod.

The model merges sandbox-control's time-limited grants with APC's per-project
fixed hosts. Three layers, each narrower than the one above:

1. **Deployment allowlist** (chart values, reviewed in Git):
   `lan.allowedHosts` lines `HOST: PORT, PORT` or `HOST: all`; exact IPv4
   only, at most 32 ports per host and 64 hosts; `lan.deniedCIDRs` is required
   (node, API, service and pod ranges, gateways). Loopback, link-local,
   metadata, multicast, broadcast and non-private space are always refused. A
   malformed list turns LAN grants off and shows the error; it does not stop
   the controller.
2. **Cell allowlist** (`Cell.spec.lan.allowed`, admission-checked): the
   subset of hosts and ports this cell may ever be granted. Empty means the
   cell cannot get LAN grants. This is APC #57's per-project list.
3. **Grant** (`LANGrant`): opened by the operator from the console for 15m,
   1h or 4h, on specific ports (subset of the cell list) or all ports (only
   on an `all` host). Bound to the current pod UID.

Operations: open, extend (adds a duration from now, capped at 4h ahead),
restrict now (removes the rule and restarts the pod so open sockets close; the
console asks for confirmation), revoke on expiry (rule removed; established
connections are cut by restarting the pod only if the operator chose
"restart on expiry"). History keeps opened, extended, restricted, expired,
applied, revoked and worker-restarted events.

**Learned ports.** Cilium Hubble flow observations of refused and allowed TCP
connections from the cell to allowlisted LAN hosts become "learned ports"
(newest first, with counts). The console offers "open with learned ports" and
a suggested allowlist line for Git review. Learning never widens a grant on
its own. Without Hubble the feature is hidden; grants still work.

Agents cannot open grants. They can file a blocked report with
`--kind lan --resource HOST:PORT`.

The read API reuses the field names of sandbox-control's `GET /api/lan`
(section 7.3) so the console code moves over unchanged.

## 6. Cilium L7 or proxy-only

**Recommendation: proxy-only for HTTP decisions, Cilium for L3/L4 only.** Cells
keep no Cilium L7 (Envoy) TLS-interception policy.

What each layer does in a cell:

| Concern | Enforced by |
| --- | --- |
| sandbox pod can reach only its proxy, DNS for it and granted LAN hosts | Cilium L3/L4 policy (default deny, both directions) |
| proxy can reach public 80/443, forge, controller | Cilium L3/L4 with CIDR exclusions for private space |
| host, method, path, query/body, packages, credentials | per-cell proxy |
| DNS answers | proxy resolves itself; sandbox pod gets only the proxy name |

Reasons:

- **One decision point.** Two L7 engines (Envoy and the proxy) must agree on
  every rule; mismatches either over-allow or break tools. Exact approvals,
  body fingerprints, windows and credential injection exist only in the proxy.
- **HTTP/2 and streaming.** The proxy already inspects negotiated HTTP/2
  stream by stream; Envoy's per-CNP interception adds a second TLS hop and
  has to be configured per host.
- **RE2 limits.** Cilium L7 path rules are RE2 regular expressions with size
  limits; exact-request approvals at thousands of entries do not fit, and
  regex rules are what this design avoids.
- **Known gVisor/SNI issues.** The qualified profile already disables Cilium's
  SNI interception for the proxy-to-gateway path because it reset same-node
  connections, and gVisor bypasses socket-level ClusterIP load balancing, so
  the proxy is reached via headless Service DNS. Adding Envoy interception
  back for cells reintroduces those failure modes.
- **Per-cell CA.** Cilium L7 TLS interception uses CA material per policy in
  the Cilium namespace; per-cell CAs would mean per-cell secrets in a shared
  agent namespace, which is weaker than keeping the key in the cell's proxy.
- **Portability.** L3/L4 is what the experimental standard NetworkPolicy
  provider can also express; L7 would pin the project to Cilium.

Cost: a compromised proxy process can reach any public 80/443 address. That is
accepted and documented: the proxy holds no agent code, runs a minimal image,
and its audit and metrics are outside the cell. Cilium still blocks it from
private space and from other cells.

## 7. Console and JSON API

### 7.1 Console layout

One console, served by the controller binary. The cell list leads to a
per-cell page with:

- a **sticky header**: cell name, state (Running, Suspended, Failed),
  egress window with countdown, active LAN grants with countdowns, open
  blocked reports count, Start/Suspend;
- a **left sidebar** with sections: Overview, Internet, LAN devices,
  Packages, Previews & dev links, Credentials, History.

| Section | Shows | Actions |
| --- | --- | --- |
| Overview | phase, conditions, runtime, kit version, Orca pairing state | start, suspend, delete, re-pair |
| Internet | window, pending requests, approvals, fixed rules, blocked reports | learn/open/lock, duration, approve/revoke, promote to rule |
| LAN devices | allowlisted hosts, active grants, learned ports, history | open, extend, restrict now |
| Packages | approved name@version per ecosystem, pending downloads | approve, remove, import lockfile |
| Previews & dev links | listeners, scan results, links | approve, revoke |
| Credentials | enrolled profiles and their routes, last use, login status | connect/reconnect provider login, disconnect |
| History | merged audit: lifecycle, policy changes, grants, approvals | filter, export |

The console is plain server-rendered HTML with small scripts using the JSON
API below, so it works without a frontend build server.

### 7.2 Authentication and CSRF

- Browser: OIDC login (`/auth/login`, `/auth/callback`), opaque HttpOnly
  `__Host-` session cookie, `SameSite=Strict`.
- Every state-changing request needs the configured `Origin` and a
  `X-Glovecell-CSRF` header equal to the token returned by `GET
  /api/v1/session` (double-submit, bound to the session).
- Automation: an explicitly configured bearer secret with a scope list
  (`read`, `cells`, `egress`, `lan`); OIDC access tokens are not accepted as
  bearer credentials.
- Operator actions that widen access (window open/learn, approvals, LAN
  grants, dev links, credential connect) require the operator role from an
  OIDC group claim; read-only viewers are possible.
- Agents never reach this API. Their CLI talks only to their own proxy.

### 7.3 Endpoints

All under `/api/v1`. Errors are `{"error": "<message>"}` with `400` (bad
input), `403` (auth, origin or CSRF), `404` (unknown cell), `409` (state
conflict or stale `resourceVersion`), `429` (limits). Times are RFC 3339 UTC
strings, `""` when unset; lists are never `null`.

| Method and path | Purpose |
| --- | --- |
| `GET /session` | identity, roles, CSRF token |
| `GET /repositories` | repositories on the configured forge |
| `GET /cells`, `POST /cells` | list (summary incl. `egress`, `egressExpires`, `pending`, `lanActive`), create |
| `GET/PATCH/DELETE /cells/{cell}` | read, change `desiredState` / resources, remove |
| `GET /cells/{cell}/egress` | window, pending, approvals, rules |
| `POST /cells/{cell}/egress/window` | `{"action": "learn"\|"open"\|"lock"\|"resume"\|"fresh"\|"duration", "minutes": 1-1440}` |
| `POST /cells/{cell}/egress/approvals` | approve a pending `observation`; `DELETE …/approvals/{id}` revokes |
| `PUT /cells/{cell}/egress/rules` | replace fixed rules (with `resourceVersion`) |
| `GET/PUT /cells/{cell}/packages` | approved packages; `POST …/packages/import` returns `{"draft": [{"ecosystem","name","version"}]}` |
| `GET /cells/{cell}/lan` | LAN section (fields below) |
| `POST /cells/{cell}/lan/grants` | `{"address", "mode": "ports"\|"all", "ports": [], "duration": "15m"\|"1h"\|"4h"}` |
| `POST /cells/{cell}/lan/grants/{address}/extend`, `/restrict` | extend, restrict now (restarts pod) |
| `GET /cells/{cell}/links`, `POST …/links/{name}/approve`, `DELETE …/links/{name}` | previews and dev links |
| `GET /cells/{cell}/credentials`, `POST …/credentials/{profile}/connect`, `DELETE …` | provider logins |
| `GET /cells/{cell}/blocked`, `POST …/blocked/{id}/dismiss` | agent blocked reports |
| `GET /cells/{cell}/history?limit=&before=` | merged audit |
| `GET /cells/{cell}/orca`, `POST …/orca/pair` | Orca runtime state, new pairing offer |

`GET /cells/{cell}/lan` keeps sandbox-control's `GET /api/lan` field names:
`enabled`, `reason`, `error`, `running`, `podUid`, `windows` (allowed
durations), `hosts[]` (`address`, `all` or `ports`, `grant` = `null` or
`{"mode", "ports", "openedBy", "openedAt", "expires"}`, `learned[]` with
`port`, `count`, `lastSeen`, `learnedGrant`, `suggestedAllowLine`) and
`history[]` (newest first, at most 50; `event` one of `opened`, `extended`,
`restricted`, `expired`, `applied`, `revoked`, `worker-restarted`,
`controller-restart`). The only renames are `workspace` → `cell` in paths and
`pod_uid` → `podUid` (already camelCase in the PR #257 JSON).

The API is described in an OpenAPI document shipped in the chart and checked
in CI against handler tests.

## 8. Agent kit

**What it is.** A versioned bundle of the files that shape how agents behave
in a cell: skills, instruction fragments for `AGENTS.md` / `CLAUDE.md`, agent
definitions, MCP/tool configuration and the `glovecell` cell CLI. Today the
same job is done by a marker-delimited "cluster guide" that a runtime CLI
appends into the agent's home instructions at start; the kit replaces that
with a reviewed, versioned source.

**Source.** A Git repository and ref named by a `Kit` resource (default: a
`kit/` directory in a repository the operator chooses; the chart ships a
small example kit). The controller fetches it with read-only forge
credentials, optionally verifies a commit signature, and records version and
digest in `Kit.status`.

**Layout.**

```
kit/
  kit.yaml                 # version, minimum controller version, targets
  instructions/            # fragments, concatenated in name order
    00-plain-language.md
    10-cell-rules.md       # how to file blocked reports, dev links, publish
  skills/explain/SKILL.md  # first skill
  agents/                  # agent definitions (Claude Code, Codex)
  mcp/servers.json         # tool servers the cell may use (proxy rules apply)
```

**Delivery.** On cell create and on every kit or cell update the controller
renders the kit plus `Cell.spec.kit.additions` into a ConfigMap (or a
read-only volume image when larger than 1 MiB) and mounts it read-only at
`/opt/glovecell/kit`. A start hook in the sandbox image links it into place:

- `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md` get a single include block
  between `<!-- glovecell-kit:begin vX -->` and `<!-- glovecell-kit:end -->`,
  replaced in place on update (same idea as today's marker), with the user's
  own lines outside the block untouched;
- skills and agent definitions are symlinked into the agent's skills and
  agents directories; the target files are read-only;
- MCP config is merged, kit entries win on name conflicts.

Repository-level `AGENTS.md` in the checkout stays the project's own and is
not overwritten. The agent can read the kit but not change it; changes go
through a pull request on the kit repository. Kit updates do not restart the
pod; running agent sessions pick them up on their next start.

**First contents.** The plain-language rule (open documents and PRs with Now,
Change, Why this way, Risk and undo, Words; define every component on first
use) and the `explain` skill.

## 9. Orca runtime per cell

This section adopts a per-project Orca runtime with these choices:

- `spec.runtime` becomes `spec.runtime.orchestrator: none | orca`, immutable.
- The runtime's egress goes through the cell's own proxy and broker
  (section 4.5) rather than a separate provider gateway; the gateway's route
  list becomes the broker's `claude` and `codex` profiles.
- There is no shared runtime at the end of the migration. Cross-repository
  work happens in a cell that enrolls extra checkouts (`spec.repositories`),
  with the forge token scoped to exactly those repositories.
- The kit (section 8) replaces the cluster guide the shared runtime wrote.
- Automations that the shared runtime ran (scheduled issue loops) become
  per-cell Orca automations stored on the cell volume, created from kit
  templates.
- Pairing, intake (`:8791`, controller bearer), gateway listener per cell,
  revocation on Suspend and Delete, and `RepositoryImport` stay as designed.

Orca is optional; a cell with `orchestrator: none` runs agents from a shell or
another orchestrator. Nothing in the controller depends on Orca internals
beyond the documented intake endpoints.

## 10. Pluggable isolation runtime and runtime audit

### 10.1 Interface

The isolation runtime is a Kubernetes `RuntimeClass` plus a Glovecell
**runtime profile** in chart values: name, RuntimeClass, pod overhead,
supported features (`inPlaceResize`, `syscallAudit`, `virtiofs`), and how
audit is attached. Admission rejects a cell asking for a runtime whose profile
lacks a feature the cell's policy needs (for example `syscallAudit` when audit
is required). Unsupported combinations fail closed.

### 10.2 Comparison

| | gVisor (systrap) | gVisor, KVM platform | Kata + Cloud Hypervisor | Firecracker (via Kata) |
| --- | --- | --- | --- | --- |
| Boundary | user-space kernel (Sentry) + seccomp on host | same Sentry; address spaces use hardware virtualization | full guest kernel in a VM | full guest kernel in a minimal VM |
| Needs | runsc | runsc, `/dev/kvm` on nodes (nested virt if nodes are VMs) | KVM, kata-runtime, guest image | KVM, devmapper snapshotter, block-only storage |
| Memory | host pages charged to pod cgroup, freed back as the Sentry releases them | same | guest RAM sized from pod limits; freed guest memory is sticky unless balloon / free-page reporting is on | sized at boot; balloon only, no hotplug |
| VM sizing | n/a | n/a | default VM size + container limits; hotplug CPU/memory | fixed at boot from limits |
| Per-pod overhead | small (tens of MiB) | similar | larger (guest kernel + agent + VMM, order 100+ MiB); must be set as RuntimeClass `overhead` | smaller VMM, still a guest kernel |
| In-place resize | cgroup change works; guest view (`/proc/meminfo`) does not update | same | partial, via hotplug; to qualify | no |
| Volumes | gofer / directfs | same | virtio-fs | block devices only; PVCs awkward |
| Syscall audit | seccheck trace points via `--pod-init-config` to a receiver | **same, unchanged** | host sees only VMM calls; needs an in-guest agent (eBPF/auditd) | same as Kata |
| Compatibility | some syscalls/ioctls missing | same as gVisor | near-native Linux | near-native, fewer devices |

**Recommendation: gVisor-KVM is the first MicroVM-class candidate.** It keeps
the same Sentry, the same seccheck audit path, the same RuntimeClass and
volume mechanics, so the audit pipeline and probes do not change; it adds
hardware-assisted address-space isolation and usually faster syscalls than
systrap. It is not a full guest-kernel boundary: a Sentry escape still lands
in a host process (sandboxed by seccomp and namespaces). Kata + Cloud
Hypervisor is the second candidate when a guest-kernel boundary is required;
it needs an in-guest audit agent and qualified memory reclaim (free-page
reporting) before cells use it. Firecracker is not recommended for cells
because of block-only storage and no resize.

### 10.3 Runtime audit and probes

Today's gVisor audit uses a host wrapper around `runsc`. On `create` it injects
`--pod-init-config` so seccheck streams to a per-sandbox receiver; on every
`start` and `exec` it checks that the receiver service is active, connected,
and has never reported dropped, invalid, unmapped, rejected or error events,
and refuses the operation otherwise. That is fail-closed, which is right for
agent work, but two details turn a short receiver hiccup into a pod restart
loop:

1. **Exec probes go through the wrapper.** A readiness or liveness probe that
   runs `exec` is refused while the receiver is unhappy; the kubelet marks the
   pod unready or kills it; the restart creates a new sandbox whose audit is
   not ready yet; repeat.
2. **Any historical loss blocks forever.** The check rejects when a loss
   counter is greater than zero, so one dropped event early in a pod's life
   blocks every later exec until the pod is replaced.

Glovecell rules:

- **No exec probes on cell pods.** Use HTTP or TCP probes against a small
  status listener in the sandbox image, or none (Agent Sandbox readiness).
  Probes then never touch the audit gate.
- **React to new loss, not history.** The gate compares loss counters with
  the values recorded at the last accepted operation and refuses only when
  they increased (a delta). Every increase is itself an audit event and a
  condition `AuditDegraded` on the Cell.
- **Keep agent work fail-closed.** `create`/`start` of the agent container
  and `exec` of agent commands still require a connected receiver and no new
  loss. On new loss the controller suspends agent sessions (pauses intake,
  refuses `exec`) and the console shows why; it does not delete the pod.
- **Bounded retry.** The receiver reconnect is retried with backoff before
  the gate refuses; the controller, not the kubelet, decides restarts.

For MicroVM runtimes the same contract (receiver connected, no new loss)
applies to the in-guest agent.

## 11. Telemetry (pluggable, off by default)

- `telemetry.export: none` (default): metrics endpoint for Prometheus scraping
  and the built-in audit store only. Nothing leaves the cluster.
- `telemetry.export: otlp`: controller and proxy send traces, metrics and
  audit-record copies to an operator-set OTLP endpoint, bounded buffers and
  timeouts so export never blocks revocation or cleanup. Exported telemetry is
  not the authoritative audit ledger.
- **Agent native telemetry**: agents that emit OTLP (Claude Code, Codex) reach
  the endpoint only if the cell's policy names it; it is a proxy rule like any
  other.
- **App-telemetry module (optional).** The OpenTelemetry Injector uses
  `LD_PRELOAD` to load language auto-instrumentation into processes the agent
  starts (Node, Python, Java, .NET). It works inside gVisor, where eBPF-based
  auto-instrumentation (OBI) cannot attach because the Sentry has no host
  kernel probes. It is off by default (`Cell.spec.telemetry.appTelemetry`),
  configured from a read-only file in the kit mount, scoped to processes
  started under the cell's app launcher (not the agent CLIs), and exports only
  to the cell's allowed OTLP endpoint. It never captures prompts, request
  bodies or headers by default.

## 12. Forge and credentials (pluggable)

### 12.1 Forge interface

A forge provider implements: list repositories, create a per-cell bot identity
or token scoped to the cell's repositories, protect the default branch,
enable/disable CI for imported forks, mirror import, revoke. Forgejo is the
qualified provider; GitHub exists as an unqualified host provider; GitLab is
future work. Agents push branches and open pull requests; they never merge.

### 12.2 Credentials backend interface

Operations: `Put`, `Get`, `Rotate`, `Revoke`, scoped by cell. OpenBao (or
Vault) is the default backend: one policy per cell allowing only
`glovecell/cells/<cell>/*`, authenticated by the proxy's projected service
account token through Kubernetes auth. A Kubernetes-Secret backend exists for
development and is documented as weaker.

### 12.3 Provider logins per cell

The broker needs per-cell credentials for model providers. Options:

| Option | How | Trade-off |
| --- | --- | --- |
| A. per-cell login | operator runs a provider login flow per cell from the Credentials section; refresh token stored under the cell's path | strongest separation; more logins; provider session limits may apply |
| B. central refresh owner | one trusted refresher holds the refresh token and writes short-lived access tokens into each enrolled cell's path | one login; a compromised refresher affects all cells |
| C. API keys | per-cell API key with spend limits | simplest; billed separately from subscriptions |

Recommendation: support A and B behind the same broker profile, default B for
subscriptions (Claude, Codex) with access tokens only in the cell path and
refresh material rejected at the proxy, A for higher-risk cells. Which source
Codex uses is open (question 1).

## 13. Threat model summary

Assets: operator credentials, forge write access, model subscriptions, LAN
devices, cluster control plane, other cells' code.

| Threat | Control |
| --- | --- |
| Agent exfiltrates code or secrets to an arbitrary host | Locked window by default; exact approvals; audit of each request |
| Agent reads real credentials | placeholders only; broker injects on reviewed routes; refresh material stripped |
| Prompt-injected agent widens its own access | agents cannot call the console API; blocked reports are proposals; admission limits writers |
| Bypass the proxy (raw sockets, DNS tunnelling) | Cilium L3/L4 default deny; no DNS except the proxy name; proxy refuses raw TCP |
| Cell-to-cell movement | per-cell namespace, proxy, CA, forge token, credentials path; no cross-namespace traffic |
| Reach LAN or cluster | proxy refuses private space; LAN only by timed, pod-bound grants within two allowlists; denylist required |
| Escape the sandbox | gVisor (or MicroVM); no service account token; syscall audit, fail closed |
| Compromised proxy | no Kubernetes credentials; holds only its cell's key and handles; Cilium restricts it to public 80/443 and forge |
| Compromised controller | trusted component; minimal RBAC, no cluster-admin; audit outside its namespace when exported |
| Malicious kit change | kit from reviewed Git ref, optional signature check, digest in status, read-only mount |
| Supply chain via packages | name@version approvals, lockfile review, dev-link scans |
| Audit gap | outbox full stops forwarding; new runtime-audit loss pauses agent work |

## 14. Packaging and rename

### 14.1 Helm chart

One chart, `glovecell`, installs: CRDs (`crds/`, plus an opt-in upgrade Job
because Helm does not upgrade `crds/`), controller + console Deployment,
admission webhook, RBAC, proxy image reference, default runtime profile
(`gvisor`), network provider (`cilium`, or the experimental
`network-policy`), optional HTTPRoute/Gateway listener, optional
ServiceMonitor. It does not install Agent Sandbox, Cilium, gVisor, the forge,
OpenBao or an identity provider; `NOTES.txt` and a preflight Job check that
they exist and fail with a clear message.

Defaults: telemetry off; windows `Locked`; LAN grants off (empty allowlist);
dev links off until a scanner is configured; app-telemetry off; Orca off;
images by digest; no hostnames, addresses or credentials in values. A
`values.schema.json` rejects unknown keys. Upgrades are ordered: CRDs →
controller → proxies (rolled per cell by the controller, one at a time, each
waiting for `ProxyReady`).

### 14.2 Rename plan

Rename everything before the first public release:

| Item | From | To |
| --- | --- | --- |
| repository | agent-project-controller | glovecell (forge redirect kept) |
| Go module | `…/agent-project-controller` | `…/glovecell` (vanity path optional) |
| binaries / images | `agent-project-controller`, proxy | `glovecell-controller`, `glovecell-proxy` |
| chart | `agent-project-controller` | `glovecell` |
| CRD group | `projects.agentwork.dev` | `glovecell.dev` |
| kind | `ProjectWorkspace` | `Cell` |
| labels / finalizer | `projects.agentwork.dev/*` | `glovecell.dev/*` |
| namespaces | `apc-<uid>` | `gc-<uid>` for new cells |

Kubernetes cannot convert objects between API groups (conversion webhooks work
only between versions of one group), so existing objects migrate by
adoption, not conversion:

1. Release N installs both CRDs. The controller reads `ProjectWorkspace` and,
   for each, creates a `Cell` with the same spec and annotation
   `glovecell.dev/migrated-from: <uid>`.
2. The controller adopts owned objects in place: adds `glovecell.dev/*`
   labels and finalizer next to the old ones, writes new network policies
   before deleting old ones (no gap), and keeps the existing namespace name,
   PVC and Sandbox. No pod restart is required except where a policy label is
   part of a pod selector; those pods restart once, in a maintenance window.
3. When `Cell` reports `Ready` with `MigratedFrom` condition true, the old
   finalizer is removed and the `ProjectWorkspace` deleted **without**
   cleanup (an annotation tells the old reconciler path to skip revocation).
4. Release N+1 drops the old CRD and old labels. Rollback before N+1:
   reinstall the previous chart; the old objects still exist until step 3.

Exit check: every `ProjectWorkspace` has a `Ready` `Cell`, project volumes
have the same UID as before, a denial test and a revoke test pass on a
migrated cell.

## 15. Phased roadmap

| Phase | Outcome | Exit check |
| --- | --- | --- |
| 0 | This design and decisions merged | maintainer approval |
| 1 | Rename and CR migration (section 14.2) | all cells `Ready` under `glovecell.dev`; old CRD unused |
| 2 | Runtime audit gate: non-exec probes, delta loss, pause instead of restart loop | induced receiver restart causes `AuditDegraded` and paused agents, no pod restart |
| 3 | Proxy parity: windows, exact approvals, package rules, broker (forge, Claude, Codex), blocked reports, per-request audit outbox | denial and recovery tests per feature; learning → lock → approve cycle on a live cell |
| 4 | LAN model: `LANGrant`, cell allowlist, learned ports, restrict now | grant opens only listed ports; expiry and restrict-now remove access; denylist refusal test |
| 5 | Console Option B and API v1 with OpenAPI | every sidebar section works against API only; CSRF/origin negative tests |
| 6 | Agent kit: `Kit`, renderer, read-only mount, first kit | kit update changes instructions without pod restart; agent cannot write the mount |
| 7 | Per-cell Orca runtime (section 9) and automations per cell | pair, work, suspend revokes pairing, resume |
| 8 | Runtime profiles; qualify gVisor-KVM | same qualification suite passes on gVisor-KVM; audit unchanged |
| 9 | Open-source release: chart defaults, docs, example kit, no-telemetry install | clean install on a disposable cluster with defaults; secret/name scan of repo clean |

Phases 2 and 3 can run in parallel with 1; 4–6 need 3; 7 needs 3 and 6; 9
needs all except 8.

## 16. Open questions

> **Answered in [D13](decisions.md#d13--answers-to-the-open-questions-2026-10-30).**
> Still open: the API group domain (5).

1. **Codex access source.** Today's Codex access depends on a shared AppRole
   and a refresh owner that are disabled. Per-cell login (option A), a central
   refresher (option B) or API keys (option C)? Does Codex work with a
   placeholder token and broker injection at all?
2. **Approvals storage at scale**: CRs up to the cap, or controller database
   from the start?
3. **WebSocket through the proxy**: needed for some tools and Orca clients;
   allow per exact host with frame-level audit, or keep refusing?
4. **Kit source and signing**: which repository holds the default kit, and is
   a signed-commit check required?
5. **Name and CRD domain**: is `glovecell.dev` registered by the maintainer?
   An API group should be a domain the project controls.
6. **Migrated namespace names**: keep `apc-<uid>` for existing cells, or
   recreate them as `gc-<uid>` (needs volume copy)?
7. **gVisor-KVM hardware**: do the nodes expose `/dev/kvm` (nested
   virtualization if nodes are VMs)?
8. **Dev-link scanner**: require a scanner (Trivy server) for dev links, or
   allow unscanned previews with a warning?
9. **Controller HA**: is one replica with fail-closed proxies enough, or do
   windows and approvals need a second replica?
