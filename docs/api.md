# API

The dashboard and HTTP API use the same project requests as direct Kubernetes clients. The API currently has one operator role, one configured repository owner and no tenant subdivision.

## Authentication

Browser users sign in at `GET /auth/login`. The OIDC callback is `GET /auth/callback`. `POST /auth/logout` invalidates the local session and requires a same-origin JSON request. Sessions are opaque cookies, not exposed OIDC tokens.

Automation can use an explicitly configured bearer secret. OIDC access tokens are not accepted as arbitrary bearer credentials. API responses do not include Forgejo or provider credentials.

## Routes

- `GET /api/v1/session` reports authentication mode and caller identity.
- `GET /api/v1/repositories` discovers repositories under the configured Forgejo owner.
- `GET /api/v1/workspaces` returns requests and observed status in the control namespace.
- `POST /api/v1/workspaces` creates a request with `name`, `repository`, optional `registries` (`go`, `npm`, `python`, `rust`) and optional `https` rules. Names are lowercase DNS labels up to 40 characters. An unknown or repeated registry, or a host listed twice, returns 400.
- `PATCH /api/v1/workspaces/{name}` sets `desiredState` to `Running`, `Paused` or `Removed`. Include the current `resourceVersion`; a stale request returns 409.
- `PATCH /api/v1/workspaces/{name}/access` replaces optional HTTPS policy with `https`, optional `registries` and current `resourceVersion`. It requires observed Paused state, an applied previous policy and no project pods. Saving leaves the project paused. Stale, running or unconverged requests return 409.
- Access levels ([access levels](access-levels.md)). `GET /api/v1/access-profiles` lists the AccessProfiles with their expanded read-only rules, allowed grant types, limits (`maxGrantMinutes`), the cluster default and whether project sandboxes support the level (`projectSupported`, `reason`). `PATCH /api/v1/workspaces/{name}/access-level` with `{"resourceVersion", "profile"}` sets `spec.accessProfileName`; it applies while the project runs, except that a profile other than custom also removes the project's own HTTPS rules and then needs the project Paused (409 otherwise). Ask and open profiles and unknown names return 400. `POST /api/v1/workspaces/{name}/lock` with `{"resourceVersion"}` deletes every AccessGrant of the project, then sets the locked profile; it answers with the own rules and LAN hosts that remain. `GET /api/v1/workspaces/{name}/grants` lists AccessGrants; `POST` with `{"type": "lan", "minutes", "lan": {"address", "ports", "note"}, "reason"}` creates one (signed-in OIDC operator only; the profile must allow the type and duration; internet and package grants return 400 on project sandboxes); `DELETE .../grants/{grant}` removes one. Every change writes an immutable audit record first; if it cannot, nothing changes (503). `POST /api/v1/workspaces` accepts `accessProfile` and otherwise uses the cluster default profile.
- `POST /api/v1/workspaces/{name}/lan-hosts` grants the project's worker direct TCP to one LAN host: `{"resourceVersion": "...", "address": "192.168.1.20", "ports": [22], "note": "optional"}`. `DELETE /api/v1/workspaces/{name}/lan-hosts/{address}` with `{"resourceVersion": "..."}` removes one grant. Both need a signed-in OIDC operator; the automation bearer and static login get 403. They work while the project runs. Refused addresses or ports return 400 with the reason, a disabled feature returns 403 on add, and a stale `resourceVersion`, duplicate address or full project returns 409. `GET /api/v1/session` reports `lanHosts` and the allowed ranges in `lanAllowed`. See [LAN hosts](console.md#lan-hosts).
- With `DOWNLOADS_ENABLED=true`, "Download into sandbox" (see [console](console.md#download-into-sandbox)). Every download route, reads included (they return full URLs, whose query strings can hold signed-link tokens), needs a signed-in OIDC operator; the automation bearer and static login get 403. The project must be Running and Ready.
  - `POST /api/v1/workspaces/{name}/download-checks` with `{"url": "https://..."}` starts a redirect-chain check and returns 202 with the check: `id`, `state` (`checking`, `ready`, `failed`), `hops` (each `url`, `redacted`, `host`, `status`, `crossHost`, `queryHash`, `contentLength`, `contentType`, `probe`, `refused`), `digest` once ready, `error`, `defaultDestination`. `GET .../download-checks/{id}` polls it. Checks are kept 15 minutes. An invalid URL returns 400; another operation in the same project returns 409.
  - `POST /api/v1/workspaces/{name}/downloads` with `{"checkId", "digest", "destination", "sha256"?, "overwrite"?, "acceptQueryChanges"?}` approves exactly that checked chain and returns 202 with the download: `state` (`starting`, `downloading`, `done`, `failed`, `changed`, `cancelled`), `bytes`, `total`, `sha256`, `path`, `error`, `code`, `change` (`hop`, `expected`, `observed`, `queryOnly`, `reason`), `followed`. A check is used once; a different digest, an expired or used check returns 409. `GET .../downloads[/{id}]` reads recent downloads (kept in memory, 20 per project). `POST .../downloads/{id}/cancel` with `{}` cancels.
  - `GET /api/v1/session` reports `downloads` and `downloadMaxBytes`, and `uploads` and `uploadChunkBytes`.
  - Upload into sandbox (see [console](console.md#upload-into-sandbox)), same OIDC-only rule. `POST /api/v1/workspaces/{name}/uploads` with `{"name", "destination", "size", "sha256"?, "overwrite"?}` starts the helper in the worker and returns 201 once it has accepted the destination: `id`, `state` (`starting`, `receiving`, `finishing`, `done`, `failed`, `cancelled`), `size`, `received`, `written`, `sha256`, `path`, `code`, `error`, `chunkBytes`, `maxBytes`. A size above `downloadMaxBytes` returns 413; an existing file without overwrite 409; a refused path 400; a workspace image without the upload helper 501 ("image too old"). Then send the file in order with `PUT .../uploads/{id}/data`, `Content-Type: application/octet-stream`, `X-Upload-Offset: <byte offset>` and a body of 1 byte to 16 MiB (the console uses `chunkBytes`, 8 MiB). A wrong offset or a part past `size` returns 409 and changes nothing. The response is the upload; after the last part it is the final result with the sandbox's `sha256`. A part that breaks off, `POST .../uploads/{id}/cancel` with `{}`, or 2 minutes without data cancel the upload, and the sandbox keeps nothing. `GET .../uploads[/{id}]` reads recent uploads (in memory, 20 per project).
- `GET /api/v1/workspaces/{name}/audit` returns persisted lifecycle and condition records, including diagnostic `reason` and `message` when available.
- With a handoff intake configured, `POST /api/v1/handoffs` hands a task to an agent in that runtime and `GET /api/v1/handoffs[/{name}]` reads its records.
- With a handoff intake configured, `POST /api/v1/orca-projects` with `{"repo": "OWNER/NAME"}` publishes a repository as a project of that runtime, and `GET /api/v1/orca-projects[/{name}]` reads publish records (`status` is `preparing`, `published` or `failed`). The console sets the actor; a request that supplies one returns 400. `GET /api/v1/session` reports `orcaProjects`.

```json
{
  "name": "example",
  "repository": "operator/example",
  "registries": ["go"],
  "https": [{"host": "example.com", "methods": ["GET", "HEAD"], "paths": ["/"]}]
}
```

`registries` expands to the reviewed hosts before the custom resource is stored. `go` becomes GET and HEAD for `proxy.golang.org` and `sum.golang.org`, each with path `/`. The stored `https` list is the expanded registry rules followed by the explicit rules. Path `/` allows the whole host. It does not approve package versions.

No creation request can select arbitrary container images, runtime classes, storage classes, secret mounts or Git origins. Operators configure those on the controller. Repository is immutable after creation. HTTPS policy may change only while the workspace stays Paused. The API checks stopped processes, and the controller checks and records the applied policy before resume.

A Ready workspace is entered with `kubectl exec` into container `workspace`, `agent-project-controller connect <name>`, or `agent-project-controller exec <name> -- <command>`. The API does not open a tunnel.

## Custom resource

`ProjectWorkspace` is namespaced at `projects.agentwork.dev/v1alpha1`. See `config/samples/project.yaml`. The controller processes only resources in its configured control namespace and derives project namespace and Forgejo identity from the resource UID.

Observed phases are `Preparing`, `Ready`, `Pausing`, `Paused`, `Blocked`, `Removing` and `Removed`. The Ready condition distinguishes desired state from observed readiness. Sandbox readiness includes the init clone and a worker readiness marker. It does not establish that an optional coding provider is authenticated or every requested external endpoint works.

The same group has `AccessProfile`, `AccessGrant`, `OrchestratorClass`, `ProviderClass` and `ForgeClass`. A workspace names them through `spec.accessProfileName` ([access levels](access-levels.md)), `spec.orchestratorClassName`, `spec.forgeClassName` and `spec.providers[].providerClassName` ([provider and forge classes](classes.md)). The chart's ValidatingAdmissionPolicies refuse invalid requests before they are stored ([chart README](../charts/agent-project-controller/README.md#admission-policies)).

Phase changes and new not-ready reasons are published as Kubernetes Events regarding the workspace (`kubectl events --for projectworkspace/<name>`). `status.audit` is deprecated and no longer appended; the durable history is `GET /api/v1/workspaces/{name}/audit`.

`spec.orchestratorClassName` selects a cluster-scoped `OrchestratorClass` (T3, Orca, a custom command client). It is immutable once set; empty means the deployment default. A missing or unsupported class blocks the workspace and grants nothing. See [orchestrator classes](orchestrators.md).

Normal Kubernetes deletion triggers the cleanup finalizer. The dashboard uses the terminal `Removed` desired state so history remains visible. Removed project data cannot be reopened through a state update.

## Extending providers

`internal/forge.Forge` defines discovery, grant and revocation; Forgejo and GitHub implement it. The controller's configured forge (Forgejo or GitHub) serves workspaces without `spec.forgeClassName`; a workspace may name a cluster-scoped `ForgeClass` instead, and `spec.providers[].providerClassName` selects a `ProviderClass`. See [provider and forge classes](classes.md), including how to add a provider or forge. `internal/network.Provider` declares capabilities, produces enforcement objects and supplies pod DNS settings that agree with its DNS enforcement. The inspecting proxy is a separate Go component with a policy document. Provider implementations must qualify their actual behavior before being advertised as supported.

The first implementation uses an operator-selected worker image as its environment template. It does not expose multiple named templates. SSH and OpenRig are not installed. A tool that can run `kubectl exec` can use a Ready workspace. That command is not an authenticated model session. Model access is an optional Secret named `model-access` in the project namespace, mounted read-only at `/run/model`. The controller rejects a refresh token in that Secret. Model hosts must be approved in the HTTPS policy before running a model session. Add them at creation or through a paused access update.

## Combined console

See [the console workflow](console.md) for visible preparation errors, paused
egress changes and optional integration of existing sandbox controls. The
`/sandboxes/` routes use the console login and retain the backend's form CSRF
checks; they do not accept arbitrary upstream targets or proxy credentials.
