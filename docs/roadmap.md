# Glovecell roadmap

Goal: **grow the set of operators who can run Glovecell** without weakening
the security model. Do that by learning, on a **disposable test cluster**,
which dependencies are the real adoption blockers — then make those seams
**deliberately swappable** and **qualified**, not “works on my home lab.”

This is not a promise to support every CNI or Git host. It is a plan to
**measure** reach, then invest in the few axes that unlock the most users.

Related: [qualification](qualification.md), [installation](installation.md),
[security](security.md), [open-source packaging](../README.md) (product boundary).

## North star

| Today (qualified pilot) | Target |
| --- | --- |
| One known-good stack: Kubernetes 1.36.x, Agent Sandbox 1.0.4, **Cilium**, **gVisor**, **Forgejo**, Authentik-style OIDC | Same security *properties*, with a published **support matrix** of (network, runtime, Git host, IdP, storage) cells that passed the harness |
| Experimental `NETWORK_PROVIDER=network-policy` and `GIT_HOST=github` exist in code but are **not** release claims | Those (or better replacements) either **promoted** after harness evidence or clearly marked unsupported |
| Qualification is manual / narrative | A **matrix harness** installs Glovecell into ephemeral clusters and runs the same fail-closed suite across permutations |

**Hard rule:** the home / production network cluster is never the permutation
lab. Destroyable clusters only (kind, k3d, talos-in-VM, cloud throwaways).

## Phase 0 — Axes inventory (1–2 weeks)

List every external dependency an installer must already have, and score it:

1. **User reach** — how many otherwise-willing operators bounce because of this?
2. **Security surface** — does swapping it risk bypass of egress, isolation, or Git admin boundaries?
3. **Engineering cost** — interface already exists? (Cilium vs NetworkPolicy; Forgejo vs GitHub.)
4. **Testability** — can the harness stand it up and tear it down without human clicks?

### Candidate axes (starting set)

| Axis | Default today | Alternatives to evaluate | Notes |
| --- | --- | --- | --- |
| **Network enforcement** | Cilium + FQDN/DNS policy | Standard NetworkPolicy CNIs that actually enforce; (later) other eBPF CNIs with DNS policy | Highest leverage if true: many clusters have no Cilium. Fail-closed bypass tests are mandatory. |
| **Isolation runtime** | gVisor | Kata (documented interest, not qualified); plain runc **never** a “supported secure” cell | Measure who can enable RuntimeClasses. |
| **Git project host** | Forgejo | GitHub App mode (`GIT_HOST=github`) | Large user reach; different token/revoke model. |
| **Operator IdP** | Authentik (pilot) | Keycloak, Dex, other OIDC | Protocol is standard; qualify redirect/groups/session behaviour. |
| **Agent Sandbox version** | 1.0.4 pin | Newer upstream minors | Stay close to upstream; don’t fork. |
| **Kubernetes version** | 1.36.x | N−1 / N−2 | Preflight + CRD compatibility. |
| **Storage class** | Delete-reclaim provisioner | Common CSI classes | Removal must still erase data. |
| **DNS path** | Direct CoreDNS endpoints | Clusters with node-local DNS quirks | Already a footgun; harness must include a node-local case. |
| **Ingress / TLS front door** | Operator’s reverse proxy | Contour, nginx, Gateway API | Out of core claim unless it breaks OIDC Origin. |

**Deliverable:** a ranked table (reach × risk × cost) in this doc or
`docs/support-matrix.md`, with a recommended **first three axes** to automate.

**Decision (when ranking is done):**

- **A (recommended):** Automate the top **two** axes first (likely network + Git host), keep runtime/IdP as second wave.
- **B:** Network only until NetworkPolicy is a real release claim.
- **C:** GitHub host only (max SaaS-Git users, Cilium still required).

## Phase 1 — Disposable matrix harness (2–4 weeks)

Build `hack/matrix/` (name flexible) that:

1. **Provisions** a throwaway cluster per job (kind/k3d profiles, or a cloud
   ephemeral API). Never points at the home kubecontext.
2. **Installs** a fixed Glovecell chart digest + the axis under test (CNI,
   RuntimeClass, disposable Forgejo or GitHub App fixture, OIDC test IdP).
3. **Runs** a single scripted suite derived from
   [Required cluster qualification](qualification.md#required-cluster-qualification-before-release):
   - worker cannot reach API / inspection keys / other namespaces’ proxies  
   - direct egress and arbitrary DNS fail; approved HTTPS path works  
   - main protection holds; revoke stops credentials mid-flight  
   - controller restart mid-grant / mid-remove recovers without re-granting  
   - (downloads off by default; one optional job with downloads on)
4. **Records** pass/fail with exact versions + image digests as JUnit or JSON
   artifacts (CI-friendly).
5. **Destroys** the cluster always (`trap`).

### Harness principles

- **One changing axis per job** at first (combinatorial explosion later).
- Same **security assertions** on every cell; only the dependency install
  differs.
- Failures must distinguish **harness bug** vs **Glovecell gap** vs
  **dependency cannot enforce** (e.g. CNI that ignores NetworkPolicy).
- No production secrets: generate Forgejo admin, OIDC client, and GitHub App
  fixtures inside the job (or use ghcr/OIDC stubs).

**Deliverable:** CI workflow on GitHub Actions (or Forgejo Actions for private
dry-runs) that runs the default cell + at least one alternate network **or**
Git-host cell nightly.

## Phase 2 — Learn which seams to productize (ongoing with Phase 1)

From harness results, classify each axis:

| Class | Meaning | Action |
| --- | --- | --- |
| **Must-swap** | Large reach; enforcement still possible | Promote interface, docs, examples, release claim |
| **Nice-swap** | Moderate reach or high cost | Keep experimental; no release claim |
| **Don’t-swap** | Breaks the model (e.g. “egress = proxy env only”) | Document as unsupported; preflight refuse |

Update the public README “Install” section from “one pilot stack” to “supported
matrix” only for **Must-swap** rows with green harness evidence.

## Phase 3 — First-class swappable pieces (after evidence)

Order depends on Phase 0 ranking. Expected sequence unless data says otherwise:

1. **Network provider** — finish NetworkPolicy qualification procedure as
   automated harness jobs; only then call it supported on named CNIs.
2. **Git host** — GitHub App path through the same suite (protection refuse,
   token scope, revoke limits documented honestly).
3. **RuntimeClass** — Kata cell if isolation checks pass; never market runc as
   equivalent to gVisor/Kata.
4. **OIDC** — matrix of 2–3 IdPs for login/group/session tests.

Each promotion needs: Helm values example, preflight checks, harness job,
qualification paragraph, and a clear “unsupported” list.

## Phase 4 — Packaging and discoverability

Aligned with [OSS A](../README.md) (one product repo):

- Publish harness docs so outsiders can run one alternate cell locally.
- GHCR images + chart versioned with the matrix that passed.
- Optional: `glovecell preflight` / `glovecell qualify` CLI wrappers around the
  suite for operator machines (still against disposable clusters).

## Explicit non-goals (near term)

- Using the **home** cluster as a soak for permutations.
- Supporting every CNI that “has NetworkPolicy objects.”
- Multi-tenant SaaS control plane.
- Replacing Agent Sandbox or forking gVisor/Cilium.
- Claiming downloads, LAN grants, or hub-and-spoke on every matrix cell before
  those features have their own harness jobs.

## Milestones (sketch)

| Milestone | Done when |
| --- | --- |
| **M0** | Axes ranked; first-three axes chosen (decision A/B/C above) |
| **M1** | Harness creates/destroys kind (or equivalent) and passes **default** Cilium+gVisor+Forgejo cell |
| **M2** | Second cell green for the #1 alternate axis (likely NetworkPolicy **or** GitHub) |
| **M3** | That axis documented as supported for named versions; preflight encodes it |
| **M4** | Nightly matrix (default + ≥2 alternates); support-matrix page in docs |
| **M5** | Next alpha/beta release cites harness artifacts, not only the home pilot |

## How this relates to existing docs

- [qualification.md](qualification.md) stays the **assertion catalog**; the
  harness is how we run it many times.
- Experimental NetworkPolicy procedure becomes **jobs**, not only prose.
- Home-lab success remains necessary for *your* deploy, insufficient for
  *public support claims*.

## Open decisions for Steve

1. **Harness runner:** **A** GitHub Actions on `SkippySteve/glovecell` (public
   signal) / B private Forgejo Actions first / C both (private soak → public).
2. **First alternate axis:** wait for Phase 0 scoring, or force **A** network /
   **B** GitHub host now.
3. **Cluster backend for CI:** **A** kind on Actions / B k3d / C small cloud
   ephemeral (cost).
