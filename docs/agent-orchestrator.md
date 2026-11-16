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
