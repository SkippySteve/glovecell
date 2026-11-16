# Agent Orchestrator in Glovecell

[Agent Orchestrator](https://github.com/OrchestratorInc/agent-orchestrator)
(AO, Apache-2.0) is a desktop and phone app for running many coding agents
at once: a kanban board of sessions, one git worktree per task, chat or the
agent's own terminal, and PR and CI routing. Its apps are clients of a Go
daemon (`ao daemon`). Glovecell runs that daemon inside each cell whose
workspace names the `ao` OrchestratorClass, so agents, worktrees and project
files stay in the gVisor cell, and the AO apps on a PC and a phone connect to
it.

## Shape

```text
AO desktop (Linux/macOS/Windows) ─┐
                                  ├─ HTTPS (LAN/VPN) ─▶ Gateway listener (TLS)
AO phone app (Android/iOS) ───────┘    https://<workspace>.<domain>
                                                 │ HTTPRoute + CiliumNetworkPolicy
                                                 ▼ (Traefik → worker :3011 only)
                                     Glovecell worker (gVisor)
                                     ao daemon: loopback API :3001 (never published)
                                                remote listener :3011 (device tokens)
                                     agents, worktrees, /workspace/repo
```

- One AO daemon per cell; each cell is one AO "remote host". Desktop and
  phone list the cells they paired, side by side.
- The daemon's remote listener runs in AO's LAN mode with
  `require-device-credentials on`: it accepts only per-device tokens, which a
  device gets by presenting a one-time pairing code.
- Not used: AO account sign-in, AO Cloud, AO's Cloudflare tunnel, and the
  pairing password. Nothing leaves the cluster to make pairing work.
- Agents run under the cell's normal policy: egress through the project
  proxy, model access through the cell's provider grants.
- Operator machines run only the AO apps ([in-cluster.md](in-cluster.md)).
  The AO desktop app starts its own local daemon for its window; that is part
  of the app, holds only the per-device tokens of the cells it paired, and
  Glovecell does not depend on it.

## Pairing a device (desktop or phone)

The console mints a one-time code for one cell:

```text
POST /api/v1/workspaces/<workspace>/ao-pairing      (OIDC operator session only)
```

The controller runs two `curl` calls against the daemon's loopback API inside
the worker (pods/exec through the controller's per-project exec binding),
checks that the remote listener is on, LAN-bound and refuses the password,
and returns:

| Field | Use |
| --- | --- |
| `address`, `code` | Desktop: **Settings → Remote hosts → Add host** (name, address, code). Phone: **Pair → Enter details manually**. |
| `pairingUrl` | `aomobile://pair#…`: open it on the phone to pair in one tap. |
| `pairingCode` | The same offer as a code to paste. |
| `expiresAt` | The code works once, within ten minutes. |

Responses are `Cache-Control: no-store`. Codes are never stored or logged;
the history records who minted a code for which cell, and when it expires.
`GET /api/v1/session` reports `aoPairing: true` when the endpoint is on.

To remove a device, run `ao remote-host devices` and
`ao remote-host devices revoke <ID>` in the worker (`agent-project-controller
connect <workspace>`). Removing the workspace deletes the daemon's data and
every device token with it.

## What this repository ships

| Piece | Default | Role |
| --- | --- | --- |
| Dockerfile target `ao-workspace` | Built and gated by CI; promotion off (`AO_WORKSPACE_PROMOTE`) | Workspace image plus AO 0.15.2 under `/opt/ao/resources` |
| `AGENT_ORCHESTRATOR_ENABLED` / chart `agentOrchestrator.enabled` | `false` | Registers the adapter; workers of the class run from `agentOrchestrator.image` and start `ao daemon` |
| OrchestratorClass `ao` (`projects.agentwork.dev/agent-orchestrator`) | Chart `enabled: false` | Opt-in per workspace |
| `agentOrchestrator.domain` + `gateway` | empty | Per-cell Service, HTTPRoute `<workspace>.<domain>` and an ingress policy for Traefik on port 3011 |
| ConfigMap `ao-host-<uid>` | When the cell is Ready | Operator-readable device address |
| `POST /api/v1/workspaces/{name}/ao-pairing` | Off without a domain | One-time pairing codes |
| Session hub slots (`/opt/ao/session-hub`) | Only in AO cells created with `spec.sessionHub.enabled` | Lend AO Chat harnesses to the hub's agents ([Session hub](#session-hub)) |

The image keeps AO's release layout because the daemon finds its bundled tmux
relative to its own path. AO's chat runtime (`@agentclientprotocol/claude-agent-acp`
0.70.0) pins five npm packages that have HIGH/CRITICAL fixes available; the
lockfile in `third_party/agent-orchestrator/acp-runtime/` raises only those,
and the build fails if AO's patched Claude adapter changes.

## Enable

1. Merge this code; CI publishes `glovecell-ao-workspace` by digest.
2. Give the deployment a promotion component for that image (or pin it by
   hand), then set `AO_WORKSPACE_PROMOTE=true`.
3. In a reviewed values change:

   ```yaml
   agentOrchestrator:
     enabled: true
     image: registry.example.com/glovecell-ao-workspace@sha256:…
     domain: ao.example.com          # *.ao.example.com covered by the listener's certificate
     gateway:
       name: my-gateway
       namespace: gateway-system
       sectionName: lan-https        # a LAN/VPN-only listener, never a public one
   orchestratorClasses:
     ao: {enabled: true}
   ```

4. Create a disposable workspace with `spec.orchestratorClassName: ao`. When
   it is Ready, mint a code and add the cell on the PC and the phone.

While `image` is still a `<repository>:unpromoted` placeholder (a deployment
switched on before CI promoted the first digest), the controller logs a
warning and keeps AO off; workspaces of the class are refused until then.

Switch off with `agentOrchestrator.enabled=false`: the class and its adapter
go away, so workspaces of the class are refused and suspended like any
refused class. Their volumes, and the daemon state on them, stay. Removing
such a workspace waits until the class is back (docs/orchestrators.md, "Life
cycle and removal"), so remove AO workspaces before switching off.

## Session hub

In a cell with a [session hub](session-hub.md), AO's Chat view can open the
hub's agents, so a chat started in AO also appears in the other apps attached
to the cell (T3 Code, Orca, Herdr).

**In plain words.**
- **Now:** AO runs chat agents itself, and nothing else can see those chats.
  AO 0.15 has no setting for adding your own chat agent: its Chat mode only
  offers a fixed list of built-in harnesses (Claude Code, Codex, Droid,
  DeepSeek Harness and others), and starts each from the program it finds on
  `PATH`.
- **Change:** in an AO cell created with `spec.sessionHub.enabled`, two of
  those built-in harnesses (*slots*) are lent to the hub's agents, in the
  order of the installation's `sessionHub.agents`: AO's **Droid** entry opens
  the first hub agent, **DeepSeek Harness** the second. With
  `agents: [cell-claude, cell-codex]`, Droid is the hub's Claude.
- **What you notice:** AO still labels the entries by the slot's name
  ("Droid"), not the hub agent's. Chats you start there are hub sessions:
  other apps list them, follow them live and can answer their permission
  questions. AO cells without a hub don't change.
- **Undo:** create the cell without `spec.sessionHub`, or switch the
  installation's `sessionHub.enabled` off.

**Words.** *Harness*: AO's name for an agent program it can run. *Slot*: one
built-in harness whose program Glovecell replaces in hub cells.

How it works:

- The `ao-workspace` image has one script per slot under
  `/opt/ao/session-hub/<slot>/` (`config/agent-orchestrator/session-hub-slot.sh`).
- A hub cell's workspace container gets the hub's allowlist at
  `/run/session-hub-client/agents.json`. Before the daemon starts, the worker
  reads the ids from it (only ids matching the hub's id pattern) and gives
  the slots to the first ones in order. For each, it sets
  `GLOVECELL_AO_HUB_<SLOT>=<agent id>` in the daemon's own environment and
  puts the slot's directory first on `PATH`. It also switches off AO's
  harness update checks and managed installs (`AO_HARNESS_UPDATE_CHECKS=off`,
  `AO_MANAGED_HARNESS_INSTALLS=off`), because a managed install would take
  precedence over `PATH` and silently replace the slot. None of this is in
  the pod spec, so an allowlist change is not drift. It applies when the
  worker next starts.
- When AO starts a slot for Chat, the script runs
  `agent-project-controller session-hub-acp --agent <agent id>` (see
  [Connecting an app](session-hub.md#connecting-an-app)). Any other use is
  refused with exit code 64: AO's terminal mode for that harness, version
  checks and model probes. An unmapped slot exits 127.
- Without the allowlist file, or with no usable id in it, the daemon starts
  exactly as in a cell without a hub.

Slots, checked against AO 0.15.2. Their Chat launch has no version check,
doesn't check the agent's name and writes no files into the repository:

| Slot | AO entry | Hub agent | AO's Chat launch the script accepts |
| --- | --- | --- | --- |
| `droid` | Droid | first in `sessionHub.agents` | `droid [--settings FILE] exec --output-format acp-daemon …` |
| `dsh` | DeepSeek Harness | second | `dsh --profile acp` |

A third or later hub agent has no AO slot. The other ACP harnesses check
versions (Cursor, Qwen, OMP, Hermes, Kimchi, OpenCode), validate the agent's
identity (Copilot, Pi, Agy), write files into the project (Kimi) or set
session modes the connector doesn't offer (Command Code). Claude Code and
Codex in AO are not ACP programs that can be swapped: AO runs Claude through
its own bundled adapter and Codex through Codex's app-server protocol. When
`AO_VERSION` moves, check the slots' launch arguments again.

Limits of AO 0.15.2, from its source (not yet checked in a live cell):

- **No list of hub sessions.** AO's board comes from its own database and
  never calls ACP `session/list`, so chats started in another app don't
  appear in AO. Chats started in AO appear everywhere else.
- **Live updates are partial.** While an AO chat is open, replies to turns
  started in another app stream in, but as one untitled reply, and the other
  app's prompt is not shown until AO reloads the history.
- **Pickers do nothing.** The hub connector offers no models or modes, so
  AO's model and permission choices for a slot have no effect. Permission
  questions still reach AO, and the first app to answer wins.
- The connector reads the cell's hub token from
  `/run/session-hub-client/token`; without it, starting a slot fails with
  "hub token unavailable".

## Trust

- AO's daemon is not a sandbox: a device with a token can run any command as
  the worker's uid in that cell. Isolation stays with gVisor, the project
  proxy and Cilium, as for every orchestrator.
- The Gateway terminates TLS; the hop from Traefik to the worker is plain
  HTTP inside the cluster network, carrying device tokens. Keep the listener
  on the LAN or VPN.
- AO's loopback API (port 3001) has no authentication. Nothing publishes it;
  only processes inside the worker and the controller's exec reach it.
- AO's PR and CI features use GitHub or GitLab through `gh`/`glab` in the
  cell; Forgejo repositories get sessions and worktrees but no PR board.
- AO releases often (several builds a week). Upgrade by changing `AO_VERSION`
  and `AO_DEB_SHA256` in the Dockerfile; the image gate rescans it.
