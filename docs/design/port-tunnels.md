# Design: previews and port forwards for every client

**Status:** proposed design, 2026-11-20. Docs only; nothing here is built yet.
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
     is listening, who asked, the address and when it ends. The page then
     opens at a private address such as `https://5173--my-repo.preview.example.net/`,
     behind the console's sign-in, in any browser: desktop, phone, or a
     client's built-in browser.
  2. **A foreground forward command.** `glovecell forward my-repo 5432` asks
     for the same kind of approval, then listens on `127.0.0.1:5432` on the
     operator's machine and carries each connection to the cell, so `psql`
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
    a separate preview domain, one origin per preview, a verification card,
    the required scan (D13 item 8), and short expiries.
  - A forward puts a cell's port on the operator's loopback, where any local
    program or web page can reach it while it runs. Mitigations: loopback-only
    bind, refused control ports, short expiry, plain warning on the card.
  - Both are off by default (chart values). Turning them off again removes the
    routes and closes open connections; reverting the code removes the
    features. Nothing is installed on an operator machine.
- **Words:**
  - **Cell gateway**: the per-cell pod that today is the egress proxy (every
    outbound request passes it). D14 adds an **ingress listener** to it for
    traffic coming in.
  - **Edge**: the cluster's shared entry point (a Gateway API implementation
    such as Traefik or Envoy Gateway) that terminates TLS for public names.
  - **HTTPRoute**: the Gateway API object that sends one host name or path
    from the edge to a backend Service.
  - **OIDC**: OpenID Connect, the sign-in protocol the console already uses
    with the deployment's identity provider.
  - **WebSocket**: a long-lived two-way connection that starts as an HTTP
    request; ingress controllers route it like normal HTTPS.
  - **HTTP CONNECT**: an HTTP method that asks a proxy to open a raw TCP
    connection; many ingress controllers do not route it.
  - **Loopback**: `127.0.0.1` / `::1`, addresses reachable only from the same
    machine (or, inside a cell, the same pod).
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

## 1. The maintainer's rule for this feature

Decided 2026-11-20, in the maintainer's words: "Generally I don't want things
forwarded long term, but for testing or previews that's fine as long as it's
made clear to me when asking for approval."

What the design takes from it:

- **Short-lived previews and forwards are allowed, each behind an explicit
  approval.** Every approval card (in the console, or the browser page the CLI
  opens) states plainly:
  - which cell and which port;
  - what is listening there: process name and command line, when the gateway
    can read them, otherwise "unknown";
  - who asked: the agent (with the reason it gave) or the operator;
  - where it will be reachable: the preview URL, or `localhost:PORT` on the
    machine that runs the forward command (its host name as the CLI reports
    it);
  - how long it lasts, as an absolute expiry time in the operator's time zone
    plus a countdown.
- **No long-term forwards.** Previews and forwards have a default expiry of
  1 hour and a hard maximum of 8 hours (the same ceiling as other timed
  grants, D12). They end at expiry even while the command or the page is still
  open. Extending is a new approval with a new card, never automatic. A
  lasting need goes through the dev-link path (scan and approval) instead.
- **Active previews and forwards are visible** in the console (the cell's
  **Previews & dev links** section, the sticky header's grant list and the
  **Needs attention** inbox), each with a one-click **Revoke**. Every start,
  stop, expiry and revoke is an audit record.
- **The forward command fits the in-cluster rule** as a one-shot operator CLI
  on these terms: it uses the operator's own identity, it needs the approval
  above, it ends at expiry or when the command ends, and nothing is installed
  or left behind on the machine (section 5).

## 2. What exists today, and the gap

| Piece | Today | Gap |
| --- | --- | --- |
| Sandbox pod ingress | Base network policy has empty ingress (`internal/network`). Two orchestrator classes add their own route straight to the worker: Orca's spoke (HTTPRoute `<cell>.<domain>` to port 6768) and AO (HTTPRoute to port 3011), each authenticated by the app's own pairing or device tokens | No route for an app's own port. The existing routes bypass the gateway, so they have no Glovecell per-request audit |
| Cell gateway | The proxy pod accepts connections only from its own worker on the egress port, and from the metrics collector on the telemetry relay port | No ingress listener (network.md section 7, step 1, not started) |
| Previews & dev links in the console | `web/src/workspace/sources/previews.ts` reads `GET /api/previews` and `/api/devlinks` from the older sandbox-control backend, mounted under `/sandboxes/` by `internal/web/sandbox_control.go`; decisions post to its `/decision` and `/devlink-decision`. Its URLs are served by that backend's own channels | For Glovecell cells the section shows "Project sandboxes do not publish previews or dev links yet" (`ProjectPreviews`) |
| Agent request command | None in Glovecell cells (no `dev-link publish` subcommand in `docs/components.json`) | Agents cannot ask for a preview |
| Scanner for previews and dev links | Required by D13 item 8; not built for cells | Needed before any preview can be approved |
| `Preview` kind | Named as "later" in [kubernetes-api.md](kubernetes-api.md) | Not defined |
| Operator CLI | `connect`, `exec` and `provider import` run once with the operator's own kubeconfig | No command that reaches a cell port without cluster credentials |

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
operator browser / client app                    operator machine (forward)
        |  https://5173--my-repo.preview.example.net     |  glovecell forward my-repo 5432
        |                                                |  127.0.0.1:5432 <- psql
        v                                                v  wss://tunnel.example.net/cells/my-repo/5432
 +------------------------------- edge (Gateway API, TLS) ----------------------------+
 |  HTTPRoutes written by the controller, only for approved previews and forwards     |
 +---------------------|-------------------------------------------|-----------------+
                       | sign-in redirect                          |
                       v                                           v
           console (controller binary)               cell gateway pod (gc-<uid>)
           OIDC session, approval cards,             ingress listener (new):
           signs tickets  ------- policy, keys ----> checks ticket, approval, expiry
                                                     audit record per request/connection
                                                     egress proxy (unchanged)
                                                               |
                                                               | approved ports only
                                                               v
                                                     sandbox pod: app on :5173, Postgres on :5432
```

- The edge routes only what is approved; the gateway checks again on its own
  clock, so an expired approval is refused even if a route is still in place.
- The sandbox pod accepts connections only from its own gateway pod, only on
  ports with a live approval. The controller adds the port to the worker's
  ingress policy on approval and removes it on expiry or revoke.
- The gateway holds no Kubernetes credentials, as today. It receives the list
  of live approvals and the ticket-verification public key through the same
  controller-written policy it already reads.
- **Loopback-bound apps.** The gateway is a different pod, so it reaches the
  app on the pod's address, not `127.0.0.1`. Many dev servers bind loopback by
  default (Vite, Postgres with `listen_addresses=localhost`). The approval card
  says "bound to loopback only" when that is the case, and the agent kit tells
  agents to bind `0.0.0.0` for anything they want to show. A relay inside the
  workspace container is a later option (section 8), not part of the first
  build.

## 4. Feature 1: preview URLs

### 4.1 Flow

1. **Request.** Either the agent runs
   `glovecell preview request --port 5173 --dir web/ --note "signup page"`
   (through the gateway's local endpoint, like a blocked report, 4.6), or the
   operator clicks **Preview** on a detected listener in the console.
2. **Scan.** D13 item 8 says there are no unscanned previews. The controller
   starts the dev-link scan Job on the named directory (read-only mount, no
   token, egress only to the scanner). When the scanner is down, the request
   waits.
3. **Approval card** (approver group, D13 item 2), with the five facts from
   section 1, the scan findings, and the expiry picker (default 1 hour, at most
   8 hours).
4. **Route.** On approval the controller records the approval (PostgreSQL,
   D13 item 2), writes a `Preview` object whose `Approved` condition generates
   the HTTPRoute for `<port>--<cell>.<previewDomain>`, and opens the worker
   ingress policy for that port from the gateway pod only.
5. **Sign-in on first visit.** The browser has no cookie for the preview host,
   so the gateway redirects to the console. The console checks the OIDC
   session (signing in if needed) and that the user may view this cell, shows
   the **verification card** once per host and session ("This page is served by
   an agent in cell `my-repo`, port 5173. It is not the console; do not enter
   passwords."), and redirects back to
   `https://5173--my-repo.preview.example.net/.glovecell/session?ticket=…`.
   The ticket is signed by the console, names the host, the operator and an
   expiry of 60 seconds, and works once.
6. **Session.** The gateway checks the ticket and sets a host-only
   `__Host-` cookie (`HttpOnly`, `Secure`, `SameSite=Lax`) that ends with the
   approval. It strips that cookie and every `/.glovecell/` path before the
   request reaches the app, then proxies HTTP and WebSocket to the app.
7. **End.** The preview ends at the first of: expiry, revoke, the process
   stops listening, or the pod restarts (approval is bound to the pod UID). The
   controller removes the route and the policy rule, and the gateway closes
   open WebSockets.

### 4.2 Approval model

| Question | Answer |
| --- | --- |
| Which ports | 1024–65535 the sandbox listens on, except reserved ports: the gateway's own, the telemetry relay, each orchestrator class's control ports (Orca runtime, AO's loopback API and remote listener, the T3 server) and a chart list `previews.deniedPorts` |
| Who asks | the agent (`glovecell preview request`) or the operator (console) |
| Who approves | a member of the approver group, in the console; agents never approve |
| What an approval binds | cell, port, pod UID, scan digest of the directory, operator who approved |
| Expiry | default 1 hour, maximum 8 hours; extending is a new approval and a new card |
| Longer needs | a dev link: same scan, its own approval and deadline (glovecell.md 4.7) |

### 4.3 Browser safety

- **Separate preview domain.** Previews are served from a domain that is not
  the console's (chart value `previews.domain`; ideally its own registrable
  domain, as code-hosting sites do for user content), so preview pages are a
  different site from the console and cannot ride its cookies.
- **One origin per preview.** `<port>--<cell>` gives every cell and port its
  own origin, so one preview's scripts cannot read another's pages or
  cookies. Labels longer than 63 characters use a short hash of the cell name.
- **No cookie tossing.** The gateway rewrites the app's `Set-Cookie` headers
  to host-only (drops `Domain=`), so an app cannot set cookies for other
  previews.
- **The app never sees operator credentials.** The console cookie never
  reaches the preview domain; the gateway's own cookie and the ticket are
  removed before the request is forwarded. Identity goes to the app only as
  an `X-Glovecell-User` header if the chart enables it.

### 4.4 Audit and network policy

- **Per request** (glovecell.md 4.8, with `direction: in` and
  `channel: preview`): time, cell, request ID, operator, host, port, method,
  path without query values, status, bytes each way, duration. WebSockets:
  open, close, frame counts and bytes each way (D13 item 3). Records go to the
  gateway's audit outbox; if it is full, the ingress listener refuses new
  requests (fail closed).
- **Lifecycle records:** requested, scanned, approved (by whom, until when),
  first opened, revoked, expired, ended because the process stopped or the pod
  restarted.
- **Network policy:** edge pods → gateway ingress port only; gateway →
  sandbox pod on approved ports only; nothing else gains a path into the
  sandbox. The rule is per pod and removed on expiry, like a LAN grant.

## 5. Feature 2: the foreground forward command

### 5.1 Flow

```text
$ glovecell forward my-repo 5432
Opening your browser to approve this forward...
Approved by alex@example.com until 15:42 (1 h).
Forwarding localhost:5432 on this machine to my-repo port 5432 (postgres).
Anything on this machine can connect while this runs. Ctrl-C stops it.
```

1. **Sign-in and approval.** The command starts a one-time loopback callback
   and opens the console's CLI page with a PKCE challenge (the same pattern as
   `gh auth login` and `kubectl oidc-login`). A `--device` flag shows a code to
   enter on another device instead. The console page signs the operator in
   with OIDC and shows the approval card: cell, port, what listens there, "you
   asked, from host `<name>`", `localhost:5432` on that machine, and the expiry.
2. **Ticket.** On approval the console redirects to the loopback callback with
   a one-time code; the CLI exchanges it (with the PKCE verifier) for a forward
   ticket: audience `glovecell-forward`, one cell, one port, the operator, the
   approval's expiry. This keeps the console rule that OIDC access tokens are
   not API bearer credentials (glovecell.md 7.2). The ticket lives only in the
   command's memory.
3. **Listen.** The CLI listens on `127.0.0.1` (and `::1`) on the requested
   port, or the next free one, and prints the address. It refuses any other
   bind address.
4. **Carry.** Each accepted TCP connection opens one WebSocket to
   `wss://tunnel.<domain>/cells/<cell>/forward/<port>` with the ticket. The
   gateway checks ticket, approval and expiry, dials the sandbox pod on that
   port, and copies bytes both ways. One WebSocket per connection (the shape
   T3 uses) keeps the code small and gives one audit record per connection.
   WebSocket rather than HTTP CONNECT because every Gateway API
   implementation routes WebSocket upgrades through an HTTPRoute, while CONNECT
   support varies.
5. **End.** At expiry the gateway closes every connection of that forward and
   the CLI exits with "approval expired at 15:42; run the command again to ask
   for a new one". Ctrl-C closes the listener and tells the console to end the
   approval early. If the CLI is killed, the approval still ends at its expiry.

### 5.2 Approval model

| Question | Answer |
| --- | --- |
| Which ports | the same rules as previews (reserved control ports refused); chart switch `forwards.enabled`, off by default |
| Who asks | only the operator, by running the command. An agent cannot start a forward; it can suggest one in a blocked report |
| Who approves | the operator who runs the command, who must be in the approver group; the card makes the facts explicit |
| What an approval binds | cell, port, pod UID, operator, the requesting machine's name as reported |
| Expiry | default 1 hour, maximum 8 hours, ends early on Ctrl-C, revoke, pod restart or cell suspend; extending is a new approval |
| Stored as | an `AccessGrant` with a `forward` member (D12), so admission enforces the 8-hour ceiling and only the controller writes it |
| Limits | at most 64 concurrent connections per forward (as T3), idle timeout 15 minutes, per-cell byte caps shared with other channels |
| Scan | none: there is no app directory to scan for a database port. Open question PT2 |

### 5.3 Audit and network policy

- **Per connection:** forward ID, operator, cell, port, client address as the
  gateway sees it, open and close time, bytes each way, close reason (client,
  app, expiry, revoke, cap).
- **Lifecycle:** requested, approved, started, stopped by the operator,
  expired, revoked, ended by pod restart.
- **Network policy:** identical to previews: the tunnel route reaches only
  the gateway's ingress port; the gateway reaches only approved sandbox ports.

### 5.4 The in-cluster rule

The rule allows "optional one-shot CLI commands that run with the operator's
own identity" and forbids port-forwards and anything left running on an
operator machine ([in-cluster.md](../in-cluster.md)). `glovecell forward` is
allowed as an `operator-cli` component on these terms (decided 2026-11-20,
section 1):

- it uses the operator's own OIDC sign-in, not a kubeconfig and not a
  Glovecell service credential;
- every run needs an approval that states cell, port, listener, requester,
  reachable address and expiry;
- it runs in the foreground and ends at expiry, on Ctrl-C, or when the
  terminal closes;
- it installs nothing, starts no background process and writes no ticket,
  token or configuration file.

Nothing in Glovecell depends on it running; closing a laptop just ends the
forward. It is not a cluster port-forward: it needs no Kubernetes access.

How `internal/installation/incluster_test.go` encodes it (built with the
command, phase 6):

1. `docs/components.json` lists `forward` as `operator-cli` with
   `"lifetime": "foreground"`; the test requires that field for any
   `operator-cli` command that keeps a listener open, so a new long-running
   client command cannot be added silently.
2. A source check on the forward package: it must not call
   `os.StartProcess`, `exec.Command`, `syscall.Setsid` or write under
   `os.UserConfigDir`, `os.UserCacheDir` or the home directory.
3. A behaviour test runs the command against a fake gateway with `HOME` and
   the `XDG_*` directories set to empty temporary directories, then checks
   that they are still empty after a session and after cancellation, that no
   listener remains, and that a non-loopback bind address is refused.
4. The docs check gains the pattern `forward` together with `nohup`,
   `systemd-run`, `--daemon` or a trailing `&`, so no doc tells an operator to
   keep a forward running in the background.

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
| Per-cell, per-port routing with approvals and expiry | a server per cell; port restriction in its own YAML, separate from Glovecell's approvals | a server per cell; `--remotes` restrictions | a route per cell and port, generated by the controller (Ingress or config) | one upstream per instance or forward-auth only | native: the gateway already holds the cell's policy |
| OIDC with an existing provider | none: header or path secret only; needs an auth layer in front | none: user and password over SSH | yes, any OIDC provider | yes | reuses the console's existing OIDC (`go-oidc`) |
| HTTP previews | no | no | yes | yes | yes (`httputil.ReverseProxy`, handles WebSocket upgrades) |
| Raw TCP | yes | yes | yes (`tcp+https` routes, `pomerium-cli tcp`) | no | yes (`gorilla/websocket`, already a dependency) |
| Operator-side client | `wstunnel` binary | `chisel` binary | `pomerium-cli`; caches its sign-in token in the user's cache directory | none (browser only) | the Glovecell CLI the operator already has; ticket kept in memory |
| Scan gate, approval cards, per-request audit to Glovecell's store | all glue | all glue | glue; its authorize log is a second audit trail | glue | in place |
| Resource cost | a few MiB per server; one per cell or a shared one with routing glue | similar | a new deployment of its own (Envoy plus authenticate, authorize and databroker services), typically hundreds of MiB | one small pod | none extra: a listener in the existing gateway process |
| New required dependency for every install | yes | yes | yes | yes (HTTP only) | no |

Also looked at and set aside:

- **The identity provider's own proxy** (for example an authentik proxy
  outpost): HTTP forward-auth per application, no raw TCP to a local client,
  and it would tie Glovecell to one identity provider.
- **Teleport**: covers web apps and TCP with its own client, but it is a whole
  access platform with its own identity, agents and licensing (AGPL-3.0
  source); far more than this job.
- **Hosted tunnels** (T3 Connect's relay, Cloudflare tunnels): traffic and
  trust leave the cluster, which Glovecell avoids (the AO integration makes
  the same choice: "Nothing leaves the cluster to make pairing work").
- **Kubernetes port forwarding**: needs cluster credentials with exec or
  port-forward rights on the operator machine, which the in-cluster rule
  forbids.

**Recommendation: build the glue in the existing Go gateway, and leave a
seam for Pomerium.**

- The hard parts are Glovecell's own: per-cell routes that exist only while
  approved, the scan gate (D13 item 8), approval cards with the five facts,
  expiry on the gateway's clock, and audit into the same outbox as egress.
  Every candidate still needs all of that as custom code.
- The transport itself is small with what the module already uses: Go's
  reverse proxy for HTTP and WebSocket, `gorilla/websocket` for the forward,
  `go-oidc` and `go-jose` for sign-in and tickets. No new dependency.
- wstunnel and chisel fail the maintainer bar (one person each) and have no
  OIDC. oauth2-proxy covers only HTTP sign-in, which the console already does.
- Pomerium passes the bar and is the best adopt option, but it would become a
  required, sizeable dependency for every Glovecell install, and its CLI
  stores a sign-in token on the operator machine.
- **Seam:** the gateway verifies identity with a public key set. A later,
  optional chart setting can trust a deployment's Pomerium (its signed
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

- **What T3 users lose:** opening a cell's dev server inside T3's in-app
  browser by clicking `localhost:5173`, and T3's own "forward port". They
  open the Glovecell preview URL instead (T3's browser can load it like any
  HTTPS page) and use `glovecell forward`.
- **What stays:** with the tunnel off, T3 streams such tabs from a headless
  browser inside the cell. That only shows pages to someone who already has
  a T3 session (terminal access) in that cell; it adds no network path.
- **Version gate:** T3 refuses to start with a policy key it does not know,
  so the file must ship only in images with T3 0.1.11 or later. A test in the
  image build checks that the pinned T3 version and the policy file go
  together.

## 8. Build plan

Each step is one pull request with its tests; every switch is off by default.

| Step | Change | Tests |
| --- | --- | --- |
| 0 | This design and D16 | maintainer review |
| 1 | `Preview` kind and forward member of `AccessGrant`; admission (port range, reserved ports, expiry at most 8 h, only the controller writes status); approvals in PostgreSQL | admission denials for each rule; non-approver cannot approve; agent identity cannot approve; missing scanner keeps a preview pending (fail closed) |
| 2 | Scan Job for previews and dev links (D13 item 8) | scan has no token and only scanner egress; findings shown; scanner down means pending, never approved |
| 3 | Gateway ingress listener: ticket check, host cookie, reverse proxy, WebSocket, audit; controller writes HTTPRoute and per-port worker ingress rule | unapproved port refused; expired approval refused on the gateway's clock with the controller down; forged, expired, replayed and wrong-host tickets refused; console and gateway cookies never reach the app; `/.glovecell/` unreachable by the app; `Set-Cookie` made host-only; full audit outbox refuses; pod restart ends the preview; interrupted approval write leaves no route |
| 4 | Console: sign-in handshake, verification card, approval card with the five facts, Previews & dev links for cells, revoke, header and inbox entries; `glovecell preview request` for agents | CSRF and origin negative tests; card shows every field or "unknown"; revoke closes open WebSockets; agent text bounded and treated as untrusted |
| 5 | Gateway forward endpoint | ticket audience, cell and port mismatches refused; reserved port refused; 64-connection cap; idle timeout; expiry and revoke close live connections; one audit record per connection |
| 6 | `glovecell forward` with PKCE and device sign-in; `components.json` entry; `incluster_test.go` additions (section 5.4) | loopback-only bind; nothing written to `HOME` or `XDG_*`; Ctrl-C ends the approval; killed CLI still expires; `forwards.enabled: false` refuses |
| 7 | T3 managed policy in T3 images on 0.1.11+ (section 7) | policy present only with a supporting T3 version; T3 server reports the tunnel off |

Out of scope:

- UDP, SOCKS and multiplexed tunnels.
- Reverse forwards (a cell reaching a port on the operator's machine): refused.
- Public or unauthenticated share links.
- Forwards started by agents, or from phones.
- SSH forwarding through glove-hub (stays `DisableForwarding yes`).
- Moving the Orca and AO session routes onto the ingress listener (network.md
  section 7, step 1; its own change).
- A relay for loopback-bound apps inside the workspace container (later, if
  the kit instruction is not enough).
- Previews for sandboxes of the older sandbox-control backend, which keep
  their own path.

## 9. Open questions

**PT2. Forwards without a scan.** D13 item 8 requires a scan for every
preview. A forward of a database port has no app directory to scan.
- **A (recommended):** forwards need no scan. The approval card, the 1-hour
  default, the 8-hour ceiling, refused control ports and loopback-only bind
  are the controls; previews keep the scan.
- **B:** scan the cell's workspace once per forward approval (slower, findings
  rarely relevant to a database).
- **C:** allow forwards only to ports that also have a scanned, approved
  preview (rules out most databases).

**PT3. T3's own tunnel in cells.**
- **A (recommended):** set `limits.previewTunnel=false` in T3 images from
  0.1.11 on (section 7). Every client uses the audited path; T3 users lose
  in-app `localhost` tabs and T3's forward button.
- **B:** leave T3's tunnel on and document it as a second, unaudited path for
  T3 sessions only.
- **C:** keep T3 on 0.1.9 until previews ship, then decide.
