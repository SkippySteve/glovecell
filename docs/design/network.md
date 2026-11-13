# Network: how traffic enters and leaves a cell

How every kind of transfer reaches a Glovecell sandbox and leaves it. That
covers agent sessions, files, repositories, web, model providers, MCP, A2A,
ACP, previews, LAN devices and telemetry. Decision
[D14](decisions.md#d14--one-gateway-per-cell-2026-10-30) makes the cell
gateway the only way in and out. An interactive version of this page is in
[network.html](network.html): open it in a browser and pick a channel.

Marks used below: **(design)** is already in [glovecell.md](glovecell.md),
**(D12/D13)** comes from those decisions, and **(proposed)** is new here and
not built yet.

## Now / Change / Why this way / Risk and undo / Words

- **Now**:
  - Traffic leaves a cell through its egress proxy (design, section 4).
  - Some inbound paths are described separately: previews and dev links
    through the gateway route (4.7), the Orca pairing listener (9), and file
    uploads through the console.
  - MCP, A2A and ACP are not described at all.
- **Change**: one **cell gateway** per cell handles both directions:
  - an **egress side**, which is today's inspecting proxy;
  - an **ingress side**, a reverse-proxy listener in the same pod.

  Every transfer type is a named **channel**, with one route, one
  authentication rule, one policy check and one audit record.
- **Why this way**:
  - One place per cell to enforce, audit and explain traffic.
  - The sandbox pod accepts connections only from its own gateway, and can
    reach only its own gateway (plus timed LAN grants).
  - Adding a protocol means adding a channel, not a new hole.
- **Risk and undo**:
  - The gateway becomes a single point of failure for the cell. It fails
    closed, so a broken gateway stops traffic and never opens it.
  - Ingress is additive: until it is built, today's separate inbound paths
    keep working.
- **Words**:
  - **Edge**: the cluster's shared entry point (Gateway API or ingress
    controller) where people and outside agents sign in.
  - **Channel**: one kind of transfer (for example "MCP, remote").
  - **Placeholder**: a fake credential the agent holds; the gateway swaps in
    the real one on reviewed routes.
  - **Cell CA**: the per-cell certificate authority the gateway uses to
    inspect HTTPS.

## 1. Overview

```mermaid
flowchart LR
  subgraph People["People and outside agents"]
    OP["Operator<br/>browser or phone"]
    UI["Agent UIs<br/>Orca, T3, ACP editors"]
    EXT["Outside A2A agents"]
  end

  EDGE["Edge<br/>TLS + OIDC sign-in<br/>Gateway API routes"]

  subgraph System["glovecell-system (trusted)"]
    CTRL["Controller + console API"]
    PG[("PostgreSQL<br/>approvals, requests,<br/>history")]
    A2AI["A2A intake (proposed)"]
    SCAN["Scanner jobs"]
    CRED["Credentials backend<br/>OpenBao"]
  end

  subgraph Cell["Cell namespace gc-&lt;uid&gt;"]
    direction LR
    subgraph GW["Cell gateway"]
      IN["Ingress listener (proposed)"]
      OUT["Egress proxy<br/>windows, approvals,<br/>broker, cell CA"]
      LOC["Local endpoints<br/>blocked, dev-link,<br/>cell MCP (proposed)"]
    end
    SB["Sandbox pod (gVisor)<br/>agents, orchestrator,<br/>/workspace, kit"]
  end

  subgraph Outside["Destinations"]
    MODEL["Model providers"]
    FORGE["Forge (Git, API)"]
    WEB["Web and package registries"]
    RMCP["Remote MCP servers"]
    RA2A["Remote A2A agents"]
    OTEL["OTLP collector (optional)"]
  end
  LAN["One LAN host<br/>(timed grant)"]

  OP --> EDGE
  UI --> EDGE
  EXT --> EDGE
  EDGE -->|console, files, previews| CTRL
  EDGE -->|sessions, previews| IN
  EDGE -->|tasks| A2AI
  A2AI --> PG
  A2AI -.->|approved task| CTRL
  CTRL -->|files, tasks, policy| IN
  CTRL <-->|mTLS: policy, state, audit| OUT
  CTRL --> PG
  CTRL --> SCAN
  IN -->|approved ports only| SB
  SB -->|all HTTP and HTTPS| OUT
  SB --> LOC
  LOC --> CTRL
  OUT -->|token injected| MODEL
  OUT -->|scoped token| FORGE
  OUT --> WEB
  OUT --> RMCP
  OUT --> RA2A
  OUT --> OTEL
  OUT -. reads credentials .-> CRED
  SB -. "Cilium L3/L4,<br/>exact host:port" .-> LAN
```

The sandbox pod has exactly three network neighbours:
- its own gateway, in both directions;
- DNS, which only answers for the gateway;
- a LAN host, while a grant is open.

The gateway holds no Kubernetes credentials. It talks to the controller over
mTLS.

## 2. Enforcement layers

```mermaid
flowchart TB
  L1["1. Edge: TLS, OIDC sign-in, per-route authorisation<br/>(who may open which channel of which cell)"]
  L2["2. Network policy, Cilium L3/L4<br/>sandbox ↔ gateway only; LAN grant pinned to one pod and host:port"]
  L3["3. Cell gateway, L7<br/>ingress: approved channels and ports;<br/>egress: decrypt, match window/approval/rule, inject credentials"]
  L4["4. Admission and RBAC<br/>grants expire, only the controller writes them;<br/>approvers checked on every decision"]
  L5["5. Isolation runtime<br/>gVisor or MicroVM: no host kernel, no node network"]
  L6["6. Audit<br/>one record per connection or request:<br/>Postgres, Events, optional OTel"]
  L1 --> L2 --> L3 --> L4 --> L5 --> L6
```

A request has to pass every layer that applies to it. Each layer fails
closed on its own.

## 3. Channels

| Channel | Direction | Path | Protocol | Who authenticates | Locked | Ask | Standard | Open (timed) | Audit |
|---|---|---|---|---|---|---|---|---|---|
| Agent session (Orca, T3) | in | edge → gateway ingress → orchestrator port | HTTPS, WebSocket | operator OIDC plus pairing | ✓ | ✓ | ✓ | ✓ | open/close, bytes |
| Agent session (ACP) **(proposed)** | in | edge → ingress → ACP bridge → agent stdio | JSON-RPC over WebSocket | operator OIDC | ✓ | ✓ | ✓ | ✓ | session, prompts by count |
| Model providers | out | sandbox → egress → provider | HTTPS, SSE, WebSocket (D13) | broker injects token | ✓ | ✓ | ✓ | ✓ | per request |
| Repository (own) | out | sandbox → egress → forge | Git smart HTTP, API | scoped forge token | ✓ | ✓ | ✓ | ✓ | per request |
| Web | out | sandbox → egress → host | HTTPS | none, or a named profile | rules and approvals only | new requests wait for approval | plus common hosts | all public hosts | per request |
| Packages | out | sandbox → egress → registry | HTTPS | registry profile | approved versions | ask per version | plus registries | all | per request |
| Files in | in | operator → console → controller → ingress → `/workspace/inbox` | HTTPS stream | operator OIDC | ✓ operator action | ✓ | ✓ | ✓ | name, size, hash |
| Files out | out | sandbox outbox → scan → console download | HTTPS stream | operator OIDC | ✓ after scan | ✓ | ✓ | ✓ | name, size, hash, findings |
| Previews and dev links | in | edge → ingress → app port | HTTPS, WebSocket | operator OIDC plus verification card | ✓ after scan and approval | ✓ | ✓ | ✓ | per request |
| MCP, local (stdio) | none | inside the sandbox | stdio | — | ✓ | ✓ | ✓ | ✓ | the agent's own telemetry |
| MCP, cell tools **(proposed)** | local | sandbox → gateway local endpoint | MCP over HTTP | per-pod token | ✓ | ✓ | ✓ | ✓ | per call |
| MCP, remote | out | sandbox → egress → MCP server | streamable HTTP, SSE | broker injects the server's token | approved hosts | ask | named servers | all public | per request |
| A2A, inbound task **(proposed)** | in | outside agent → edge → A2A intake → approval → cell | JSON-RPC over HTTPS, SSE | OAuth2 client or mTLS | queued for approval | queued | queued | queued | task, decision |
| A2A, outbound | out | sandbox → egress → remote agent | JSON-RPC over HTTPS, SSE | named profile | approved hosts | ask | named agents | all public | per request |
| LAN device | out | sandbox → host:port (not via the gateway) | TCP | — | timed grant only | timed grant | timed grant | timed grant | flow logs, grant history |
| Telemetry | out | sandbox → egress telemetry route → collector | OTLP/HTTP | collector token | ✓ | ✓ | ✓ | ✓ | counts only |
| Vendor telemetry | out | — | — | — | refused | refused | refused | refused | denial |

Notes:
- **Inbound is the same at every level.** The level controls what the cell may
  reach, not who may reach the cell. Inbound always needs the operator (or an
  approved task).
- **Open is time-limited** (at most 8h; D12 grant rules). Private, cluster and
  metadata addresses are refused at every level; LAN goes through grants only.
- **"Ask"** holds a new request as pending in PostgreSQL (D13). The operator
  approves it once (exact match) or promotes it to a rule.

## 4. Egress decision

```mermaid
flowchart TD
  R["Request from the sandbox<br/>(CONNECT host:443, http://host:80)"] --> P{"Parses cleanly?<br/>no escaped paths,<br/>size limits"}
  P -- no --> D1["DENY 400"]
  P -- yes --> N{"Resolves only to<br/>public addresses?"}
  N -- no --> D2["DENY: private, cluster,<br/>metadata"]
  N -- yes --> V{"Vendor telemetry<br/>host?"}
  V -- yes --> D3["DENY"]
  V -- no --> C{"Credential route?<br/>provider, forge,<br/>MCP or A2A profile"}
  C -- yes --> A1["ALLOW and inject<br/>the real credential"]
  C -- no --> F{"Fixed rule or<br/>package rule?"}
  F -- yes --> A2["ALLOW"]
  F -- no --> X{"Exact approval?<br/>(PostgreSQL, cached)"}
  X -- yes --> A3["ALLOW"]
  X -- no --> L{"Access level"}
  L -- "Open, within grant" --> A4["ALLOW, record"]
  L -- "Learning window" --> A5["ALLOW, save as approval"]
  L -- "Locked / Ask" --> Q["DENY, create a pending<br/>request for one-click approval"]
  A1 & A2 & A3 & A4 & A5 --> U{"WebSocket upgrade?"}
  U -- "yes, host allowed (D13)" --> WS["Frames: audited,<br/>closed on expiry or<br/>level change"]
  U -- no --> OK["Forward; one audit record"]
```

Every branch writes one audit record. If the audit outbox is full,
everything except credential routes is denied (design 4.1).

## 5. Sequences

### 5.1 Agent session (Orca or T3; ACP the same way)

```mermaid
sequenceDiagram
  autonumber
  actor O as Operator (Orca app)
  participant E as Edge (OIDC)
  participant I as Gateway ingress
  participant S as Sandbox (orchestrator)
  participant G as Gateway egress
  participant M as Model provider
  O->>E: open session for cell X
  E->>E: sign-in, may this user use cell X?
  E->>I: mTLS, route to cell X "session" channel
  I->>I: channel enabled? orchestrator class matches?
  I->>S: WebSocket to orchestrator port
  S-->>O: conversation stream (through I and E)
  S->>G: model call with placeholder token
  G->>G: provider route, swap in the real token
  G->>M: HTTPS
  M-->>S: streamed reply
  Note over I,G: both sides write audit records - the conversation content<br/>is the agent's own telemetry, not the gateway's
```

### 5.2 Web request that needs approval

```mermaid
sequenceDiagram
  autonumber
  participant S as Sandbox agent
  participant G as Gateway egress
  participant C as Controller
  participant DB as PostgreSQL
  actor O as Operator
  S->>G: GET https://example.org/data.json
  G->>G: no rule or approval - level is Ask
  G->>C: pending request (mTLS)
  C->>DB: insert access request
  G-->>S: 403 with a request id
  C-->>O: "Needs attention" inbox
  O->>C: approve (OIDC approver group)
  C->>DB: approval and decision record
  C->>G: push the new approval
  S->>G: retry
  G->>S: ALLOW (exact approval)
```

### 5.3 Repositories

```mermaid
sequenceDiagram
  participant S as Sandbox (git)
  participant G as Gateway egress
  participant F as Forge
  S->>G: git fetch/push with placeholder token
  G->>G: forge route: only this cell's repositories
  G->>F: Git smart HTTP with a short-lived scoped token
  F-->>S: refs and packs
  Note over F: branch protection: agents push branches and open PRs,<br/>never merge (design 12.1)
```

### 5.4 Files in and out

```mermaid
sequenceDiagram
  actor O as Operator
  participant C as Controller / console
  participant I as Gateway ingress
  participant S as Sandbox
  participant K as Scanner job
  O->>C: upload file (OIDC)
  C->>C: size cap, hash, history record
  C->>I: stream to cell X files channel
  I->>S: write /workspace/inbox/<name> (read-only to the agent)
  S->>S: agent writes /workspace/outbox/<name>
  C->>K: scan outbox item (read-only mount)
  K-->>C: findings
  O->>C: download (warning shown if findings)
  C->>I: read outbox item
  I-->>O: file stream (through the console)
```

### 5.5 MCP

```mermaid
flowchart LR
  subgraph S["Sandbox"]
    A["Agent"]
    L["Local MCP server<br/>(stdio, in the sandbox)"]
  end
  subgraph GW["Cell gateway"]
    T["Cell tools MCP (proposed)<br/>blocked report, dev link,<br/>files, request access"]
    E["Egress proxy"]
  end
  R["Remote MCP server<br/>(streamable HTTP)"]
  A -- stdio --> L
  A -- "HTTP, per-pod token" --> T
  T -- mTLS --> CT["Controller"]
  A -- "HTTPS, placeholder" --> E
  E -- "token injected,<br/>approved host only" --> R
```

- MCP server configuration ships in the agent kit (D6), so every orchestrator
  gets the same tools.
- Cell tools replace orca-pilot's CLI commands, so any UI can use them.
- A local server's own network calls go through the egress proxy like any
  other request.

### 5.6 A2A

```mermaid
sequenceDiagram
  autonumber
  participant X as Outside agent
  participant E as Edge
  participant N as A2A intake (proposed)
  participant DB as PostgreSQL
  actor O as Operator
  participant C as Controller
  participant S as Sandbox
  participant G as Gateway egress
  participant R as Remote agent
  X->>E: tasks/send (OAuth2 client or mTLS)
  E->>N: authenticated caller
  N->>DB: task stored as a pending request (untrusted text)
  N-->>X: task id, state "submitted"
  O->>C: approve task for cell X
  C->>S: deliver task through the orchestrator adapter
  S-->>N: progress and result (through the controller)
  N-->>X: status and artefacts (SSE)
  S->>G: outbound A2A call (named agent profile)
  G->>R: HTTPS, credential injected
```

Inbound tasks never connect straight to a cell. They become pending requests
that the operator approves, like web requests. Their text is treated as
untrusted input that may carry prompt injection.

### 5.7 Previews and dev links

```mermaid
sequenceDiagram
  participant S as Sandbox app (port P)
  participant C as Controller
  participant K as Scanner
  actor O as Operator
  participant E as Edge
  participant I as Gateway ingress
  S->>C: glovecell dev-link publish --port P (cell tools)
  C->>K: scan the app directory (SBOM, CVEs, secrets)
  K-->>C: findings
  C-->>O: approval card with warnings (D13: required scan)
  O->>C: approve
  C->>I: enable route cell X:P
  O->>E: open https://<name>.<dev domain>
  E->>I: OIDC, verification card
  I->>S: HTTP or WebSocket to port P
```

### 5.8 LAN device

```mermaid
sequenceDiagram
  actor O as Operator
  participant C as Controller
  participant K as Cilium
  participant S as Sandbox pod
  participant H as LAN host
  O->>C: open grant host:port for 1h
  C->>C: admission: within the allowlist and the cell list, ≤ 24h
  C->>K: policy bound to this pod UID
  S->>H: TCP (not through the gateway)
  Note over C,K: on expiry the policy narrows first, then the grant is<br/>deleted - restrict now restarts the pod
```

## 6. What the gateway refuses

- **Raw TCP or UDP to the internet.** Use HTTPS, or a LAN grant for local
  devices.
- **Certificate-pinned clients:** they fail against the cell CA, and there is
  no blind passthrough (D13).
- **Private, cluster, node and metadata addresses** at every level.
- **Inbound connections that don't come through the edge,** and ports that
  are not approved.
- **Credentials sent inside WebSocket frames:** only header credentials can be
  brokered (D13).
- **Vendor telemetry** from UIs and agents.

## 7. Building order (proposed)

1. **Ingress listener in the gateway pod.** Move the preview, dev-link and
   session routes onto it, and tighten the sandbox network policy to "from
   the gateway only".
2. **Cell tools MCP endpoint**, with orca-pilot's commands moved onto it.
3. **WebSocket support in the egress proxy** (D13 item 3).
4. **Files channel** through the ingress.
5. **ACP bridge** and the `acp` orchestrator class.
6. **A2A intake.**
