# Projects and sandbox controls

The project dashboard includes lifecycle status, repository access, outbound HTTPS
rules and access history. An optional trusted backend brings pre-existing sandbox
controls into the same origin at `/sandboxes/`, with the same operator login and
navigation. This keeps existing policies, package approvals, previews and history
with the service that owns them; no worker or credential migration is required.

## Instant web access

When sandbox-control is mounted, the Sandboxes page starts with a **Web access**
panel. **Open all** lets every enrolled sandbox-control workspace reach any
public HTTP or HTTPS destination for the chosen window (15 minutes to 24
hours). **Lock all** returns them to their approved requests. Each row also has
its own **Open** or **Lock** button. Both take effect on the worker's next
request without a pause or restart: sandbox-control authorizes every request
against the live state. Open records requests for review but never approves
them, so locking really removes the access. Private networks, cluster services
and cloud metadata stay blocked in every state, and windows expire to Locked
on their own.

The console calls sandbox-control's JSON API through the `/sandboxes/` mount:
`GET /sandboxes/workspaces` returns the summaries and an `X-Sandbox-CSRF`
token, and `POST /sandboxes/egress` with `Accept: application/json` applies
`learning-open` or `learning-lock`. Console projects (ProjectWorkspaces) keep
fixed HTTPS rules enforced by their own proxy; those still change only while
paused, as described below.

## Console UI

The UI is a React app built with OpenUI components (`@openuidev/react-ui`) in
`web/`. `npm ci && npm run build` there writes `internal/web/dist`, which the
Go binary embeds; the container build regenerates it from the lockfile.
Dependencies are pinned exactly. The OpenUI theme tokens are generated into
static CSS because the console's CSP forbids injected styles, and dark mode
follows the operating system.

### Sandbox workspace view

Opening a sandbox shows one workspace at a time. A sidebar lists every
workspace (sandbox-control sandboxes and console projects, with counts of what
waits on you and a **LAN** badge while LAN access is open) and the sections of
the current one: **Overview**, **Internet**, **LAN devices**, **Packages**,
**Previews & dev links**, **Credentials**, **History**, plus **Runs** and
**Settings** for sandbox-control sandboxes. A sticky header shows the workspace
state, its pod, and every open grant with a countdown: web access (with Lock),
each LAN grant (with Restrict) and live dev links. On narrow screens the sidebar
becomes a workspace picker and a row of section links, and the header scrolls
away.

Routes are `#/sandboxes/<id>/<section>` and `#/projects/<name>/<section>`. The
earlier tab names (`access`, `devlinks`, `activity`, `repositories`) still land
on the matching section.

Each section reads its data through one module in
`web/src/workspace/sources/` (`internet.ts`, `lan.ts`, `packages.ts`,
`previews.ts`, `credentials.ts`, `history.ts`, `reports.ts`). The sections never
call the backends directly, so a later backend can replace a module without
changing the UI. A part of a section with no read API yet says "not available
yet" and links to the classic page.

The LAN devices section reads sandbox-control's `GET /api/lan` and posts its
actions to the existing `POST /egress` (`action=lan-open`, `lan-open-all`,
`lan-open-learned`, `lan-extend`, `lan-restrict`, `lan-forget`, with
`pod_uid`). **Restrict now**, from the section or the header, and **Stop** ask
for confirmation first, because both restart or stop the workspace pod and end
its agent sessions. The console proxies `GET /api/lan` and `GET /api/cluster`
(read only); cluster grants are still changed on the classic page.

`node scripts/preview.mjs [port] [host]` in `web/` serves the built console
against invented fixtures, with no backend, for local review.

## Preparation errors

Each project shows the current Ready condition next to its phase. A preparation,
policy or cleanup error appears as an attention panel with the actual message.
Condition changes are also retained in access history. Repeated retries of the
same condition do not create duplicate records.

An existing Forgejo `main` rule that falls short, for example one that does not
apply to administrators, is tightened by the controller during preparation; the
change is logged with the repository and the settings it changed. The controller
never weakens a rule. When Forgejo rejects or ignores the change, or a wildcard
rule might overlap `main`, the panel shows the shortfall (for example
`require at least 1 approval (currently 0)`) and directs the operator to
Settings > Branches. Fix that rule on Forgejo. The controller retries the same
request; deleting and recreating the project is unnecessary.

## Project access

Open a sandbox and select **Internet** to inspect its HTTPS grants. Add initial
destinations when opening a project, or pause an existing project and wait for
**Paused** before changing them. Each destination specifies an exact lowercase
hostname, supported methods and literal path prefixes. Path `/` allows the whole
host. Registry presets expand to download hosts and do not restrict package
names or versions. Removing every optional rule denies additional HTTPS access.

Saving leaves the project paused. The API verifies that all worker and proxy
pods, including terminating pods, are gone. The controller applies network and
proxy rules, records the applied policy, and then reports Paused. Choose Resume
after that update finishes. Existing streams cannot continue under retired
permissions because the proxy also stops during pause.

Repository grants are separate, controller-managed rules. Optional HTTPS rules
cannot widen Git-host or repository API access. Repository identity, inspection
CA, namespace and project volume are preserved when updating optional access.
Interrupted updates retry from the saved policy, accepting only resources matching
the old or requested policy. Unexpected resource drift remains an error.

## LAN hosts

A project can reach single computers on the operator's own network over plain
TCP, for example SSH to a dev box or an HTTP API on a device. Open a sandbox's
**LAN devices** section and use **LAN hosts**: enter one IPv4 address, the TCP ports
(each listed, for example `22, 8080`) and an optional note, then **Grant LAN
host**. Each grant has a **Remove** button. The Overview section lists the current
grants with who added them and when.

Grants change while the project runs. There is no pause, restart or
recreation: the controller rewrites the project's `lan-hosts` policy on its
next reconcile, normally within seconds. The direction is sandbox to computer
only. The grant adds an egress rule; nothing on the network can open a
connection into the sandbox, and the console gains no new route.

Only a signed-in OIDC operator from the allowlist can add or remove a grant.
The automation bearer secret and static login are refused with 403, even though
they have operator authority elsewhere. Requests use the console's usual
same-origin and JSON checks. The feature is off until an administrator sets
`LAN_GRANT_ALLOWED_CIDRS` (see [installation](installation.md#lan-host-grants)).
With it off, the form is hidden and existing grants can still be removed.

The controller refuses an address that is:

- not a single canonical IPv4 address: no CIDR, range, hostname or IPv6;
- outside every configured `LAN_GRANT_ALLOWED_CIDRS` range;
- inside `LAN_GRANT_DENIED_CIDRS`, where the operator lists node addresses
  and the node VLAN, the API and service VIPs, the pod and service CIDRs and
  router or gateway addresses;
- the address of any cluster node, read from the Node objects at grant and at
  every reconcile, whether or not the denylist names it;
- loopback, `0.0.0.0/8`, link-local including cloud metadata `169.254.169.254`,
  multicast, `240.0.0.0/4` or `255.255.255.255`;
- the first or last address of its allowed range, or any address ending in `.0`
  or `.255` (treated as network or broadcast addresses of a home `/24`).

Ports are 1 to 65535, listed one by one, at most 16 per host. Ranges are not
accepted: a grant is meant for a known service, and an explicit list keeps every
open port visible in the console, the audit record and the policy. A project
holds at most 8 hosts, one grant per address. To change a host's ports, remove
the grant and add it again.

### How it reaches the host

Workers normally reach only their inspecting proxy. A LAN grant is the one
direct exception: Cilium policy `lan-hosts` in the project namespace allows the
worker pod TCP to `<address>/32` on the listed ports. The proxy, other projects
and every other destination are unchanged, so the rest of the project's egress
stays exactly as restricted as before. The proxy is not involved, because it
only handles inspected HTTPS.

Raw TCP clients such as `ssh` ignore the proxy variables and connect directly.
HTTP clients that honour `HTTP_PROXY`, such as `curl`, Python and Node, would
send the request to the proxy, which refuses private addresses. Bypass it for
the granted address: `NO_PROXY=192.168.1.20 curl http://192.168.1.20:8080/` or
`curl --noproxy '*' ...`. The worker has no DNS for LAN names; use the address.

Traffic leaves the node with the node's address. A router or firewall between
the node network and the granted host must also allow it, from the node
addresses to that host and port. The controller never changes that device.

Removing a grant deletes its rule. Cilium checks egress policy on each packet the
worker sends, so open connections to that host are expected to stop as well.
Pause the project if you need certainty that a session has ended.

Every add and remove writes an immutable audit record with the operator, the
project, the address, the ports and the time, and the controller writes another
when it applies the policy. Both appear in **History**. If the operator later
narrows the ranges or turns the feature off, a project whose grant is now refused
moves to **Needs attention**, its worker stops and its `lan-hosts` policy is
deleted. Remove the grant to resume.

## Include pre-existing sandboxes

The legacy control backend contract is a server-rendered control service with
`X-authentik-username` operator authentication, same-origin POST forms, a signed
`__Host-sandbox-csrf` cookie and a matching form token. It retains its existing
authorization, policy validation and audit behavior.

Configure these controller environment values together, or set them under
`config` in the Helm values:

```yaml
SANDBOX_CONTROL_URL: http://sandbox-control.sandbox-system.svc.cluster.local:8080
SANDBOX_CONTROL_ORIGIN: https://sandboxes.example.com
SANDBOX_CONTROL_OPERATOR: operator
```

`URL` is the trusted internal control service, never a worker, preview listener,
Git broker or caller-supplied URL. `ORIGIN` is the backend's configured HTTPS
origin, which its existing POST check expects. `OPERATOR` is an explicit delegated
backend identity. Every authenticated console operator receives that identity's
sandbox-control privileges, so qualify the console's operator allowlist for both
services before enabling it. End users and agents must not be console operators.

Allow the controller to reach only the backend's control port, and permit that
source in the backend's ingress policy. The header-authenticated backend must
remain inaccessible to workers and untrusted clients. The project controller
does not add broad network access or discover backend URLs automatically.

The console strips incoming authentication/forwarded identity headers and inserts
the configured identity only after verifying its own login. It forwards only the
backend CSRF cookie, never console cookies or automation credentials. Form POSTs
must originate from the console; both backend CSRF checks remain in effect. HTML
links, form actions, polling and redirects are mounted under `/sandboxes/`.
Redirects to other origins and unrecognized control routes are rejected.

The **Sandboxes** list shows the backend's registered shared and provider
sandboxes beside project environments, with their state and how many requests
await review. Opening one shows its access, activity, previews and settings
under the same console bar.
Project environments continue to use their own controller and inspecting proxy.
The combined UI does not imply those environments share a network policy or a
trust boundary.

## Handoffs and Orca projects

Set both values to connect the console to an agent runtime's handoff intake,
such as the Orca sandbox:

```yaml
HANDOFF_URL: http://handoff-intake.runtime.svc.cluster.local:8791
HANDOFF_TOKEN_FILE: /control-secrets/handoff-token
HANDOFF_SANDBOX: runtime   # optional
```

The console sends the token as `Authorization: Bearer` on every intake request.
The intake must require it as well as limiting ingress to the controller; a
network policy alone is not its authentication. A token shorter than 32 bytes,
or a URL without a token, stops the controller from starting. An intake that
refuses the token surfaces as a bad gateway, not as the caller's login failure.

**Orca projects** publishes a repository to that runtime: the intake clones it
with the runtime's own repository access and registers it as an Orca project. No
agent starts. Orca pushes the project to every client paired with that runtime,
so every paired phone and desktop lists it. Those clients have full control of
the runtime: terminals, files and agent launches for all of its projects. Use a
separate runtime and pairing when projects need separate trust boundaries.

The page lists the console's published repositories and every other repository
the runtime holds (status **In Orca**). On **Sandboxes**, each row names its kind:
**Orca runtime** for the existing sandbox that runs the intake, **Coding sandbox**
for other existing sandboxes and **Project sandbox** for project environments.
Orca projects are repositories inside the runtime, so they have their own names.
The runtime's row lists the first six and a count, linked to **Orca projects**.
`HANDOFF_SANDBOX` names that sandbox by its sandbox-control id. Without it, the
console uses the only existing sandbox in the intake service's namespace
(`SERVICE.NAMESPACE.svc`). If neither matches, no row lists projects. A
`HANDOFF_SANDBOX` without `HANDOFF_URL` stops the controller from starting.

### Agent reports

A sandbox's **Overview** (under **Needs a decision**) and **History** list open agent reports from sandbox-control: an
agent's own statement that a denied resource stops its task. Kind, resource and
reason are agent-written. The console shows them as plain text without links,
and clamps their length. Dismissing a report changes no access; grant access
through the existing controls. Agents reach their report endpoint through the
sandbox broker, never through the console. Releases of sandbox-control without
reports hide the section. A report that names an allowlisted LAN host offers
**Open LAN access**, which opens the LAN devices section with that host
preselected; the agent still cannot open anything itself.

## Rollout

Apply the reviewed CRD from `config/crd` before updating the controller. Helm
does not update an installed CRD automatically. A controller that stores LAN host grants needs
the CRD with `spec.lanHosts`; with an older CRD the API server prunes the field
and the console reports that the CRD must be applied. The new schema permits HTTPS
changes only when both the old and new desired state are Paused; repository
changes and reopening Removed projects remain forbidden.

Use a digest-pinned controller image while retaining the existing workspace and
proxy image pins. Configure the backend connection and network route, then
verify login, project status, a disposable paused access update and an existing
sandbox form through the console. Merely opening a project or viewing a sandbox
does not approve a new grant. Pause now stops both worker and proxy processes.

After qualification, redirect the old dashboard hostname to the console's
`/sandboxes/` route while keeping its service available internally. Keep preview
and Git endpoints on their current routes. The chart does not retire existing
ingress routes or deploy the backend for you. A backend outage affects the
existing-sandbox section; project lifecycle controls continue to work.

Rollback to an older controller requires preserving compatible access policies
and the applied ledger. Do not restore the older immutable-policy CRD over
changed project requests or delete storage to recover the old UI.
