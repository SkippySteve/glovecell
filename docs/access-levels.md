# Access levels

Every sandbox has one access control with named levels, from shut to wide open:

| Level | Allows | Stays blocked |
|---|---|---|
| **Locked** | The sandbox's own repository, model providers, read-only reference sources from Git | Every other web request |
| **Ask** | Locked, plus each exact request an operator approved | New requests, until approved |
| **Standard** | Locked, plus downloads from npm, PyPI, Go modules and crates.io, and GitHub pages, raw files, archives and release assets (read only) | Uploads, pushes and POST to those hosts; every other host |
| **Open** | Every public website, any method, for a limited time | Private networks, cluster services and cloud metadata, always; everything again when the window ends |
| **Custom** | Whatever the fine-grained editors list | Everything they do not list |

The console shows the levels as one control on a sandbox's **Internet** page. Each level says what it allows; choosing
one lists the exact rule changes it makes and asks for confirmation. Editing a rule in the fine-grained tables below it
makes the level Custom. Open always has a visible expiry, and **Lock now** stays on screen while anything is open.

## What the backends enforce today

Two backends serve sandboxes. A level is never stored on its own: it is read from what each backend enforces.

| Control | sandbox-control sandboxes (learning egress) | Project sandboxes (ProjectWorkspace) |
|---|---|---|
| Internet window | Locked, Learning or Open for 15 min, 1 h, 4 h or 24 h; applies on the next request | Not available (the project proxy has exact hosts only) |
| Exact approvals | Approve or revoke one method and URL; grouped by endpoint | Not available (no request log) |
| Rules | Web rules: path pattern, whole domain, `*.domain`, GitHub repo or owner, optional pass-through; expiry 1 d, 7 d, 30 d or permanent | Own HTTPS rules: exact host, methods, literal path prefixes; change only while paused. Profile rules (read only) change while running |
| Rules from Git | Reference sources, read only | Reference rules, read only |
| LAN | Allowlisted hosts (Git), opened for 15 min, 1 h or 4 h; restrict restarts the pod | `spec.lanHosts` (until removed) and LAN AccessGrants (timed); both apply while running |
| Packages | Registry downloads among the requests; package lists on the classic page | Registries through the Standard level; per-package grants not yet enforced |
| Credentials | Provider hosts; time-limited cluster grants (classic page) | Repository grants (controller-managed), brokered providers (OIDC operator) |
| Downloads and uploads | One-time, operator-approved, per file | Same, with `DOWNLOADS_ENABLED=true` |
| Levels offered | Locked, Ask, Standard, Open, Custom | Locked, Standard, Custom (Ask and Open shown disabled with the reason) |

Defaults: sandbox-control sandboxes take their policy profile from Git (Settings). Project sandboxes take the cluster
default AccessProfile (`locked` in the chart) when created; a project created before access profiles keeps only its own
rules, so upgrading never widens it.

## Kubernetes API

Two kinds in `projects.agentwork.dev/v1alpha1` (they move to `glovecell.dev` with the D2 rename):

- **AccessProfile** (cluster-scoped): a named preset. `spec.level`, `description`, read-only `https` rules,
  `registries`, `allowedGrants` (internet, lan, package), `maxGrantDuration` per grant type and `approveRequests`.
  The chart installs `locked` (cluster default, annotation `accessprofile.projects.agentwork.dev/is-default`), `ask`,
  `standard`, `open` and `custom` from `config/samples/accessprofiles.yaml` (`accessProfiles.install`).
- **AccessGrant** (namespaced, in the control namespace, owned by its cell): one time-limited widening, a union of
  `internet` (a window), `lan` (one IPv4 host and ports) or `package` (one package, optional version), with
  `spec.expiresAt`. The controller applies live grants, records `status.state` (Active or Refused, with a message),
  and deletes expired ones.

A cell (ProjectWorkspace) names its profile in `spec.accessProfileName`. Open is the `open` profile plus a live
internet grant; Custom is the `custom` profile with the cell's own rules and explicit grants; **Lock now** deletes
every grant of the cell, then sets the `locked` profile.

Enforcement on project sandboxes:

- Profile rules join the proxy policy as labelled read-only rules (`profile:<name>`) next to the reference rules. A
  profile switch or edit applies while the sandbox runs: network policy first, then the proxy policy, then a proxy
  restart so connections opened under a wider profile end. A missing profile blocks the sandbox.
- LAN grants join `spec.lanHosts` in the `lan-hosts` policy while the sandbox runs. Expiry narrows the policy first;
  the grant is deleted afterwards, so an interruption never leaves the wider policy.
- A grant is refused (never applied) when its profile does not allow its type, its expiry is beyond the profile's
  limit, its LAN address or ports are refused, it names another cell or is not owned by it. Internet and package grants
  are refused on project sandboxes until the project proxy can enforce them.
- Exact approvals and request history are not custom resources; they move to PostgreSQL behind an `ApprovalStore`.

Console API: `GET /api/v1/access-profiles`, `PATCH /api/v1/workspaces/{name}/access-level`,
`POST /api/v1/workspaces/{name}/lock`, `GET|POST /api/v1/workspaces/{name}/grants` and
`DELETE /api/v1/workspaces/{name}/grants/{grant}`. See [API](api.md).

### Admission policies

The CRD schemas enforce shape (levels, read-only profile methods, grant union, immutable cell and type). The chart adds
ValidatingAdmissionPolicies (`templates/access-admission-policies.yaml`, failure policy Fail, enabled with
`admissionPolicies.enabled`, LAN ranges from the `admission-params` ConfigMap):

- `access-grant-writer`: only the controller's ServiceAccount (or `system:masters`) creates or updates AccessGrants and
  their status; agent ServiceAccounts are refused even if RBAC allowed them.
- `access-grant`: `expiresAt` after creation and within `accessGrants.maxDuration` (internet 8h, lan 24h, package 720h,
  counted from `metadata.creationTimestamp` because CEL has no clock); an update may only move `expiresAt` earlier; the
  `projects.agentwork.dev/cell` label and exactly one ProjectWorkspace ownerReference name `spec.cell`; LAN addresses
  inside the allowlist, outside the denylist and never loopback, link-local, metadata, multicast, network or broadcast.
- `access-profile`: rules are read only (POST only to `git-upload-pack`, never `git-receive-pack`), paths are literal,
  only the open level allows internet grants and it must set their maximum duration.

Left to a webhook or the controller (CEL cannot do them): node addresses for LAN grants (the controller reads Node
objects), at most one default profile (needs a list), wildcard hosts on public suffixes (needs the public-suffix list),
and `spec.accessProfileName` naming an existing profile (needs a lookup; the controller blocks the cell otherwise).
