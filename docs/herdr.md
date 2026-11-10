# Herdr-core in Glovecell

Android Herdr and the PC Herdr GUI talk to **herdr-core** (`hc`), not the
in-pod GUI and not the retired Herdr TUI. Design in the infra repo:
`docs/glovecell/herdr-core.md`.

## Shape

```text
Android Herdr  --SSH / Wi‑Fi or WireGuard-->  hub herdr-core (Steve's PC)
                                              |
                                              | hc machine add --via \
                                              |   'agent-project-controller herdr-via <workspace>'
                                              v
                                         Glovecell worker
                                         (hc server + agents)
```

The PC Herdr app uses the same hub core, so phone and desktop see the same
machines and panes.

## What this repository ships

| Piece | Default | Role |
| --- | --- | --- |
| Workspace image `hc` / `herdr-core` 0.1.19 | Present, unused | Binary only |
| `HERDR_CORE_ENABLED` / chart `herdrCore.enabled` | `false` | Starts `hc server` in workers whose class is `herdr`; registers the adapter |
| OrchestratorClass `herdr` | Chart `enabled: false` | Opt-in per workspace |
| `agent-project-controller herdr-via <workspace>` | Always available | Hub `--via` pipe: kubectl exec → `hc connect` |
| ConfigMap `herdr-machine-<uid>` | When Ensure runs | Operator-readable via recipe |

No public Traefik SSH route. Reach the hub over LAN or WireGuard only.

## Operator steps (after enablement)

1. Promote a workspace image that contains `hc` and a controller build with
   this code; set `herdrCore.enabled=true` and
   `orchestratorClasses.herdr.enabled=true` in a reviewed values change.
2. Create or patch a disposable workspace with `spec.orchestratorClassName: herdr`.
3. On the PC hub: install Herdr + herdr-core; keep WireGuard up.
4. `hc machine add --name <cell> --via 'agent-project-controller herdr-via <workspace>'`
   (kubeconfig that can exec the worker, same class as `connect` / T3).
5. Android Herdr: SSH to the PC hub; open the cell. PC GUI: same hub.

## Trust

Upstream herdr-core is not a sandbox: anyone who reaches its API gets that
account's shell. Isolation stays with gVisor + Glovecell proxy/Cilium. The via
helper refuses shell metacharacters and only runs an `hc` / `herdr-core`
connect line.
