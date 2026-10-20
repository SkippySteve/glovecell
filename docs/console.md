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

## Preparation errors

Each project shows the current Ready condition next to its phase. A preparation,
policy or cleanup error appears as an attention panel with the actual message.
Condition changes are also retained in access history. Repeated retries of the
same condition do not create duplicate records.

For example, an existing Forgejo `main` rule with zero required approvals produces
`require at least 1 approval (currently 0)` and directs the operator to Settings
> Branches. Fix that rule on Forgejo. The controller retries the same request;
deleting and recreating the project is unnecessary. The dashboard never rewrites
existing branch protection to make preparation pass.

## Project access

Open a sandbox and select its **Access** tab to inspect its HTTPS grants. Add initial
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

### Agent reports

A sandbox's **Activity** tab lists open agent reports from sandbox-control: an
agent's own statement that a denied resource stops its task. Kind, resource and
reason are agent-written. The console shows them as plain text without links,
and clamps their length. Dismissing a report changes no access; grant access
through the existing controls. Agents reach their report endpoint through the
sandbox broker, never through the console. Releases of sandbox-control without
reports hide the section.

## Rollout

Apply the reviewed CRD from `config/crd` before updating the controller. Helm
does not update an installed CRD automatically. The new schema permits HTTPS
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
