# Control-host client registration (desktop workflow)

This is how **desktops** open Ready project sandboxes in Orca, T3 Code, Herdr,
or another Adapter—the same path for every orchestrator. Phones use
[hub-and-spoke](hubspoke.md) instead.

A long-running registrar runs **on the operator's desktop** (or an optional
privileged control-client pod). It mounts each Ready checkout over FUSE,
registers the local mount path with every configured Adapter, and revokes those
registrations when the project leaves Ready. In Orca, keep the Host on
**Local**; open the registered project. Do not pick the spoke as a remote
"Orca server" for day-to-day work.

The optional Helm control client is disabled by default. Rebuilding this source
does not authorize starting a registrar or deploying its privileged control pod.
This is not per-sandbox in-pod T3 (`agent-project-controller t3 WORKSPACE`).
Pairing and chat stay with the central client; the registrar only publishes and
withdraws project paths.

## Model

- One control client session; many sandboxes registered into it as they become Ready.
- Destroy/remove a project in the controller → registrar revokes the client entry, then drops the mount.
- One persistent project sandbox; many worktrees under the same authority boundary.
- Device pairing stays with the control client. Agent conversations are not transferred automatically.
- Worktree ownership is exclusive in the registration ledger. Visibility is not a claim.

## Where to run it

Run the registrar in the **control T3 pod**, next to the controller, in its own namespace. That pod is privileged so it can mount FUSE. It uses a service account named `registrar` that can read project workspaces, write client ledgers, and exec only into project namespaces the controller has granted. Project sandboxes stay on gVisor and do not receive this account.

Enable it with Helm `controlClient.enabled=true` and a `controlClient.namespace` that is not the control namespace. The chart sets `REGISTRAR_NAMESPACE` so each project namespace gets a `registrar-exec` Role limited to `pods` get/list/watch and `pods/exec` create. The controller first binds its worker exec ClusterRole (`WORKER_EXEC_CLUSTER_ROLE`) to itself in that namespace: Kubernetes lets it grant `pods/exec` only where it holds it. The workspace image should contain `/opt/t3/t3` built for that image's C library. A musl image cannot run a glibc T3 binary. Until the image includes T3, the state volume can hold `t3dist/`, the glibc libraries, and `bin/agent-project-controller`. The registrar mounts with `mount(2)` first, so the control pod does not need `fusermount`.

Phone and browser sessions pair with this control T3. A T3 process inside a gVisor sandbox cannot mount the checkouts.

```bash
# after the pod is Ready, from a machine with kubectl
kubectl -n "$CONTROL_CLIENT_NAMESPACE" exec deploy/control-t3 -- \
  /opt/t3/t3 auth pairing create \
  --base-dir /var/lib/control-t3/state \
  --base-url "https://YOUR_CONTROL_T3_HOST" \
  --ttl 10m --label "Control operator" --json
```

Open that one-time URL on the phone or browser within ten minutes. Keep the URL private. Publish `YOUR_CONTROL_T3_HOST` with `controlClient.routing` only after the gateway, DNS, and certificate for that name are ready. Do not point an existing sandbox preview hostname at this Service until you mean to replace that session.

## Transport

`agent-project-controller register --watch` holds a go-fuse mount per Ready workspace. Each mount is backed by a jailed `python3` helper over `kubectl exec` with roots `/workspace/repo` and `/workspace/worktrees` only. No in-pod SSH server and no sshfs.

Mount directory: `~/.local/share/agent-project-controller/mounts/<workspace>` (override with `APC_MOUNT_ROOT`).

## Modular clients (Adapters)

Integrations are pluggable. Builtins:

| Kind | Register | Revoke |
|------|----------|--------|
| `t3` | `t3 project add <mount> --title <name>` (adds `--base-dir` from `T3CODE_HOME` when set) | `t3 project remove <id>` |
| `orca` | `orca-ide repo add --path <mount> --json` | `orca-ide project setup-delete --setup <id> --json` |

T3 “already exists” is success. T3 “not found” on revoke is treated as already revoked. Do not pass `--force` to T3 remove.

### Custom tools

Add a named kind to `CLIENT_INTEGRATIONS` and supply argv templates (comma-separated; no shell). Placeholders: `{path}`, `{title}`, `{id}`, `{binary}`.

```bash
export CLIENT_INTEGRATIONS=t3,orca,mytool
export APC_CLIENT_MYTOOL_BINARY=/usr/local/bin/mytool
export APC_CLIENT_MYTOOL_REGISTER=project,add,{path},--name,{title}
export APC_CLIENT_MYTOOL_REVOKE=project,remove,{id}
```

Go packages can also call `registration.RegisterAdapter` with a custom `Adapter` implementation.

Unknown kinds and failed register/revoke leave the ledger `pending` and **block** project removal until fixed (fail closed).

## Ledger

A mutable ConfigMap in the control namespace named `clients-<workspace-uid-without-dashes>` stores registration acks (`pending`, `registered`, `revoked`) and worktree owners. The controller refuses cleanup-verified while any client is pending or registered, or if a state is unknown. A missing ledger does not block removal.

## Operator commands

On the control-client host:

```bash
CONTROL_NAMESPACE=agent-project-controller \
CLIENT_INTEGRATIONS=t3 \
agent-project-controller register --watch
```

Optional environment:

- `APC_MOUNT_ROOT` — mount parent directory
- `APC_T3_BINARY` / `APC_ORCA_BINARY` — client executables
- `CLIENT_INTEGRATIONS` — comma list such as `t3`, `t3,orca`, or custom kinds
- `APC_CLIENT_<KIND>_BINARY` / `_REGISTER` / `_REVOKE` — custom Adapter wiring

Empty `CLIENT_INTEGRATIONS` enables every builtin whose binary is set or found on PATH.

## Persistence

After a successful interactive `--watch` run, install a user or system unit **on the control host** (do not enable until qualified):

```ini
# ~/.config/systemd/user/agent-project-controller-register.service
[Unit]
Description=Register Ready sandboxes with control clients
After=network-online.target

[Service]
Type=simple
ExecStart=%h/bin/agent-project-controller register --watch
Environment=CONTROL_NAMESPACE=agent-project-controller
Environment=CLIENT_INTEGRATIONS=t3
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

While qualifying, run `register --watch` in the foreground or under `nohup` so mounts stay alive.

## Dashboard

`GET /api/v1/workspaces/{name}/clients` returns the ledger. The OpenUI project overview shows registered clients when a ledger exists. It
refreshes their state every ten seconds. Ledger errors remain visible.
