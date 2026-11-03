# Contribution workflow

Use a task branch. Commit changes, push to Forgejo and open a pull request after relevant checks. Do not merge without the maintainer's approval.

Keep the control backend in Go. Build on upstream Agent Sandbox APIs without forking its controller. Agent orchestrators and telemetry exporters are optional.

Do not include deployment-specific addresses, credentials or source from private infrastructure in reusable code. Do not grant project access or deploy to a live cluster as a side effect of unit tests. Qualification must use disposable environments and repositories.

Security changes need tests for denial and interrupted-operation recovery. Unsupported policies must fail closed. Removal must never report completion before external revocation and owned storage cleanup are verified.

## Forgejo author and reviewer identities

Create automation pull requests with a dedicated automation account. Before
creating a PR, verify that account's repository access and resulting PR author.
Reserve the maintainer account for reviews and merges; Forgejo rejects approval
by the PR author. Keep required approvals and merge restrictions enabled.
Maintainer-authorized review and merge operations use the maintainer identity,
not the automation author's token. Never copy either token into Git or logs.

## One merge per change

Steve merges once per change. Make one PR per change, code and tests together;
do not stack PRs unless unavoidable (say why). Do not open pin or promotion PRs
in steve/opnsense-kubernetes-bgp: after merge, this repository's CI builds the
images from main and adds them to the single rolling promotion PR there
(`promote/agent-project-controller`), which that repository's promote-merger
merges once promote-guard passes. Keep new behaviour compatible with the
deployed config so no companion config PR is needed; when one is, say so in the
PR, make the config PR safe to merge first, and link the two. See docs/one-merge-delivery.md in steve/opnsense-kubernetes-bgp.
