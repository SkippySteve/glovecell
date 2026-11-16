# Choosing Orca, Herdr, T3, or Agent Orchestrator for a cell

Steve wants the same day-to-day choice on phone and PC: pick **one** client per
workspace. All four keep project files inside the gVisor cell; they differ in
how the phone and desktop reach it.

| Client | OrchestratorClass | Phone | PC | Hub / account |
| --- | --- | --- | --- | --- |
| **Orca** | `orca` | Paired once to the Orca hub; new cells appear without a new scan | Orca desktop / Local mounts | In-cluster `orca-hub` + spoke dial ([hubspoke.md](hubspoke.md)) |
| **Herdr** | `herdr` | Android Herdr SSHs to the in-cluster glove-hub over Wi‑Fi/VPN | Herdr GUI on the same glove-hub | Chart `gloveHub` + `herdr-via` (`docs/herdr.md`) |
| **T3** | `t3` | Stock T3 app signed into the controller's T3 account | `app.t3.codes` or desktop T3 | T3 Connect catalog + managed HTTPS ([t3-connect.md](t3-connect.md)) |
| **Agent Orchestrator** | `ao` | AO app, paired once per cell with a one-time code from the console | AO desktop, **Remote hosts** (same code flow) | No hub or account: each cell's daemon at `https://<workspace>.<domain>` on the LAN/VPN ([agent-orchestrator.md](agent-orchestrator.md)) |

Set `spec.orchestratorClassName` when you create the workspace (immutable once
set). Prefer an explicit class over the empty default.

## Empty class (deployment default)

- With `T3_CONNECT_ENABLED=true`, an empty class still means **T3** (historical
  behaviour).
- With T3 Connect off, empty means none (Orca/Herdr only when named).

## What must be on for each choice

| Client | Controller flags / images | Cluster objects |
| --- | --- | --- |
| Orca | `HUBSPOKE_ORCA_ENABLED=true`, `ORCA_RUNTIME_IMAGE` | OrchestratorClass `orca`, orca-hub |
| Herdr | `HERDR_CORE_ENABLED=true`, workspace image with `hc` | OrchestratorClass `herdr` |
| Agent Orchestrator | `AGENT_ORCHESTRATOR_ENABLED=true`, `AGENT_ORCHESTRATOR_IMAGE` (target `ao-workspace`), `AGENT_ORCHESTRATOR_DOMAIN` + gateway | OrchestratorClass `ao` |
| T3 | `T3_CONNECT_ENABLED=true`, controller image = **t3-controller** target (`/opt/t3/t3` + cloudflared), PVC `t3-controller-account` | OrchestratorClass `t3` |

Orca and Herdr can stay enabled while T3 is on: the class on each workspace
selects the path. Do not flip T3 Connect on until the t3-controller image is
promoted and the account PVC is authorized (`t3 connect login` in
[t3-connect.md](t3-connect.md)).

Main CI builds, gates and pushes the t3-controller image by digest. The
promotion PR step stays off (repository variable `T3_CONTROLLER_PROMOTE`)
until the deployment repository's `promote.py` has a `project-t3-controller`
component and a Deployment that pins it. cloudflared is built from its pinned
upstream tag with `golang.org/x/net` v0.62.0, because the 2026.10.7 release
binary fails the image gate (CVE-2026-78661).

## Phone / VPN posture

- **Orca / Herdr:** LAN or WireGuard to the hub (no public SSH for cells).
- **Agent Orchestrator:** LAN or WireGuard to a TLS Gateway listener; the
  controller routes `<workspace>.<domain>` to that cell's AO port only.
- **T3:** devices talk to T3's managed HTTPS endpoints (Cloudflare); Tailscale
  on the controller dashboard is optional and separate from environment
  registration.
