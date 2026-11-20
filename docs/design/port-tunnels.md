# Design: previews and port forwards for every client

**Status:** proposed design, 2026-11-20. Docs only; nothing here is built yet,
except the T3 guard of build step 1 (#180).
Decision record: [D16](decisions.md#d16--previews-and-port-forwards-through-the-cell-gateway-2026-11-20).

**Related:** [glovecell.md 4.7](glovecell.md#47-previews-and-dev-links)
(previews and dev links), [network.md 5.7](network.md#57-previews-and-dev-links)
and [section 7](network.md#7-building-order-proposed) (building order),
[decisions D13 and D14](decisions.md), [in-cluster.md](../in-cluster.md),
[clients-choice.md](../clients-choice.md).

## Now / Change / Why this way / Risk and undo / Words

- **Now:** an agent in a cell often starts a web app (a dev server on port
  5173) or a database (Postgres on 5432). The operator cannot open either from
  their own machine. The sandbox pod accepts no inbound connections, and no
  Glovecell feature publishes a cell's port. Each client app has its own idea
  of how to do this: T3 Code 0.1.11 added a tunnel that reaches every loopback
  port of its host, while Orca, Herdr and Agent Orchestrator have nothing.
  Whatever a client does on its own bypasses Glovecell's approvals and audit.
- **Change:** two Glovecell features that every client can use, built in this
  order:
  1. **Preview URLs.** The agent (or the operator) asks to show port 5173 of
     cell `my-repo`. The operator approves a card in the console that says what
     Glovecell observed, what the agent claims, who asked, the address and when
     it ends. The page then opens at a private address such as
     `https://5173--my-repo.example-preview.net/`, behind the console's
     sign-in, in any browser: desktop, phone, or a client's built-in browser.
  2. **A foreground forward command.** `glovecell forward my-repo 5432` first
     opens `127.0.0.1:5432` on the operator's machine, then asks for the same
     kind of approval, then carries each connection to the cell, so `psql`
     works locally. It stops when the command stops or the approval expires,
     whichever comes first.

  Both go through the cell gateway, both expire (one hour by default, eight at
  most), both are listed in the console with a revoke button, and every
  request or connection is recorded.
- **Why this way:** one audited path for all clients instead of one per
  client, and it is the path the design already reserved (ingress listener in
  the cell gateway, D14). The main alternative, adopting an identity-aware
  proxy such as Pomerium, is a good project but would still need all of
  Glovecell's approval, scan and per-cell routing glue, plus a new required
  dependency in every install (section 6).
- **Risk and undo:**
  - A preview runs agent-written code in the operator's browser. Mitigations:
    a preview domain that is a different registrable domain from the console,
    one origin per preview, a gateway-set Content Security Policy that blocks
    the page from sending data to other sites, a verification card, the
    required scan (D13 item 8), and short expiries. Top-level navigation away
    from the page remains a possible leak (section 4.3).
  - A forward puts a cell's port on the operator's loopback, where every
    program running as the operator can reach it while it runs. Mitigations:
    loopback-only bind (or a private Unix socket), a same-user check on Linux,
    refused control ports, HTTP refused unless the port has an approved preview,
    short expiry, and a plain warning on the card.
  - The new public listener runs in its own container in the gateway pod,
    without the cell CA key or any credential, so a bug in it does not expose
    the egress side's secrets.
  - Both features are off by default (chart values). Turning them off removes
    the routes and closes open connections; reverting the code removes the
    features. Nothing is installed on an operator machine.
- **Words:**
  - **Cell gateway**: the per-cell pod that today is the egress proxy (every
    outbound request passes it). D14 adds an **ingress listener** for traffic
    coming in; this design puts it in a second container of that pod.
  - **Edge**: the cluster's shared entry point (a Gateway API implementation
    such as Traefik or Envoy Gateway) that terminates TLS for public names.
  - **HTTPRoute**: the Gateway API object that sends one host name or path
    from the edge to a backend Service.
  - **OIDC**: OpenID Connect, the sign-in protocol the console already uses
    with the deployment's identity provider.
  - **PKCE**: Proof Key for Code Exchange, the OAuth step that lets a command-
    line program finish a browser sign-in without a stored secret.
  - **WebSocket**: a long-lived two-way connection that starts as an HTTP
    request; ingress controllers route it like normal HTTPS.
  - **HTTP CONNECT**: an HTTP method that asks a proxy to open a raw TCP
    connection; many ingress controllers do not route it.
  - **CSP**: Content Security Policy, a response header that tells the browser
    which sites a page may load from or send to.
  - **Registrable domain**: the part of a host name a person can register
    (`example.com` in `a.b.example.com`); browsers treat hosts that share it
    as the same site.
  - **Loopback**: `127.0.0.1` / `::1`, addresses reachable only from the same
    machine (or, inside a pod, the same pod).
  - **Preview**: a private URL to one running process in a cell, valid while
    that process runs and the approval lasts.
  - **Dev link**: a stable private URL for one scanned build of an app; the
    path for longer-lived needs (glovecell.md 4.7).
  - **Forward**: a TCP connection relay from the operator's machine to one
    port in a cell, for as long as the forward command runs and its approval
    lasts.
  - **Ticket**: a short-lived, single-purpose token the console signs after
    sign-in and approval (cell, port, operator, expiry); the gateway checks it.
  - **T3 Code, Orca, Herdr, Agent Orchestrator (AO)**: the client apps an
    operator uses to drive agents in cells ([clients-choice.md](../clients-choice.md)).
  - **wstunnel, chisel, Pomerium, oauth2-proxy**: open-source tunnel and
    identity-aware proxy projects, compared in section 6.

The command is written `glovecell forward` here. Until the rename (D2) the
binary is `agent-project-controller`, so today it would be
`agent-project-controller forward`.

## 1. The maintainer's rule for this feature

Decided 2026-11-20, in the maintainer's words: "Generally I don't want things
forwarded long term, but for testing or previews that's fine as long as it's
made clear to me when asking for approval."

What the design takes from it:

- **Short-lived previews and forwards are allowed, each behind an explicit
  approval.** Every approval card (in the console, or the console page the CLI
  prints) has two clearly separated parts.
  - **Observed by Glovecell** (facts the agent cannot forge):
    - the cell, the port and the pod;
    - that the port accepts connections, as the gateway checked it from
      outside the sandbox;
    - the protocol the gateway saw on the first connection (HTTP, TLS or
      other), when there has been one;
    - who asked: the agent (through its per-pod token) or the signed-in
      operator;
    - for a forward, the client address the console saw for the sign-in and
      for the ticket exchange, with a warning when they differ;
    - where it will be reachable: the preview URL, or `localhost:PORT` (or the
      Unix socket path) on the machine running the command;
    - the expiry, as an absolute time in the operator's time zone plus a
      countdown.
  - **Reported by the agent, unverified** (shown quoted, one line each, cut at
    200 characters, never rendered as HTML):
    - the process name and command line read from the sandbox;
    - the agent's reason;
    - for a forward, the machine name the CLI reports.
- **No long-term forwards.** Previews and forwards have a default expiry of
  1 hour and a hard maximum of 8 hours (the same ceiling as other timed
  grants, D12). They end at expiry even while the command or the page is still
  open. Extending is a new approval with a new card, never automatic. A
  lasting need goes through the dev-link path (scan and approval) instead.
- **Active previews and forwards are visible** in the console (the cell's
  **Previews & dev links** section, the sticky header's grant list and the
  **Needs attention** inbox), each with a one-click **Revoke**. Every request,
  approval, start, stop, expiry and revoke is an audit record.
- **The forward command fits the in-cluster rule** as a one-shot operator CLI
  on these terms: it uses the operator's own identity, it needs the approval
  above, it ends at expiry or when the command ends, and nothing is installed
  or left behind on the machine (section 5.5).

## 2. What exists today, and the gap

| Piece | Today | Gap |
| --- | --- | --- |
| Sandbox pod ingress | Base network policy has empty ingress (`internal/network`). Two orchestrator classes add their own route straight to the worker: Orca's spoke (HTTPRoute `<cell>.<domain>` to port 6768) and AO (HTTPRoute to port 3011), each authenticated by the app's own pairing or device tokens | No route for an app's own port. The existing routes bypass the gateway, so they have no Glovecell per-request audit |
| Cell gateway | One process (`proxy`) holds the cell CA key (`/tls/ca.key`) and the credential files the broker injects. It accepts connections only from its own worker on the egress port, and from the metrics collector on the telemetry relay port | No ingress listener (network.md section 7, step 1, not started) |
| Gateway policy updates | The gateway reads `/policy/policy.json` only at start (`internal/proxy/proxy.go`, `Run`); to apply a change the controller restarts the gateway pod (`internal/controller/reference_rules.go`). Short-lived grants use another path: the controller execs `download-grant` in the gateway pod, which talks to a loopback control listener; grants live in memory and a restart drops them (`internal/proxy/download_grants.go`) | A restart per approval would drop every preview, forward and egress connection, so approvals need the live path (section 3.2) |
| Previews & dev links in the console | `web/src/workspace/sources/previews.ts` reads `GET /api/previews` and `/api/devlinks` from the older sandbox-control backend, mounted under `/sandboxes/` by `internal/web/sandbox_control.go`; decisions post to its `/decision` and `/devlink-decision`. Its URLs are served by that backend's own channels | For Glovecell cells the section shows "Project sandboxes do not publish previews or dev links yet" (`ProjectPreviews`) |
| Listener detection | None for Glovecell cells | The approval card needs observed facts (section 3.4) |
| Agent request command | None in Glovecell cells (no `dev-link publish` subcommand in `docs/components.json`) | Agents cannot ask for a preview |
| Scanner for previews and dev links | Required by D13 item 8; not built for cells | Needed before any preview can be approved |
| `Preview` kind | Named as "later" in [kubernetes-api.md](kubernetes-api.md) | Not defined |
| Operator CLI | `connect`, `exec` and `provider import` run once with the operator's own kubeconfig | No command that reaches a cell port without cluster credentials |

Known item, separate from this design: glovecell.md section 7.2 says the
console session cookie is `SameSite=Strict`; the code sets `Lax` and refuses
state-changing requests whose `Origin` is not the console's
(`internal/web/oidc.go`, `internal/web/server.go`). The preview design does
not depend on either, because previews live on a different registrable domain;
the 7.2 text should be corrected or the cookie tightened in its own change.

How each client reaches a cell today, and what it would use for this feature:

| Client | Reaches a cell through | Previews | TCP forwards |
| --- | --- | --- | --- |
| Browser and phone | console (OIDC) | open the preview URL | not possible (a browser cannot listen on ports) |
| Orca | in-cluster hub to each cell's Orca runtime ([hubspoke.md](../hubspoke.md)) | open the URL in Orca's browser or the system browser | `glovecell forward` in a local terminal |
| Herdr | SSH to the in-cluster glove-hub, which forces `herdr-core connect` and sets `DisableForwarding yes` ([herdr.md](../herdr.md)) | system browser | `glovecell forward`; SSH forwarding stays off, since it would bypass approvals |
| T3 Code | T3 Connect, pinned to v0.1.9 and off by default ([t3-connect.md](../t3-connect.md)) | T3's in-app browser or the system browser | `glovecell forward`; T3's own tunnel turned off in cells (section 7) |
| Agent Orchestrator | the AO daemon in each cell, over its HTTPRoute ([agent-orchestrator.md](../agent-orchestrator.md)) | system browser | `glovecell forward` |

## 3. Shape

```text
operator browser / client app                      operator machine (forward)
        |  https://5173--my-repo.example-preview.net       |  glovecell forward my-repo 5432
        |                                                  |  127.0.0.1:5432 <- psql
        v                                                  v  wss://tunnel.example.net/cells/my-repo/5432
 +---------------- edge (Gateway API, TLS; a LAN or VPN listener only) ---------------+
 |  HTTPRoutes written by the controller, only for approved previews and forwards     |
 +---------------------|---------------------------------------------|---------------+
                       | sign-in redirect                            |
                       v                                             v
           console (controller binary)          cell gateway pod (gc-<uid>)
           OIDC session, approval cards,        +-- ingress container (new) ----------+
           signs tickets                        |  own non-root UID, no /tls, no       |
                 |                              |  credentials; checks ticket,         |
                 | exec: ingress-grant  ------> |  approval, expiry; audit outbox      |
                 |  (Unix socket, this          +--------------------------------------+
                 |   container only)            +-- proxy container (unchanged) -------+
                 |                              |  egress, cell CA, broker credentials |
                 |                              +--------------------------------------+
                                                               |
                                                               | approved ports only
                                                               v
                                         sandbox pod: app on :5173, Postgres on :5432
```

### 3.1 The ingress container

- D14 puts the ingress listener in the gateway pod. It runs as **a separate
  container** of that pod, with its own non-root UID, a read-only root file
  system, no `/tls` mount and no credential files. The egress `proxy`
  container keeps the cell CA key and the broker's credentials, and the
  internet-facing parser never shares a process or a file system with them.
- The edge listener the routes attach to must be reachable **only from the LAN
  or a VPN**, like the AO route (agent-orchestrator.md). The chart requires an
  explicit `sectionName` for it and documents that a public listener is not
  supported.
- **Shared loopback.** Containers in a pod share one network namespace, so the
  ingress container could reach the egress side's loopback control listener
  (download grants). Before the ingress container ships, that listener moves to
  a Unix socket on a volume mounted only in the proxy container (build step 2),
  and the ingress container's own control channel is a Unix socket on a volume
  mounted only in it.

### 3.2 How approvals reach the gateway

Approvals do not travel through `policy.json`, which the gateway reads only at
start. They use the download-grant pattern:

- On approve, the controller runs `agent-project-controller ingress-grant` in
  the ingress container (the controller's existing per-project exec binding).
  It writes the approval (ID, kind, port, pod UID, expiry, hostname, ticket
  verification key ID) to the container's control socket. Revoke is the same
  command with `--revoke ID`.
- Approvals live only in the ingress container's memory. A restart drops them,
  which fails closed. The controller watches the gateway pod; when the ingress
  container becomes ready again it re-sends every live approval from
  PostgreSQL before marking the cell's previews `Serving`.
- **Expiry needs no message:** the ingress container ends approvals on its own
  clock (D13 item 9), even if the controller is down.
- **Revoke latency:** the target is under 5 seconds from the console click to
  closed connections: one exec, and the container closes that approval's
  WebSockets and TCP streams on receipt. If the exec fails, the controller
  retries and the console shows "revoke pending" in red; the route is removed
  from the edge at the same time, so new requests stop even before the
  container hears about it.
- The ticket-verification public key reaches the ingress container the same
  way; it changes only when the console rotates its signing key.

### 3.3 Network policy

- Edge pods → gateway pod, ingress container's port only.
- Ingress container → sandbox pod, approved ports only. The controller adds
  the port to the worker's ingress rule on approval and removes it on expiry
  or revoke, like a LAN grant. (Policy is per pod, not per container, so the
  egress container could reach those ports too; it has no reason to and no
  code that does.)
- Nothing else gains a path into the sandbox.
- **Loopback-bound apps.** The gateway is a different pod, so it reaches the
  app on the pod's address, not `127.0.0.1`. Many dev servers bind loopback by
  default (Vite, Postgres with `listen_addresses=localhost`). The card shows
  "bound to loopback only" when that is the case, and the agent kit tells
  agents to bind `0.0.0.0` for anything they want to show. A relay inside the
  workspace container is a later option, not part of the first build.

### 3.4 Listener detection

A **listener detector** in the controller (build step 4) fills the card:

- every 30 seconds while a cell is Running and previews or forwards are on, it
  reads the sandbox's listening sockets (`/proc/net/tcp` and `tcp6`) and the
  owning processes through the controller's existing per-project exec binding;
- the ingress container then confirms each candidate port from outside the
  sandbox (a TCP connect), and records the protocol it sees on first use.

What the detector reads from inside the sandbox is agent-controlled: a process
can name itself anything. Only the port check and the gateway's protocol
observation go under "Observed by Glovecell"; process names and command lines
go under "Reported by the agent, unverified". Where the exec binding is not
enabled, the card shows the observed part only.

## 4. Feature 1: preview URLs

### 4.1 Flow

1. **Request.** Either the agent runs
   `glovecell preview request --port 5173 --dir web/ --note "signup page"`
   (through the gateway's local endpoint, authenticated by the per-pod token,
   like a blocked report, 4.6), or the operator clicks **Preview** on a
   detected listener in the console.
2. **Scan.** D13 item 8 says there are no unscanned previews. The controller
   starts the dev-link scan Job on the named directory (read-only mount, no
   token, egress only to the scanner). It records two digests: **dependencies**
   (lockfiles, manifests and installed package trees) and **sources**. When
   the scanner is down, the request waits.
3. **Approval card** (approver group, D13 item 2), with the observed and
   reported parts from section 1, the scan findings, and the expiry picker
   (default 1 hour, at most 8 hours).
4. **Route.** On approval the controller records the approval (PostgreSQL,
   D13 item 2), writes a `Preview` object whose `Approved` condition generates
   the HTTPRoute for `<port>--<cell>.<previewDomain>`, opens the worker ingress
   rule for that port, and sends the approval to the ingress container
   (section 3.2).
5. **Sign-in on first visit.** The browser has no cookie for the preview host,
   so the ingress container redirects to the console with the **approval ID**
   only. The console checks the OIDC session (signing in if needed) and that
   the user may view this cell, and shows the **verification card** once per
   host and session ("This page is served by an agent in cell `my-repo`, port
   5173. It is not the console; do not enter passwords."). It then redirects to
   `https://5173--my-repo.example-preview.net/.glovecell/session?ticket=…`. The
   return host comes from the approval record, never from a request
   parameter. The ticket names the host, the approval, the operator and an
   expiry of 60 seconds, and works once.
6. **Session.** The ingress container checks the ticket, sets a host-only
   `__Host-` cookie (`HttpOnly`, `Secure`, `SameSite=Strict`) that ends with
   the approval, and answers with a small same-origin page that navigates to
   the clean URL (the ticket never stays in the address bar, history or a
   `Referer`; that page sends `Referrer-Policy: no-referrer`). The extra hop
   matters: a `Strict` cookie set during a redirect chain that started on
   another site is not sent on the rest of that chain. Opening a preview from
   the console therefore always passes through this short handshake.
7. **Proxying.** The container strips its own cookie and every `/.glovecell/`
   path before the request reaches the app, then proxies HTTP and WebSocket.
8. **End.** The preview ends at the first of: expiry, revoke, the process
   stops listening, the pod restarts (approval is bound to the pod UID), or a
   change after the scan (4.4). The controller removes the route and the
   policy rule, and the ingress container closes open WebSockets.

### 4.2 Approval model and limits

| Question | Answer |
| --- | --- |
| Which ports | 1024–65535 the sandbox listens on, except reserved ports: the gateway's own, the telemetry relay, each orchestrator class's control ports (Orca runtime, AO's loopback API and remote listener, the T3 server) and a chart list `previews.deniedPorts` |
| Who asks | the agent (`glovecell preview request`) or the operator (console) |
| Who approves | a member of the approver group, in the console; agents never approve |
| What an approval binds | cell, port, pod UID, dependency digest and latest source scan, operator who approved |
| Expiry | default 1 hour, maximum 8 hours; extending is a new approval and a new card |
| Longer needs | a dev link: same scan, its own approval and deadline (glovecell.md 4.7) |
| Request limits | one pending request per cell and port; at most 5 pending requests and 10 live previews per cell; after a deny, 10 minutes before the agent may ask for the same port again; at most 2 scan Jobs at once in the cluster (chart values) |
| Traffic limits | per-preview byte caps each way (default 1 GiB per hour), shared per-cell caps with other channels |

### 4.3 Browser safety

- **A separate registrable domain is required.** `previews.domain` must not
  share a registrable domain with the console's origin; the chart and the
  controller refuse such a setting (checked with the Public Suffix List). So a
  preview page is a different site from the console: it cannot ride the
  console's cookies and cannot plant cookies on the console's domain.
- **One origin per preview.** `<port>--<cell>` gives every cell and port its
  own origin, so one preview's scripts cannot read another's pages. Labels
  longer than 63 characters use a short hash of the cell name.
- **Cookies between previews.** Previews still share the preview domain, and
  page script can set `document.cookie` with `Domain=` for it, which the
  gateway cannot rewrite. So:
  - the gateway's own cookie uses the `__Host-` prefix, which a page cannot
    set or overwrite through `Domain=`;
  - the app's `Set-Cookie` headers are rewritten to host-only (`Domain=`
    dropped);
  - the ingress container forwards to the app only cookie names this host's
    responses have set, and drops a name that appears twice;
  - deployments that publish the preview domain may also list it on the
    Public Suffix List, which makes each preview its own site.
- **Origin checks.** The ingress container refuses a WebSocket upgrade, and any
  method other than GET and HEAD, whose `Origin` is not the preview's own host.
- **Gateway-set CSP.** The ingress container replaces the app's
  `Content-Security-Policy` header with its own:
  `default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; style-src 'self' 'unsafe-inline'; connect-src 'self' wss://<own host>; form-action 'self'; base-uri 'self'; frame-ancestors 'none'`.
  Inline and eval stay allowed because dev servers need them (hot reload
  preambles); what matters for leaks is that loads, requests, forms and frames
  can reach only the preview's own host. It also sets
  `X-DNS-Prefetch-Control: off` and drops `Link` preload headers to other
  hosts. A `<meta>` CSP in the page can only add restrictions.
- **What remains.** A page can still navigate the whole tab, or open a new
  window, to another site with data in the URL; CSP has no reliable control
  for top-level navigation. WebRTC is only partly covered by CSP. The
  verification card says so, the operator sees the address change, and the
  scan, the short expiry and the per-request audit limit the damage.
- **The app never sees operator credentials.** The console cookie never
  reaches the preview domain; the gateway's own cookie and the ticket are
  removed before the request is forwarded. Identity goes to the app only as an
  `X-Glovecell-User` header if the chart enables it.

### 4.4 Changes after the scan (hot reload)

Previews exist for work in progress, and dev servers reload when files change.
The approval is bound to the scan's digests, so:

- while a preview is live, the controller re-hashes the directory every 2
  minutes with a short read-only Job (the same isolation as the scan);
- **dependencies changed** (lockfile, manifest or installed packages): the
  preview is **suspended**, the route answers "changed since approval", and
  the card asks for a new scan and approval;
- **sources changed only**: the secret scan runs again on the changed files;
  clean results keep the preview live and add a history record, findings
  suspend it like a dependency change;
- a change is noticed within one check interval (up to 2 minutes), which the
  card states.

### 4.5 Audit

- **Per request** (glovecell.md 4.8, with `direction: in` and
  `channel: preview`): time, cell, approval ID, request ID, operator, host,
  port, method, path without query values, status, bytes each way, duration.
  WebSockets: open, close, frame counts and bytes each way (D13 item 3).
  Records go to the ingress container's audit outbox; if it is full, the
  container refuses new requests (fail closed).
- **Lifecycle records:** requested, scanned, approved (by whom, until when),
  first opened, suspended after a change, revoked, expired, ended because the
  process stopped or the pod restarted.

## 5. Feature 2: the foreground forward command

### 5.1 Flow

```text
$ glovecell forward my-repo 5432 --console https://console.example.com
Listening on localhost:5432 on this machine (not yet connected to the cell).
Approve this forward in the console:
  https://console.example.com/cli/forward/7Q2M-K4XD
Approved by alex@example.com until 15:42 (1 h).
Forwarding localhost:5432 to my-repo port 5432.
Every program running as you on this machine, including local agents,
can connect while this runs. Ctrl-C stops it.
```

1. **Console address.** The CLI learns the console from `--console` or the
   `GLOVECELL_CONSOLE` environment variable; there is no configuration file.
   The console's **Forward** button shows the full command to copy.
2. **Bind first.** The CLI opens its local listener first (`127.0.0.1` and
   `::1` on the requested port, or the next free one), so the card shows the
   real port. It refuses any other bind address. `--unix PATH` listens on a Unix
   socket instead, created in a directory only the operator can read (for
   Postgres: `psql -h DIR`), which other users and most sandboxes on the
   machine cannot reach. Until approval, accepted connections wait.
3. **Sign-in and approval.** The CLI makes an in-memory key pair and a PKCE
   challenge, starts a one-time loopback callback, and **prints** the console
   URL (it does not launch a browser, so it runs no other program). The
   console signs the operator in with OIDC and shows the approval card
   (section 1), including the client address it saw. There is no device-code
   mode in the first build: a code someone else can type in is an easy phishing
   path for approvals.
4. **Ticket.** On approval the console redirects to the loopback callback with
   a one-time code; the CLI exchanges it, with the PKCE verifier and its public
   key, for a forward ticket: audience `glovecell-forward`, one cell, one port,
   the operator, the approval's expiry, and the key's thumbprint. The console
   compares the exchange's client address with the sign-in's and records both.
   This keeps the console rule that OIDC access tokens are not API bearer
   credentials (glovecell.md 7.2). The ticket and key live only in the
   command's memory.
5. **Carry.** Each accepted TCP connection opens one WebSocket to
   `wss://tunnel.<domain>/cells/<cell>/forward/<port>`. The ticket goes in an
   `Authorization` header, never in the URL, together with a signature over a
   fresh timestamp made with the CLI's key, so a copied ticket is useless
   without the key. The ingress container checks ticket, signature, approval
   and expiry, dials the sandbox pod on that port, and copies bytes both ways.
   One WebSocket per connection (the shape T3 uses) keeps the code small and
   gives one audit record per connection. WebSocket rather than HTTP CONNECT,
   because every Gateway API implementation routes WebSocket upgrades through
   an HTTPRoute, while CONNECT support varies.
6. **Same-user check.** On Linux the CLI accepts a local connection only if
   the connecting socket belongs to the same OS user (from `/proc/net/tcp`), as
   T3's forward does. macOS and Windows offer no simple equivalent, so there
   the card and the CLI warn more strongly and suggest `--unix`. Neither
   protects against other programs running as the operator, which the warning
   names.
7. **End.** The CLI keeps its own timer: at expiry it closes the listener and
   every connection and exits with "approval expired at 15:42; run the command
   again to ask for a new one", whether or not the gateway has closed anything.
   The ingress container closes that forward's connections at the same time on
   its own clock. Ctrl-C closes the listener and tells the console to end the
   approval early; if the CLI is killed, the approval still ends at expiry.

### 5.2 Approval model and limits

| Question | Answer |
| --- | --- |
| Which ports | the same rules as previews (reserved control ports refused); chart switch `forwards.enabled`, off by default |
| HTTP | refused unless the port also has an approved preview (open question PT2, option D): the ingress container reads the first bytes, and an HTTP request line or a TLS ClientHello offering `http/1.1` or `h2` closes the connection with a message pointing to previews. A protocol where the server speaks first (no client bytes within 300 ms) is not HTTP and passes |
| Who asks | only the operator, by running the command. An agent cannot start a forward; it can suggest one in a blocked report |
| Who approves | the operator who runs the command, who must be in the approver group; the card makes the facts explicit |
| What an approval binds | cell, port, pod UID, operator, the CLI's key thumbprint, the client address seen at sign-in |
| Expiry | default 1 hour, maximum 8 hours, ends early on Ctrl-C, revoke, pod restart or cell suspend; extending is a new approval |
| Stored as | an `AccessGrant` with a `forward` member (D12), so admission enforces the 8-hour ceiling and only the controller writes it; delivered to the ingress container as in 3.2 |
| Limits | one pending forward request per operator, cell and port; at most 5 live forwards per operator and 10 per cell; at most 64 concurrent connections per forward (as T3); idle timeout 15 minutes; per-cell byte caps shared with other channels |
| Scan | none for non-HTTP ports (PT2) |

### 5.3 Who can reach the tunnel

- The tunnel and console host names resolve to the LAN or VPN listener, so
  from a cell they are private addresses, which the egress proxy refuses at
  every level. The egress proxy also refuses those host names by name, whatever
  they resolve to.
- The ingress container refuses tunnel and sign-in requests whose client
  address (as the edge reports it) is in the cluster's pod or node ranges.
- A test (build step 6) proves that a cell can reach neither the tunnel nor the
  console host, so an agent cannot start a forward or an approval flow on the
  operator's behalf from inside the cluster.

### 5.4 Audit

- **Per connection:** approval ID, forward ID, operator, cell, port, client
  address as the edge reports it, protocol seen, open and close time, bytes
  each way, close reason (client, app, expiry, revoke, cap, HTTP refused).
- **Lifecycle:** requested, approved, started, stopped by the operator,
  expired, revoked, ended by pod restart.

### 5.5 The in-cluster rule

The rule allows "optional one-shot CLI commands that run with the operator's
own identity" and forbids port-forwards and anything left running on an
operator machine ([in-cluster.md](../in-cluster.md)). `glovecell forward` is
allowed as an `operator-cli` component on these terms (decided 2026-11-20,
section 1):

- it uses the operator's own console sign-in, not a kubeconfig and not a
  Glovecell service credential;
- every run needs an approval that states what section 1 lists;
- it runs in the foreground and ends at expiry, on Ctrl-C, or when the
  terminal closes;
- it installs nothing, starts no other process and writes no ticket, key,
  token or configuration file.

Nothing in Glovecell depends on it running; closing a laptop just ends the
forward. It needs no Kubernetes access.

How `internal/installation/incluster_test.go` encodes it (built with the
command, build step 7):

1. **Lifetime on every CLI entry.** Every `operator-cli` entry in
   `docs/components.json` must have `"lifetime"`: `"one-shot"` or
   `"foreground"`. A list `allowedForeground` in the test, like
   `allowedExceptions`, names the commands that may keep running
   (`forward` only) and may only shrink.
2. **No other processes.** `go list -deps` on the forward package must not
   include `os/exec`; an AST scan of its files refuses calls to
   `os.StartProcess`, `syscall.ForkExec`, `syscall.Exec` and `syscall.Setsid`.
   The CLI prints the sign-in URL instead of launching a browser, so no
   exception is needed.
3. **Nothing written.** A behaviour test runs the command against a fake
   console and gateway with `HOME`, `TMPDIR` and every `XDG_*` directory set to
   empty temporary directories, and checks they are still empty after a
   session and after cancellation, that no listener remains, and that a
   non-loopback bind address is refused.
4. **Expiry without help.** A test whose fake gateway never closes anything
   checks that the CLI exits on its own at the ticket's expiry.
5. **Docs.** The docs check gains the pattern `forward` together with
   `nohup`, `systemd-run`, `--daemon` or a trailing `&`, so no doc tells an
   operator to keep a forward running in the background.

No new exception is needed; the exception lists still only shrink.

## 6. Build or adopt: evaluation

The bar (AGENTS.md, "Prefer maintained open source"): releases within about
six months, more than one maintainer or a backing organisation, a security
policy, real adoption, a compatible licence, images or releases pinnable by
digest or checksum. Figures from the GitHub API on 2026-11-20; "commits" are
non-bot commits since 2026-05-20.

| Project | Last releases | Maintainers | Security policy | Adoption | Licence | Pinnable |
| --- | --- | --- | --- | --- | --- | --- |
| [wstunnel](https://github.com/erebe/wstunnel) | v11.1.0 2026-10-19, v11.0.0 2026-09-19 | one person: 50 of 66 commits, next 5 | `SECURITY.md`; 0 published advisories | 7.2k stars | BSD-3-Clause | yes (GHCR image, release checksums) |
| [chisel](https://github.com/jpillora/chisel) | v1.12.0 2026-08-29 (v1.13.0 pre-release 2026-10-11) | one person: all 14 commits | none in the repository; 3 published advisories | 16.8k stars, 250 open issues | MIT | yes |
| [Pomerium](https://github.com/pomerium/pomerium) + [pomerium-cli](https://github.com/pomerium/cli) | v0.34.1 2026-11-11, v0.34.0 2026-11-04; CLI v0.34.1 2026-11-11 | company-backed: 248 commits, 6+ regular authors; CLI 41 commits, 5 authors | `SECURITY.md`, 14 published advisories | 5.1k stars (CLI 16) | Apache-2.0 | yes |
| [oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy) | v7.15.6 2026-11-05, v7.15.5 2026-10-01 | organisation: 40 commits, one main author | `SECURITY.md`, 15 published advisories | 15.4k stars | MIT | yes |
| Glue in the existing Go gateway | ships with Glovecell | Glovecell maintainers | Glovecell's process | n/a | Glovecell's | yes (Glovecell images by digest) |

How each fits this job:

| | wstunnel | chisel | Pomerium (+ CLI) | oauth2-proxy | Go gateway glue |
| --- | --- | --- | --- | --- | --- |
| Per-cell, per-port routing with approvals and expiry | a server per cell; port restriction in its own YAML, separate from Glovecell's approvals | a server per cell; `--remotes` restrictions | a route per cell and port, generated by the controller (Ingress or config) | one upstream per instance or forward-auth only | native: the ingress container receives the cell's approvals |
| OIDC with an existing provider | none: header or path secret only; needs an auth layer in front | none: user and password over SSH | yes, any OIDC provider | yes | reuses the console's existing OIDC (`go-oidc`) |
| HTTP previews | no | no | yes | yes | yes (`httputil.ReverseProxy`, handles WebSocket upgrades) |
| Raw TCP | yes | yes | yes (`tcp+https` routes, `pomerium-cli tcp`) | no | yes (`gorilla/websocket`, already a dependency) |
| Operator-side client | `wstunnel` binary | `chisel` binary | `pomerium-cli`; caches its sign-in token in the user's cache directory | none (browser only) | the Glovecell CLI the operator already has; ticket and key kept in memory |
| Scan gate, approval cards, per-request audit to Glovecell's store | all glue | all glue | glue; its authorize log is a second audit trail | glue | in place |
| Resource cost | a few MiB per server; one per cell or a shared one with routing glue | similar | a new deployment of its own (Envoy plus authenticate, authorize and databroker services), typically hundreds of MiB | one small pod | one small extra container per gateway pod, no new image |
| New required dependency for every install | yes | yes | yes | yes (HTTP only) | no |

Also looked at and set aside:

- **The identity provider's own proxy** (for example an authentik proxy
  outpost): HTTP forward-auth per application, no raw TCP to a local client,
  and it would tie Glovecell to one identity provider.
- **Teleport**: covers web apps and TCP with its own client, but it is a whole
  access platform with its own identity, agents and licensing (AGPL-3.0
  source); far more than this job.
- **Hosted tunnels** (Cloudflare tunnels, vendor relays): traffic and trust
  pass through a third party. Glovecell already accepts this for one opt-in
  case, T3 Connect, which uses T3's relay and cloudflared; previews and
  forwards should not depend on it, so they stay inside the cluster.
- **Kubernetes port forwarding**: needs cluster credentials with exec or
  port-forward rights on the operator machine, which the in-cluster rule
  forbids.

**Recommendation: build the glue in the existing Go gateway, and leave a
seam for Pomerium.**

- The hard parts are Glovecell's own: per-cell routes that exist only while
  approved, the scan gate (D13 item 8), approval cards with observed and
  reported facts, expiry on the gateway's clock, live approval delivery, and
  audit into the same outbox as egress. Every candidate still needs all of that
  as custom code.
- The transport itself is small with what the module already uses: Go's
  reverse proxy for HTTP and WebSocket, `gorilla/websocket` for the forward,
  `go-oidc` and `go-jose` for sign-in and tickets. No new dependency.
- wstunnel and chisel fail the maintainer bar (one person each) and have no
  OIDC. oauth2-proxy covers only HTTP sign-in, which the console already does.
- Pomerium passes the bar and is the best adopt option, but it would become a
  required, sizeable dependency for every Glovecell install, and its CLI
  stores a sign-in token on the operator machine.
- **Seam:** the ingress container verifies identity with a public key set. A
  later, optional chart setting can trust a deployment's Pomerium (its signed
  `X-Pomerium-Jwt-Assertion`) as the edge instead of the console's ticket
  flow, for deployments that already run it.

## 7. T3 cells and T3's own preview tunnel

T3 Code 0.1.11 (pingdotgg/t3code PRs #39393 and #39415, released 2026-11-20)
added `/api/preview/tunnel`: a WebSocket that carries one TCP connection to
any loopback port of the T3 host, for any client with the `terminal:operate`
scope. The desktop app uses it for in-app browser tabs and a "forward port"
feature; a server setting `enablePreviewTunnel` and a managed-policy limit
`limits.previewTunnel` turn it off. Glovecell cells pin T3 v0.1.9, which does
not have it.

In a cell, that tunnel would reach every port without Glovecell's approval
card, expiry, scan or audit. **Recommendation:** when T3 cells move to 0.1.11
or later, ship T3's managed policy file `/etc/t3code/policy.json` in the T3
workspace image (root-owned, read-only to the agent) with
`"limits": {"previewTunnel": false}`. Then every client uses the same audited
path.

- **Guard first.** #180 adds a test that fails if the Dockerfile bundles T3
  0.1.11 or later without that policy (build step 1), so the bump cannot ship
  the tunnel switched on.
- **What T3 users lose:** opening a cell's dev server inside T3's in-app
  browser by clicking `localhost:5173`, and T3's own "forward port". They
  open the Glovecell preview URL instead (T3's browser can load it like any
  HTTPS page) and use `glovecell forward`.
- **What stays:** with the tunnel off, T3 streams such tabs from a headless
  browser inside the cell. That only shows pages to someone who already has
  a T3 session (terminal access) in that cell; it adds no network path.
- **Version gate:** T3 refuses to start with a policy key it does not know,
  so the file must ship only with T3 0.1.11 or later; the #180 test checks the
  pin and the file together.

## 8. Build plan

Each step is one pull request with its tests; every switch is off by default.

| Step | Change | Tests |
| --- | --- | --- |
| 0 | This design and D16 | maintainer review |
| 1 | T3 guard (#180) | bundled T3 0.1.11+ without `limits.previewTunnel=false` fails; 0.1.9 passes |
| 2 | Move the download-grant control listener from loopback to a Unix socket on a proxy-only volume | the old loopback port is closed; a second container in the pod cannot add a grant; grants still work |
| 3 | `Preview` kind and forward member of `AccessGrant`; admission (port range, reserved ports, expiry at most 8 h, only the controller writes status); approvals and rate limits in PostgreSQL | admission denials for each rule; non-approver and agent identity cannot approve; second pending request for the same port refused; deny cooldown; per-cell caps |
| 4 | Scan Job (D13 item 8) with dependency and source digests, the 2-minute re-hash, and the listener detector | scan has no token and only scanner egress; scanner down means pending; dependency change suspends; source change with a secret suspends; concurrent-scan cap; detector output lands in the "reported" part only |
| 5 | Ingress container: own UID, no `/tls`, Unix control socket, `ingress-grant`, ticket check, host cookie, reverse proxy, WebSocket, CSP, audit; controller writes HTTPRoute and per-port worker ingress rule and re-sends approvals after a restart | unapproved port refused; expired approval refused on its own clock with the controller down; revoke closes an open WebSocket within 5 s; restart drops approvals until the controller re-sends them; forged, expired, replayed and wrong-host tickets refused; return host taken from the approval, not a parameter; ticket removed from the URL and `Referrer-Policy: no-referrer` on the hop page; `SameSite=Strict` cookie set and sent after the hop; WebSocket upgrade and POST with a foreign `Origin` refused; app `Set-Cookie` made host-only; cookie names the app never set are dropped; console and gateway cookies never reach the app; `/.glovecell/` unreachable by the app; CSP replaced; byte caps; full audit outbox refuses; container has no `/tls` and no credential files; pod restart ends the preview |
| 6 | Forward endpoint in the ingress container: first-bytes HTTP check, key-bound tickets, in-cluster source refusal | ticket audience, cell, port and key mismatches refused; ticket in a URL ignored; reserved port refused; HTTP and TLS-with-HTTP-ALPN refused without a preview, server-first protocol passes; 64-connection cap; idle timeout; expiry and revoke close live connections; a cell cannot reach the tunnel or console host; one audit record per connection |
| 7 | Console: sign-in handshake, verification card, approval card (observed and reported parts), Previews & dev links for cells, revoke, header and inbox entries; `glovecell preview request` for agents; `glovecell forward` (PKCE with key, printed URL, bind first, `--unix`, same-user check on Linux); `components.json` entry and `incluster_test.go` additions (5.5) | CSRF and origin negative tests; card shows every field or "unknown" and never renders reported text as HTML; console sees and shows mismatched client addresses; loopback-only bind; nothing written to `HOME`, `TMPDIR` or `XDG_*`; CLI exits at expiry with a silent gateway; Ctrl-C ends the approval; `forwards.enabled: false` refuses |
| 8 | T3 managed policy shipped with the T3 bump to 0.1.11+ (section 7) | the step 1 test passes with the new pin; T3 server reports the tunnel off |

Out of scope:

- UDP, SOCKS and multiplexed tunnels.
- A device-code sign-in for forwards (reconsider after the first build).
- Reverse forwards (a cell reaching a port on the operator's machine): refused.
- Public or unauthenticated share links, and public edge listeners.
- Forwards started by agents, or from phones.
- SSH forwarding through glove-hub (stays `DisableForwarding yes`).
- Moving the Orca and AO session routes onto the ingress listener (network.md
  section 7, step 1; its own change).
- A relay for loopback-bound apps inside the workspace container (later, if
  the kit instruction is not enough).
- Previews for sandboxes of the older sandbox-control backend, which keep
  their own path.

## 9. Open questions

**PT2. Forwards and scanning.** D13 item 8 requires a scan for every preview.
A forward of a database port has no app directory to scan, but a forward of
an HTTP port would be an unscanned preview on the operator's `localhost`,
sharing that host's cookies across ports.
- **D (recommended):** forwards need no scan, and refuse HTTP (an HTTP request
  line, or TLS offering `http/1.1` or `h2`) unless the port also has an
  approved preview. The approval card, expiry, refused control ports and
  loopback-only bind are the controls for everything else.
- **A:** forwards need no scan and carry any protocol.
- **B:** scan the cell's workspace once per forward approval (slower, findings
  rarely relevant to a database).
- **C:** allow forwards only to ports that also have a scanned, approved
  preview (rules out most databases).

**PT3. T3's own tunnel in cells.**
- **A (recommended):** set `limits.previewTunnel=false` in T3 images from
  0.1.11 on (section 7; guarded by #180). Every client uses the audited path;
  T3 users lose in-app `localhost` tabs and T3's forward button.
- **B:** leave T3's tunnel on and document it as a second, unaudited path for
  T3 sessions only.
- **C:** keep T3 on 0.1.9 until previews ship.
