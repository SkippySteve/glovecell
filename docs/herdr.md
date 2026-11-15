# Herdr-core in Glovecell

Android Herdr and the PC Herdr GUI talk to **herdr-core** (`hc`), not the
in-pod GUI and not the retired Herdr TUI.

## Shape

```text
Android Herdr ─┐                       glove-hub pod (chart, own namespace)
               ├─ SSH (LAN/WireGuard) ─▶ sshd ─ForceCommand─▶ herdr-core hub
PC Herdr GUI  ─┘                         │
                                         │ hc machine add --via \
                                         │   'agent-project-controller herdr-via <workspace>'
                                         ▼  (pods/exec, per-cell RoleBinding)
                                    Glovecell worker
                                    (hc server + agents)
```

Phone and PC connect to the same hub, so they see the same machines and
panes. Nothing runs on the operator's machines except the Herdr apps
(docs/in-cluster.md).

## What this repository ships

| Piece | Default | Role |
| --- | --- | --- |
| Workspace image `hc` / `herdr-core` 0.1.19 | Present, unused | Binary only |
| `HERDR_CORE_ENABLED` / chart `herdrCore.enabled` | `false` | Starts `hc server` in workers whose class is `herdr`; registers the adapter |
| OrchestratorClass `herdr` | Chart `enabled: false` | Opt-in per workspace |
| `agent-project-controller herdr-via <workspace>` | Always available | Hub `--via` pipe: exec a fixed `herdr-core connect` in the worker |
| ConfigMap `herdr-machine-<uid>` | When Ensure runs | Operator-readable via recipe |
| Chart `gloveHub` (image `glovecell-glove-hub`) | `enabled: false` | In-cluster hub: sshd + herdr-core + enroller |

### glove-hub

One Deployment in its own namespace (`<release namespace>-glove-hub` by
default) with two containers:

- `hub`: OpenSSH on port 2222 with `ForceCommand herdr-core connect`. Only
  the Ed25519/ECDSA keys in `gloveHub.authorizedKeys` are accepted, each with
  `restrict` (no TTY, no forwarding). The host key is generated on first start
  and kept on the hub's PVC.
- `enroll`: `agent-project-controller glove-hub-enroll` keeps the hub's
  machine list equal to the Ready workspaces of the `herdr` class. It only
  adds, changes or removes machines whose `--via` it wrote; hand-added
  machines are left alone.

Permissions:

- Read ProjectWorkspaces in the control namespace (Role).
- `pods/exec` only in project namespaces whose workspace uses the `herdr`
  class: the controller binds the `glove-hub-exec` ClusterRole there and
  deletes the binding when the workspace leaves the class. The ClusterRole is
  never bound cluster-wide.
- Network: default-deny; ingress only from `gloveHub.networkPolicy.sshFromCIDRs`
  on 2222 (none listed = no ingress); egress only to the Kubernetes API.

The Service is `ClusterIP` by default. Expose it on the home network with
`service.type: LoadBalancer` plus the annotations your load balancer needs
(for example a fixed LAN address). Never route it through a public ingress.

## Operator steps

1. Promote a workspace image and a controller build with this code, plus the
   `glovecell-glove-hub` image.
2. In a reviewed values change set `herdrCore.enabled=true`,
   `orchestratorClasses.herdr.enabled=true` and the `gloveHub` block
   (digest image, `authorizedKeys` from the phone and PC Herdr apps, Service
   and `sshFromCIDRs`).
3. Create or patch a disposable workspace with
   `spec.orchestratorClassName: herdr`; within `syncInterval` it appears in
   the hub (`hc machine ls` inside the hub container).
4. Phone and PC Herdr: add an SSH remote `agent@<hub address>:22` with the
   app's key and accept the host key on first use.

To switch it off, set `gloveHub.enabled=false`: the chart removes the hub, its
namespace and RBAC, and the controller stops creating per-cell bindings.

## Trust

Upstream herdr-core is not a sandbox: anyone who reaches its API gets that
account's shell. For glove-hub that means a holder of an authorized key can
act as the hub's uid 1000, including its ServiceAccount, which can exec into
`herdr`-class workers and read workspace objects. Treat those keys like
cluster credentials and keep the listener on LAN/WireGuard.

Isolation of the workers themselves stays with gVisor and the Glovecell
proxy/Cilium. `herdr-via` ignores the command line the hub sends and always
runs the same `env HOME=… XDG_RUNTIME_DIR=… herdr-core connect` in the worker,
so the hub cannot choose what runs there beyond the herdr-core protocol.
