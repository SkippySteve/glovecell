# Provider and forge classes

`ProviderClass` and `ForgeClass` are cluster-scoped kinds in
`projects.agentwork.dev/v1alpha1` (decision D12). They describe how the
controller obtains model provider leases and which Git forge hosts a
workspace's repository, the way a StorageClass describes storage. A
`ProjectWorkspace` references them by name. Only cluster administrators
create or change classes.

## ProviderClass

```yaml
apiVersion: projects.agentwork.dev/v1alpha1
kind: ProviderClass
metadata:
  name: grok-short
spec:
  driver: provider-broker      # reads the provider broker's lease Secret
  provider: grok
  lease:
    minDuration: 2m            # withhold leases with less remaining lifetime (default and minimum 1m)
    maxDuration: 3m            # cap the expiry written into a project (empty: the broker's expiry)
  parameters:
    leasesSecret: provider-broker/provider-leases   # optional; must equal PROVIDER_LEASES_SECRET
```

A project selects it per provider:

```yaml
spec:
  providers:
  - name: grok
    providerClassName: grok-short
  - name: cursor              # no class: the default class for cursor
```

How a grant resolves:

- With `providerClassName`, the controller uses that class. The class's
  `spec.provider` must equal the grant's `name`.
- Without one, it uses the class for that provider that carries the annotation
  `projects.agentwork.dev/is-default-class: "true"`. If more than one class
  carries it, the grant fails closed.
- If no default class exists, or the ProviderClass CRD is not installed, the
  grant uses the built-in default: driver `provider-broker`, the controller's
  `PROVIDER_LEASES_SECRET` and a one-minute minimum. That is exactly how a
  grant behaved before classes existed. Deleting a default class therefore
  does not revoke access. To revoke access, remove the grant or unset
  `PROVIDER_LEASES_SECRET`.

Drivers:

| Driver | Status |
| --- | --- |
| `provider-broker` | Implemented. It serves `grok` and `cursor`. The only parameter is `leasesSecret`. |
| `openbao-broker` | Reserved. Refused until it is implemented. |
| `api-key-secret` | Reserved. Refused until it is implemented. |

A class pointing at any other Secret is refused, so a class cannot make the
controller copy arbitrary Secrets into projects.

## ForgeClass

```yaml
apiVersion: projects.agentwork.dev/v1alpha1
kind: ForgeClass
metadata:
  name: team-forgejo
spec:
  driver: forgejo
  baseURL: https://git.team.example
  credentialsSecretRef:        # the controller's admin token for this forge
    name: team-forgejo-admin
    key: token                 # namespace: empty or the controller's namespace
  parameters:
    owner: team
```

A workspace selects it once, when it is created:

```yaml
spec:
  repository: team/project
  forgeClassName: team-forgejo
```

- An empty `forgeClassName` keeps the controller's configured forge
  (`GIT_HOST` and its settings), so existing workspaces do not change.
- `forgeClassName` is immutable. A class's `driver`, `baseURL` and
  `parameters.owner` are immutable too. Removal revokes on the forge that
  granted access. If the class is missing or unusable, removal of a
  workspace that holds a grant waits in `Removing` and does not report
  completion. It never falls back to another forge.
- For that workspace, the class sets the forge that is used for grants,
  revocation, the proxy's Git rule, the worker's clone origin and additional
  `host: forgejo` repositories.
- The administrative credential stays in the controller. Workers receive only
  the per-workspace token that the forge mints, as before.

Drivers: `forgejo` is implemented. `github` and `gitlab` are reserved, and the
controller refuses them as unsupported for now. The chart's admission policy
also refuses them. GitHub remains available as the controller's configured
forge (`GIT_HOST=github`), not as a class.

## Status and failure

The controller sets `status.conditions[type=Accepted]` on each class. A
`False` condition gives the reason, never credential material. Workspaces
resolve their classes again on every reconciliation, so a lagging status
never grants anything. When a class is unknown, uses an unsupported or unknown
driver, has unknown parameters or cannot be read, the workspace is `Blocked`
and receives nothing from it. For a provider, every brokered grant is
withdrawn. For a forge, no grant is made.

## Helm chart

With `classes.createDefaults: true` (the default), the chart generates:

- `<namespace>-<release>-forgejo`: a ForgeClass for `FORGEJO_ORIGIN`,
  `FORGEJO_OWNER` and key `forgejo-token` of `existingSecret`, only with
  `GIT_HOST=forgejo`;
- `<namespace>-<release>-grok` and `-cursor`: default ProviderClasses on
  `PROVIDER_LEASES_SECRET`, only when it is set.

These classes resolve to the same behaviour as the settings they come from, so
a chart upgrade changes nothing. With `classes.admissionPolicy: true` and
`admissionPolicies.enabled: true` (both the default), a
ValidatingAdmissionPolicy refuses a ForgeClass whose credentials are outside
the release namespace, or whose driver is not `forgejo`.

The chart also renders the ClusterRole `<namespace>-<release>-class-admin`
(full control of both kinds), labelled
`projects.agentwork.dev/aggregate-to-admin: "true"` and bound to nobody. The
controller can only read classes and write their status. Agent and worker
ServiceAccounts have no access.

Helm does not upgrade CRDs. Apply the chart's `crds/` directory (including
`providerclasses.yaml`, `forgeclasses.yaml` and the updated
`projectworkspaces.yaml`) before upgrading, or use a Flux HelmRelease with `upgrade.crds: CreateReplace`.
Without the class CRDs the controller runs as before: bare provider names and
the configured forge keep working, named classes fail closed, and class
status is off. One cluster should run one controller that creates default
classes. A second one's defaults would make bare grants ambiguous, and its
admission policy would refuse the first one's ForgeClasses.

## Adding a provider

1. **Reviewed routes.** Add the provider's hosts to
   `internal/proxy.BrokeredProviders`, together with the exact method and path
   rules and the grant check in `internal/proxy/provider_grants.go`. Add a
   stock launcher that reads `/run/model/provider-access`
   ([provider launchers](provider-launchers.md)). Add the name to the
   `spec.providers[].name` enum in `config/crd/projectworkspaces.yaml`.
2. **Lease source.** For the `provider-broker` driver, teach the broker to
   renew the provider's owner login. Add it to
   `internal/providerbroker.LeaseProviders` and to the owner read and write
   helpers. A new driver, such as `openbao-broker`, goes in
   `internal/providerbroker/classes.go`: accept it in `ValidateClass`, list its
   parameters (unknown keys must fail closed), and return a `Class` that names
   where the lease comes from. Remove it from `ReservedProviderDrivers`.
   Refresh material must never be published. The controller, proxy and
   launcher reject it.
3. **Class.** Ship a ProviderClass, and a default class in the chart if bare
   names should use it.
4. **Tests.** Cover resolution, a refused unknown or reserved driver, leases
   withheld outside the bounds, no refresh material in project Secrets, and
   withdrawal that survives an interrupted write
   (`internal/controller/classes_test.go`).

## Adding a forge

1. **Implementation.** Implement `internal/forge.Forge` (`Discover`, `Grant`,
   `Revoke`). `Grant` must scope a per-workspace identity to one repository
   and protect the default branch. `Revoke` must be idempotent and must report
   failure rather than success when it cannot confirm revocation.
2. **Driver.** Register a `forge.Driver` for it in `internal/forge/forge.go`.
   Name the parameters it accepts and remove its name from `ReservedDrivers`.
   Extend the ForgeClass `driver` enum if the name is new, and the chart's
   admission policy expression.
3. **Proxy and clone.** Add the forge's Git and API rules to
   `controller.AccessRules` and its clone origin to `cloneOrigin`, so the
   worker reaches only the workspace's own repository.
4. **Tests.** Cover resolution through a class, a refused unknown driver and a
   refused foreign credential namespace, no credential in the project
   namespace or Sandbox, and removal that waits for revocation on the right
   forge (`internal/controller/classes_test.go`,
   `internal/forge/forge_test.go`).
