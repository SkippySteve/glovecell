# Orchestrator classes

An agent orchestrator is the tool an operator uses to drive agents in a
project sandbox: T3, Orca, or a custom command client. The controller connects
orchestrators to sandboxes through **orchestrator classes** and **adapters**,
the way Gateway API connects Gateways to implementations through a
GatewayClass and its `controllerName`.

- An `OrchestratorClass` is a cluster-scoped object. Its `spec.controllerName`
  names the adapter that serves it, and optional `spec.parameters` or
  `spec.parametersRef` hold adapter-specific settings.
- A `ProjectWorkspace` picks a class with `spec.orchestratorClassName`.
- An adapter is Go code in the controller, registered under one
  `controllerName`. The controller calls it to grant, pause and revoke the
  orchestrator's access to one workspace.

This is part of decision D12 (Kubernetes-native API).

## Words

- **Class**: an `OrchestratorClass` object.
- **Adapter**: the controller code behind a `controllerName`
  (`internal/orchestrator.Adapter`).
- **Registrar**: `agent-project-controller register --watch`, the process on
  the operator's control host that mounts Ready sandboxes and registers the
  mounts with desktop clients (`internal/registration`).
- **Fail closed**: when something is unknown or wrong, grant nothing.

## Built-in classes

| controllerName | What it does today | Needs |
| --- | --- | --- |
| `projects.agentwork.dev/t3` | T3 Connect: starts T3 in the sandbox and links it to the T3 account ([T3 Connect](t3-connect.md)). Phone and PC use the T3 account catalog. | `t3Connect.enabled` + **t3-controller** image |
| `projects.agentwork.dev/orca` | Hub-and-spoke when `HUBSPOKE_ORCA_ENABLED=true` ([hubspoke.md](hubspoke.md)): `orcad` in each gVisor spoke, hub enrolls for phone relay. Otherwise the legacy control-host registrar (client kind `orca`). Design: [project Orca runtime](design/project-orca-runtime.md). | hubspoke hub + `ORCA_RUNTIME_IMAGE`, or a registrar with Orca |
| `projects.agentwork.dev/herdr` | herdr-core in the worker; hub `--via` for Android/PC Herdr ([clients-choice.md](clients-choice.md); `docs/herdr.md` on APC #129). | `herdrCore.enabled` + workspace `hc` |
| `projects.agentwork.dev/command` | Custom command clients on the registrar (`APC_CLIENT_<KIND>_REGISTER/REVOKE`). | a registrar with the custom kind |

The Helm chart can create one class for each. All are off by default:

```yaml
orchestratorClasses:
  t3:      {enabled: false, name: t3}       # requires t3Connect.enabled
  orca:    {enabled: false, name: orca}
  command: {enabled: false, name: command}
```

The built-in adapters take no parameters. A class for them that sets
`parameters` or `parametersRef` is refused.

## Choosing a class

```yaml
apiVersion: projects.agentwork.dev/v1alpha1
kind: ProjectWorkspace
metadata:
  name: example
  namespace: agent-project-controller
spec:
  repository: operator/example
  desiredState: Running
  orchestratorClassName: orca
```

- `spec.orchestratorClassName` is **immutable once set**. The API server
  refuses changing or clearing it (a CEL rule on the field and one on the
  spec). It may be set later on a workspace that had none; set it at creation
  or while the workspace is paused, because the worker's pod template changes.
- **Empty means the deployment default.** With T3 Connect enabled that is T3,
  exactly as before classes existed: existing workspaces keep working, with
  the same Ready reasons and messages. With T3 Connect disabled it is none.
- A named class is resolved on every reconcile. The workspace is refused
  (phase `Blocked`, Ready `False`) when:

  | Ready reason | Cause |
  | --- | --- |
  | `OrchestratorClassNotFound` | no class with that name |
  | `UnsupportedController` | this controller has no adapter for the class's `controllerName` |
  | `InvalidParameters` | the adapter refused the class, or both `parameters` and `parametersRef` are set |
  | `OrchestratorClassUnavailable` | the class could not be read |

  The message says which class and why. A refused workspace gets nothing: the
  sandbox is suspended, every installed adapter's `Suspend` runs, and LAN and
  brokered provider grants are withdrawn, as for any refused policy.

## Class status

The controller sets condition `Accepted` on every class whose
`controllerName` starts with `projects.agentwork.dev/`. It is `True` when the
adapter is installed and accepts the class. Classes for other controllers are
left alone. Workspaces do not trust this status: they run the same check
themselves, so a stale condition never grants access.

```sh
kubectl get orchestratorclasses
NAME   CONTROLLER                    ACCEPTED   AGE
orca   projects.agentwork.dev/orca   True       2m
```

`spec.controllerName` is immutable; make a new class instead.

## Life cycle and removal

The controller calls the adapter as it always called T3 Connect:

- `Ensure` while the sandbox is Ready. An error keeps the workspace out of
  Ready (`OrchestratorPending`, or `T3ConnectPending` for T3) and is retried.
- `Suspend` whenever the workspace is not Ready, is pausing or is refused.
- `Remove` during removal, before the project namespace and volume are
  deleted. Removal does not finish until `Remove` returns nil.

Before the first `Ensure` for a named class, the controller writes the audit
record `orchestrator-bound`. If the class later disappears, removal waits until
it is restored and its adapter confirms revocation. A class that never got that
far granted nothing, so its removal does not wait. When a class is set on a
workspace that used the deployment default, removal revokes the default too,
and with T3 Connect disabled it still refuses unrevoked T3 history as before.

The registrar's own check is unchanged: the namespace is not deleted while the
`clients-<uid>` ledger shows a pending or registered client of any kind.

## Adding an orchestrator

A new orchestrator, for example the Herdr GUI app, is a new adapter plus a
class. The controller core does not change.

1. **Pick a controllerName** under a domain you control. Names starting with
   `projects.agentwork.dev/` belong to this project; a third-party adapter
   should use its own domain, for example `herdr.example.dev/gui`.
2. **Write the adapter.** Implement `orchestrator.Adapter`
   (`internal/orchestrator/orchestrator.go`):

   ```go
   type Adapter interface {
       Ensure(context.Context, *api.ProjectWorkspace) error
       Suspend(context.Context, *api.ProjectWorkspace) error
       Remove(context.Context, *api.ProjectWorkspace) error
       ControllerName() string
       RegistrationKind() string
       ValidateClass(*api.OrchestratorClass) error
   }
   ```

   - `Ensure` grants access for a Ready sandbox and returns an error until it
     is usable. It is called on every reconcile, so make it idempotent.
   - `Suspend` stops access. It runs often, including for workspaces the
     adapter never served, so make it cheap when there is nothing to stop.
   - `Remove` revokes access outside the cluster and returns an error until
     revocation is **verified**. Keep a durable record (a Secret or ConfigMap
     in the control namespace, labelled with the workspace UID) of anything
     you create, so a restarted controller can still revoke it.
   - `ValidateClass` refuses parameters you do not understand. Use
     `orchestrator.NoParameters` if you take none.
   - `RegistrationKind` names the registrar client kind, if the orchestrator
     is a desktop client the registrar drives.

   If the orchestrator is only a desktop client, you may not need Go code: run
   the registrar with a custom kind and use the `command` class.

   ```sh
   CLIENT_INTEGRATIONS=herdr \
   APC_CLIENT_HERDR_BINARY=/usr/local/bin/herdr \
   APC_CLIENT_HERDR_REGISTER='project,add,{path},--title,{title}' \
   APC_CLIENT_HERDR_REVOKE='project,remove,{id}' \
     agent-project-controller register --watch
   ```

   The `command` adapter's removal then waits until the registrar reports
   `herdr` revoked.
3. **Register it** in the controller's registry next to the built-ins
   (`cmd/agent-project-controller/main.go`):

   ```go
   reconciler.Orchestrators.Register(herdr.New(direct, ns))
   ```

   Registration refuses invalid and duplicate names.
4. **Create a class** naming it:

   ```yaml
   apiVersion: projects.agentwork.dev/v1alpha1
   kind: OrchestratorClass
   metadata:
     name: herdr
   spec:
     controllerName: herdr.example.dev/gui
   ```

   Ship it in your chart values or apply it yourself. Classes for a
   third-party `controllerName` get no `Accepted` condition from this
   controller; workspaces naming them are still served if the adapter is
   registered.
5. **Test** as the built-ins do (`internal/orchestrator`,
   `internal/controller/orchestrator_test.go`): resolution, refusal of bad
   parameters, `Ensure`/`Suspend`/`Remove`, removal waiting for revocation, and
   recovery after a failed or interrupted revocation.

## Orca today and later

The Orca adapter covers what exists now: the registrar's `orca` client kind
and the console's Orca project intake, which is per deployment, not per
workspace. `Ensure` and `Suspend` grant and hold nothing; `Remove` waits for
the registrar to revoke the Orca registration. The per-project Orca runtime
(orcad as the project worker, pairing, intake at :8791) is tracked in issue
#51; the TODOs in `internal/orchestrator/registrar.go` mark where it goes.
