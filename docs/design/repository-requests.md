# Design: agents request new repositories

**Status:** design, 2026-11-22; questions RQ1–RQ3 decided (section 11). Docs only; nothing here is built yet.
Decision record: [D17](decisions.md#d17--agents-request-new-repositories-the-operator-approves-2026-11-22).

**Related:** [glovecell.md 4.6](glovecell.md#46-blocked-reports) (blocked
reports), [network.md 5.5 and 7](network.md#7-building-order-proposed) (cell
tools endpoint), [port-tunnels.md](port-tunnels.md) (the same request, card and
approval pattern), [repository-enrollment.md](../repository-enrollment.md),
[in-cluster.md](../in-cluster.md).

## Now / Change / Why this way / Risk and undo / Words

- **Now:** an agent that needs a new repository (say, to move an app's image
  source out of a larger repository) cannot get one. It can describe the need
  in text, and the operator then creates the repository by hand, adds the CI
  secrets the image workflow needs, adds the bot accounts as collaborators,
  sets branch protection and reruns the workflow. Every new repository repeats
  those steps, and a forgotten one shows up later as a failed CI run.
- **Change:** the agent runs
  `glovecell request repository price-watch --template image-app --reason "move the price-watch image out of the platform repo"`.
  The operator's phone gets a short notification ("cell `platform` asks for
  repository `apps/price-watch`"). The operator opens the console, reads the
  approval card and presses **Approve** or **Deny**. On approval a small
  provisioner pod creates `apps/price-watch` from the template repository, with
  CI secrets and bot access already in place (inherited from the
  organization), branch protection on, and task-branch write for the cell that
  asked. The agent's command, which waited, prints the new repository's URL and
  the agent continues.
- **Why this way:** it reuses three things that exist: Forgejo's own
  organization secrets, teams and template repositories (so the repository is
  complete the moment it exists), the console's approval cards, and the forge
  agent's pattern of keeping a Forgejo token in its own narrow pod. The main
  alternative, declaring repositories as code and applying them with OpenTofu,
  is in section 9.
- **Risk and undo:** the provisioner's token can create repositories in one
  organization and nothing else. A new repository can read the organization's
  CI secrets, so those secrets must be ones every repository in the
  organization may hold (section 7). A new repository deploys nothing by
  itself: getting its images into a cluster stays a separate, reviewed change
  in the deployment repository. Undo: set `repositoryRequests.enabled: false`;
  repositories already created stay until the operator deletes them.
- **Words:**
  - *Cell*: one project sandbox with its own gateway (D14).
  - *Organization* (Forgejo): a group account; its Actions secrets and teams
    apply to every repository it owns.
  - *Template repository*: a Forgejo repository marked as a template; "generate
    from template" copies its files into a new repository.
  - *Provisioner*: the new pod that holds the repository-creation token.
  - *ntfy*: a self-hostable push-notification server with Android and iOS apps.
  - *Per-pod token*: the secret the gateway gives only its own sandbox pod, so
    a call to the gateway's local endpoint proves which cell made it.

## 1. What exists today, and the gap

| Piece | Exists | Gap |
|---|---|---|
| Approval cards and the approver group | yes, for web requests (`access-requests`) and LAN grants | requests carry a web `Target` only |
| A way for the agent to ask | no; the cell tools endpoint is proposed (network.md 7, step 2) | needed by blocked reports, previews and this |
| Forgejo token outside the controller | yes, the forge agent (Discover, Grant, Revoke) | its token is an administrator token; creating repositories should use a narrower one |
| Branch protection setup | yes, `internal/forgejo` `ensureProtection` | reuse as is |
| One configured repository owner | yes | cells cannot work on a repository of a second owner (open question RQ1) |
| Notifications for pending requests | no | needed for every request kind, not only this one |

## 2. Shape

```text
sandbox pod                       cell gateway pod               controller (console)
 glovecell request repository  -> /glovecell/v1/requests      -> repository request
   --wait                          (per-pod token, local only)    (PostgreSQL, pending)
                                                                   |   \
                                                                   |    \-> notifier: one POST to
                                                                   |        the configured ntfy topic
                                                                   v
                                                         approval card (OIDC approver)
                                                                   |
                                                                   | approve: projected SA token,
                                                                   | audience "glovecell-repo-provisioner"
                                                                   v
                                                         repo-provisioner pod (own SA)
                                                          token: create in one organization
                                                                   |
                                                                   v
                                                         Forgejo: generate from template,
                                                         protect main and task/*, grant the
                                                         cell's identity task-branch write
```

## 3. Flow

1. **Request.** The agent runs
   `glovecell request repository <name> --template <template> --reason <why> [--wait]`.
   The CLI posts to the gateway's local endpoint with the per-pod token; the
   gateway adds the cell's identity and forwards to the controller over the
   gateway's authenticated channel to the controller (mTLS, network.md 5.5). The agent cannot choose the organization,
   visibility, owner or permissions: those come from configuration.
2. **Checks before anything is shown.** Name: a lowercase DNS label of at most
   40 characters, not a configured reserved name, not an existing repository
   (checked through the provisioner's read-only `Exists` call). Template: one
   of the configured templates. Reason: one printable line, at most 500
   characters, treated as untrusted text. Limits: 3 pending repository
   requests per cell, 10 created per cell per day. A repeat of a pending
   request updates its reason and count, like a blocked report.
3. **Notification.** The controller sends one POST to the configured ntfy
   topic (section 6). Text and console link are built by the controller; the
   agent's reason is not sent.
4. **Approval card** in the console, for members of the approver group only:
   - **Observed by Glovecell:** the cell, its repository, the requesting
     identity (agent session if known), the time, and how many times it was
     asked.
   - **Reported by the agent:** the reason, shown as quoted plain text.
   - **What approval does:** the full new name (`apps/price-watch`), private,
     the template and its current commit, the inherited organization secrets
     by name (never values) and teams, the protection rules, and "task-branch
     write for cell `platform`".
   - **What approval does not do:** "Nothing is deployed. Images from this
     repository reach a cluster only after a reviewed change in your
     deployment repository."
   - Buttons: **Approve**, **Deny** (with an optional reason the agent sees).
5. **Provision.** On approval the controller writes the decision and an audit
   record first (if it cannot, nothing happens; 503), then calls the
   provisioner (section 5). The request moves to `creating`, then `created`
   with the URL, or `failed` with a reason.
6. **Answer.** `--wait` polls the gateway and prints the result: the clone URL,
   or the denial reason, or the failure. Without `--wait` the agent can run
   `glovecell request status <id>`. The cell can push `task/*` branches to the
   new repository as soon as the grant is applied; the controller adds it to
   the cell's repository list the same way an enrolled repository is added.
7. **Deny.** The request ends; a new request for the same name within 24 hours
   is refused with the denial reason, so an agent cannot ask again in a loop.

## 4. The request channel

This is the first tool on the cell tools endpoint that network.md step 2
proposes, built in its smallest form:

- An HTTP endpoint on the gateway pod, reachable only from that cell's sandbox
  pod (network policy), authenticated by the per-pod token. Blocked reports
  (glovecell.md 4.6) and preview requests (port-tunnels.md 4.1) move onto the
  same endpoint later.
- The gateway forwards each call to the controller with the cell's identity
  attached; the controller never trusts a cell name from the request body.
- Plain HTTP and JSON first, with the same operations later exposed as MCP
  tools (network.md 5.5).
- Unsupported calls return 404; the endpoint fails closed when the controller
  is unreachable (the request is refused, not queued in the gateway).

## 5. The provisioner pod

A Deployment in the chart, like the forge agent: its own ServiceAccount (no
Kubernetes API rights beyond TokenReview), its own Secret, a read-only root
file system, non-root, egress only to Forgejo's HTTP port and DNS, ingress only
from the controller.

It serves three calls, each checked against a projected ServiceAccount token
whose audience is `glovecell-repo-provisioner` and whose subject is exactly
the controller's ServiceAccount:

| Call | Does | Refuses |
|---|---|---|
| `Exists(name)` | reports whether `<org>/<name>` exists | names outside the pattern |
| `Create(name, template, identity)` | generates `<org>/<name>` from `<org>/<template>` (`git_content` only: no webhooks, no git hooks, no topics), private, default branch `main`; applies main and `task/*` protection with `ensureProtection`; grants `identity` (an `apc-`/`apcp-` workspace identity) write | an existing name; a template not in the configured list; an organization other than the configured one; an identity outside the workspace pattern |
| `Status(name)` | reports protection and the grant, for recovery | |

Interrupted operations: `Create` is idempotent on a repository the
provisioner itself created for the same request ID (recorded in the
repository description as `glovecell-request:<id>`); it completes missing
protection and grants, and never touches a repository without that marker.
The controller retries a request left in `creating` after a restart.

**Its token.** A dedicated Forgejo user, member of one team of the
organization with "create repositories" permission and no other access. By
Forgejo's team rules it becomes administrator of each repository it creates,
which it needs for branch protection; it has no access to other organizations
or users, cannot change organization settings, secrets or teams, and cannot
merge (it is not on any merge allowlist). The controller never holds it.

## 6. Notifications

One notifier for every kind of pending request (web, package, LAN, repository,
preview), configured once:

- `notifications.ntfy.url` and `notifications.ntfy.topic`, an optional token
  from a Secret, and `notifications.consoleURL`.
- One POST per new pending request; a repeat of a pending request does not
  notify again. At most 20 notifications per hour per installation; past
  that, one summary line.
- The text is built by the controller from observed fields only: kind, cell,
  the requested resource, and a link to the console's request page. It never
  contains the agent's reason, tokens or approval codes.
- Approval happens only in the console after OIDC sign-in, never from the
  notification (no action buttons).
- A failed notification is logged and counted
  (`glovecell_notifications_failed_total`); the request is still shown in the
  console.

Built as described in [notifications.md](../notifications.md).

## 7. Configuration

```yaml
repositoryRequests:
  enabled: false                 # off by default
  organization: apps             # where new repositories go
  templates: [image-app]         # repositories <organization>/<template> marked as templates
  reservedNames: []
  limits: {pendingPerCell: 3, createdPerCellPerDay: 10}
  provisioner:
    existingSecret: glovecell-repo-provisioner   # key: token
notifications:
  ntfy: {url: "", topic: "", existingSecret: ""}
  consoleURL: ""
```

The organization's own setup is outside Glovecell and done once by the
operator: create the organization; add its Actions secrets; add its teams
(bots with "all repositories" access); create the template repository; create
the provisioner's user and team. Every repository in the organization can read
the organization's Actions secrets from any workflow on any branch, so put
there only secrets that every repository in it may hold.

## 8. What stays outside Glovecell

Deploying. A created repository can build and publish images with the
organization's secrets, but whatever promotes images into a cluster (in the
reference deployment, a reviewed change that names the repository as an
allowed image source) stays a separate decision by the operator.

**Clients that are not cells** (an orchestrator running outside Glovecell)
cannot call the cell tools endpoint. For them, the console's **New repository**
button creates the same request on the operator's behalf: same checks, same
card, same provisioner; the requester is recorded as the signed-in operator,
and the identity to grant is chosen from the enrolled ones.

## 9. Build or adopt

| Option | What it is | Verdict |
|---|---|---|
| Forgejo organization secrets, teams and template repositories | built into Forgejo | **adopted**: they make a new repository complete without custom code |
| OpenTofu with a Forgejo provider, applied by tofu-controller under Flux | repositories declared as code | not now: adds a controller and a provider whose maintenance could not be confirmed, and an agent's request still needs a human approval in between; can replace the provisioner later, since both end in the same Forgejo state |
| Backstage scaffolder | a portal with templates that create repositories | not adopted: a large portal and database for one action, and its approval model would duplicate the console's |
| The forge agent with a wider token | reuse the existing pod | not adopted: its token is an administrator token; repository creation gets the narrowest token instead |

Custom code left: the provisioner's three calls (about 200 lines over
`internal/forgejo`), the request kind in the store, one card, the CLI command
and the notifier.

## 10. Build plan

Each step is one PR with its tests (denial and interrupted-operation
recovery for steps 3 and 4).

1. This design (docs only).
2. **Notifier** for pending web, package and LAN requests (section 6). Useful
   on its own.
3. **Repository request kind, provisioner pod and card**, off by default:
   store schema and migration, the three provisioner calls with fakes and an
   integration test against a disposable Forgejo, chart Deployment, Service,
   ServiceAccount and network policy, the console card and the **New
   repository** button. `docs/components.json` gains the provisioner.
4. **Cell tools endpoint** with `request repository` and `request status`, and
   the `glovecell request repository … --wait` CLI in the workspace image.
5. Deployment repository (outside this repository): values, the provisioner's
   Secret and its credential inventory entry, network policy, switch-on.

## 11. Decided questions

The maintainer chose these on 2026-11-22:

- **RQ1 A. Cells on repositories of more than one owner.** The installation
  accepts a list of owners (`forgejo.owners`), with the forge agent's checks
  applied per owner, so a cell can work on a repository the provisioner
  created in the organization. (Rejected: one owner, with existing
  repositories moved into it.)
- **RQ2 A. Who approves:** the existing approver group. (Rejected: a separate
  group.)
- **RQ3 A. Never public.** New repositories are always private; making one
  public stays a manual operator action outside Glovecell.

Build order is unchanged (section 10); RQ1 lands with step 3.
