# GitHub brokering for project sandboxes

Status: implemented behind configuration, inert by default. The proxy and
broker code are in `internal/githubpolicy`, `internal/proxy/github_broker.go`,
`internal/githubbroker` and `internal/controller/github.go`.

## In plain words

Agents in a project sandbox can push branches, open pull requests and issues
and comment on GitHub as the operator's own account, but they never hold the
operator's GitHub credential. A small broker outside every sandbox keeps the
credential. The project's egress proxy, which sees every request the sandbox
makes, adds the credential only to requests the project's GitHub policy
allows. Anything destructive (deleting or transferring a repository, changing
visibility or settings, pushing to the default branch) is refused at the
proxy. Creating a repository or a fork needs the operator's approval for the
exact name and visibility, and the approval works once.

What you notice: in a sandbox, `git push` to an allowed repository simply
works, `curl`/`gh api` calls to `api.github.com` work with any placeholder
token, and a refused action returns a GitHub-style JSON error that says why.

Risk: the proxy holds a live access token (up to 8 hours) for every opted-in
project, so a compromised proxy is a compromised token. Undo: remove
`spec.github` (one project), run `github-broker revoke` (everything), or
uninstall the GitHub App (last resort).

## 1. Identity

**Recommended: a GitHub App owned by the operator's account, used through
user access tokens** (user-to-server). Work then appears as the operator
"with" the App's badge, PRs are authored by the operator, and the App's
permissions cap what any token can do. Alternatives: a fine-grained personal
access token (long-lived, no refresh, no App-level cap), installation tokens
(act as `<app>[bot]`, cannot create repositories in a personal account), or a
second machine account (needs a classic token for most useful work).

What each token type can do, as far as GitHub's documentation was checked
(see "Verification" below for what was and was not confirmed):

| Capability | App user access token (`ghu_`) | App installation token (`ghs_`) | Fine-grained PAT (`github_pat_`) |
|---|---|---|---|
| Acts as | the user, shown "with <app>" | `<app>[bot]` | the user |
| Lifetime | 8 h; refresh token 6 months, rotated on every refresh | 1 h, mint again from the App key | 1–366 days or none |
| Scope | intersection of the App's permissions, the user's access and the repositories the App is installed on | the installation's repositories and permissions, can be narrowed per token | selected repositories and permissions |
| Create a repository in a personal account (`POST /user/repos`) | expected with repository permission **Administration: write** (unverified) | expected **no**: personal-account repositories cannot be created by an installation (unverified) | expected with Administration: write and "All repositories" (unverified) |
| `git push` over HTTPS | yes, token as the password (any user name; the proxy uses `x-access-token`) | yes, user name `x-access-token` | yes |
| Open pull requests and issues | Pull requests / Issues: write, on installed repositories only | same | own repositories (no upstream PRs) |
| Upstream repositories where the App is not installed | no | no | no ("cannot contribute to public repos where the user is not a member") |

If user access tokens cannot create personal repositories, the narrowest
working combination is: keep the App for everything else and let the operator
create repositories by hand (the approval flow then has nothing to approve),
or add a fine-grained PAT with only Administration: write, held by the same
broker and attached only to approved `POST /user/repos`. The policy code is
the same either way.

**Verification.** The rows marked "unverified" could not be re-checked while
writing this note: docs.github.com was refused by the sandbox's egress. The
lifetime, scope, push and "not installed" rows match GitHub's documentation as
read on 2026-10-24 (the deployment's `docs/github-contributions.md`, section
2). Confirm repository creation once, with a scratch repository, before
relying on it (operator step in the deployment docs).

## 2. Parts

```
GitHub App (operator's account)            operator: one device-flow sign-in
     ^ refresh (rotating), revoke                        |
github-broker (own namespace, no ingress)  <-------------+
     | state Secret: refresh token (never leaves)
     | lease Secret: access token, expiry capped at 5 min, renewed every 30 s
     v get (controller only)
controller --- per project with spec.github --->  proxy-ca/github-access  (lease)
                                                  proxy-ca/github-policy  (policy)
                                                  CNP "providers": proxy -> github.com, api.github.com
worker --(CONNECT, inspected TLS)--> project proxy --(credential attached if allowed)--> GitHub
```

- **Broker** (`agent-project-controller github-broker`). Holds the App's client
  secret (mounted file) and the user authorization in its state Secret. It
  renews the access token 30 minutes before expiry and publishes a lease capped
  at five minutes. It records "refresh started" before calling GitHub, keeps a
  successful answer in memory until stored, and publishes only stored tokens.
  If it crashes between GitHub's answer and the store, the spent refresh token
  is refused on the next try and the broker withdraws the lease and asks for a
  new authorization: it fails closed rather than guessing. It needs `get` and
  `patch` on its two Secrets, by name, and nothing else.
- **Controller.** With `GITHUB_LEASE_SECRET=<namespace>/<name>` it copies the
  lease (or `{"enabled":false}` if it is missing, malformed or within a minute
  of expiry) and the project's policy into the proxy-ca Secret of each project
  with `spec.github`, and opens `github.com` and `api.github.com` for that
  project's proxy pod only. Without the variable, `spec.github` is refused.
- **Proxy.** Rereads both keys on every request. Without `github-policy`
  nothing changes. With it, every request to the two hosts is classified by
  `internal/githubpolicy`; only allowed requests get `Authorization` (Basic
  `x-access-token:<token>` for git, Bearer for the API). Requests for other
  repositories go through the project's fixed rules without any credential.
- **Worker.** Never mounts proxy-ca and has no Kubernetes token. Tools use a
  placeholder; the proxy replaces whatever `Authorization` they send.

## 3. Policy (enforced at the proxy)

`spec.github`:

```yaml
spec:
  github:
    repositories: [owner/app, owner/*]   # exact or every repository of owner
    protectedBranches: [release/*]       # the default branch is always protected
    pushBranches: [agent/*]              # optional: confine pushes
    allowMerge: false
    allowReleases: false
    approvals: []                        # written by the console
```

| Request | Decision |
|---|---|
| git fetch/clone, push discovery (`info/refs`) on an allowed repository | allow |
| git push: create or update a branch that is not protected (and inside `pushBranches` when set) | allow |
| git push: any update of the default branch (looked up live at GitHub, cached 1 min) or of `protectedBranches` | **deny** (covers force-pushes to them) |
| git push: delete any branch; move or delete a tag; refs outside `refs/heads`, `refs/tags`; signed pushes; compressed pushes | **deny** |
| git push: create a tag | allow only with `allowReleases` |
| A push with any denied update | **deny the whole push** (git would apply the rest) |
| REST `GET`/`HEAD` under `/repos/<allowed>`; `GET /user`, `/rate_limit` | allow |
| Create/edit PRs (title, body, state, base), PR review comments, reviews (not `APPROVE`), review requests | allow |
| Create/edit issues, comments, labels, reactions | allow |
| Merge a PR (`PUT .../pulls/{n}/merge`) | **deny** unless `allowMerge` |
| `POST .../merges` (direct merge) | **deny** |
| Releases create/edit | **deny** unless `allowReleases`; release deletion always denied |
| Delete, transfer, rename, archive, visibility or other settings (`DELETE`/`PATCH /repos/{o}/{r}`, `/transfer`) | **deny** |
| Collaborators, invitations, webhooks, deploy keys, Actions (secrets, variables, runs), environments, Dependabot/Codespaces secrets, rulesets, branch protection, Pages, security settings | **deny** |
| Contents API and Git data API writes (would bypass the push rules) | **deny** |
| Any `DELETE` | **deny** |
| `POST /user/repos`, GraphQL `createRepository` | **approval**: exact name and visibility; a request without `private: true` counts as public |
| `POST /repos/{o}/{r}/forks` | **approval**: exact source and fork name; never into an organization |
| GraphQL queries | allow (see limits) |
| GraphQL mutations `createPullRequest`, `createIssue`, `addComment` | allow after resolving the node ID to an allowed repository |
| Any other GraphQL mutation, subscriptions, several operations or several mutation fields in one document, fragments at the mutation root, inline input objects | **deny** |
| Anything else on the two hosts for an allowed repository | **deny** |
| Requests for repositories outside the policy | not brokered: fixed rules, no credential |

Defence in depth on GitHub: a ruleset on every repository's default branch
(block deletion and non-fast-forward, require a pull request) with no bypass
actors. Note that a token with Administration: write could edit rulesets; the
proxy refuses `/rulesets`, so the ruleset protects against everything except a
stolen token used outside the proxy.

### Approvals

1. The agent's request (`POST /user/repos {"name":"x","private":true}`) is
   refused with HTTP 403 and
   `{"message":"…needs an operator approval…","approval":{"action":"create-repository","name":"x","visibility":"private"}}`.
   The proxy writes an `approval-required` audit record with the project.
2. The operator approves exactly that in the console API:
   `POST /api/v1/workspaces/{name}/github/approvals {"resourceVersion","action","name","visibility","source","minutes"}`
   (OIDC operators only, default 60 minutes, at most a day; immutable audit
   record). It lands in `spec.github.approvals`; the proxy sees it within a
   kubelet sync, without a restart.
3. The agent retries. The proxy matches the live approval, reserves it while
   the request is in flight, and marks it used when GitHub answers 2xx. A
   refused attempt (for example 422, name taken) leaves it usable; a second
   success is refused with 409. A proxy restart forgets "used", but GitHub
   refuses a second repository with the same name and the approval expires.

Follow-up once D12/D13 land: the proxy records the pending request as an
AccessRequest (target `api.github.com`, method, path, argument digest) so it
appears in the console's approvals and as a voice approval card, and approving
it writes the same `spec.github.approvals` entry.

## 4. How agents call GitHub

- **git** needs no configuration: the proxy attaches the credential, so git
  never sees a 401 and never prompts. Clone with `https://github.com/<owner>/<repo>.git`.
- **REST and GraphQL**: any client, any placeholder token, for example
  `curl -H 'Authorization: token placeholder' https://api.github.com/...`.
- **gh**: the real GitHub CLI works with `GH_TOKEN=<placeholder>` and the
  default host. `gh pr create`, `gh issue create`, `gh pr comment` and
  `gh repo create` are expected to use the brokered GraphQL mutations above or
  their REST equivalents, both handled (expected from gh's behaviour; its
  source could not be re-read while writing this, so check once). Flags that need
  other mutations (labels, reviewers, projects on `pr create`) are refused
  after the PR exists. The workspace image does not ship `gh` yet (a pinned
  release must be added with its checksum). Tools that are not the real CLI,
  such as a Forgejo `gh` shim, must not be pointed at github.com.
- An optional `X-Agent-Identity` header names the agent in audit records. It
  is a claim and is never forwarded.

## 5. Audit

- Every GitHub write, refusal and approval request: one structured proxy log
  line `github brokered request` with `project`, `project_uid` (set by the
  controller, not the sandbox), `agent_claim`, `account`, `action`,
  `repository`, `refs`, approval fields, `approved_by`, `verdict`
  (`allowed`/`denied`/`approval-required`/`completed`/`refused-by-github`/`interrupted`)
  and GitHub's status. Never the token, a request body or a response.
- Opt-in, policy changes, approvals and revocations: immutable audit ConfigMaps
  (the project's access history), with the OIDC operator as actor.
- Once the D13 store is available, both go to `audit_events`.

## 6. Revocation

| Scope | How | Effect |
|---|---|---|
| One project | Remove `spec.github` (console `DELETE /api/v1/workspaces/{name}/github`) | keys and host policy withdrawn on the next reconcile; the proxy restarts, so open connections close |
| Everything | `agent-project-controller github-broker revoke` in the broker pod | lease disabled first; then GitHub revokes the authorization (`DELETE /applications/{client_id}/grant`), which ends access and refresh tokens. If GitHub does not confirm, the command fails and keeps the tokens for a retry |
| Broker gone | stop or delete it | leases run out within five minutes |
| Last resort | uninstall or suspend the App on GitHub, or revoke it under "Authorized GitHub Apps" | GitHub refuses every token |

## 7. Limits

- The proxy cannot tell a fast-forward from a forced update without the
  object graph. It protects the default branch and `protectedBranches` by
  refusing every update to them; set `pushBranches` (for example `agent/*`) to
  keep agents off branches people work on, and use a GitHub ruleset with
  `non_fast_forward` for a second layer.
- GraphQL queries are allowed and read whatever the token can read: every
  repository the App installation covers, not just the project's list. Install
  the App on selected repositories where that matters.
- One authorization serves every opted-in project; the token in each proxy is
  the same and lives up to 8 hours at GitHub. Revoking at GitHub is the only
  way to end it early.
- `uploads.github.com` (release assets), LFS, `codeload`, `raw` are not
  brokered.
- The refresh token lives in a Kubernetes Secret. Moving it to an external
  secret store needs only a different state backend for the broker.

## 8. Tests

`internal/githubpolicy` (tables for REST, git routes, receive-pack parsing,
branch rules, GraphQL lexing and operations, approvals), `internal/proxy`
(`github_broker_test.go`, a fake GitHub over TLS: allowed push, default and
protected branch and force-push refusals, destructive API calls, approval
once, token never visible to the worker or the log, lease expiry, refresh and
revocation, interrupted push, inert without policy),
`internal/githubbroker` (device flow, rotation, interrupted refresh, revoke),
`internal/controller` and `internal/web` (opt-in only where configured,
revocation restarts the proxy, approvals without restart, OIDC-only API).
