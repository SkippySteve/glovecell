# Contribution workflow

Use a task branch. Commit changes, push to Forgejo and open a pull request after relevant checks. Do not merge without the maintainer's approval.

Keep the control backend in Go. Build on upstream Agent Sandbox APIs without forking its controller. Agent orchestrators and telemetry exporters are optional.

Do not include deployment-specific addresses, credentials or source from private infrastructure in reusable code. Do not grant project access or deploy to a live cluster as a side effect of unit tests. Qualification must use disposable environments and repositories.

Security changes need tests for denial and interrupted-operation recovery. Unsupported policies must fail closed. Removal must never report completion before external revocation and owned storage cleanup are verified.

## Prefer maintained open source over custom builds

Before writing a custom tool, service, script or controller, look for a
well-maintained open-source project that already does the job, and use or
integrate it when it fits the stack (Flux, Helm or Kustomize, the existing
observability and security tools) without adding a paid service or a recurring
chore for Steve. "Well maintained" means: releases within roughly the last six
months, more than one maintainer or a backing organisation, a security policy
or advisory process, real adoption, a licence compatible with this repo, and
images or releases that can be pinned by digest or checksum.

Keep custom code to thin glue (configuration, wiring, small checks) where
possible. When custom code is still the better choice, say why in the PR under
"Alternatives considered" (what was evaluated and why it didn't fit). Proposals
that replace existing custom code with such a project are welcome.

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
(`promote/glovecell`), which that repository's promote-merger
merges once promote-guard passes. Keep new behaviour compatible with the
deployed config so no companion config PR is needed; when one is, say so in the
PR, make the config PR safe to merge first, and link the two. See docs/one-merge-delivery.md in steve/opnsense-kubernetes-bgp.
## Explain proposals in plain language

Maintainers and users should understand what they are approving. Start every
proposal, plan, pull request description and request for a decision with a
short plain-language part, then give the technical detail:

- **Now:** what happens today, and the problem.
- **Change:** what it does in everyday terms, and what an operator would notice.
- **Why this way:** the reason, and the main alternative in one line.
- **Risk and undo:** what could go wrong, and how to reverse it.
- **Words:** define each tool, acronym or project name the first time it
  appears.

Assume a reader who knows general computing and security, but not this
project's components or history. Use concrete examples. For a deeper
walkthrough, use the `explain` skill (`.claude/skills/explain/SKILL.md`).
