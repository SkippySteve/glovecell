# agent-project-controller

Helm chart for the controller, embedded dashboard, API, ProjectWorkspace CRD, and RBAC.

The chart does not install Agent Sandbox, Cilium, a runtime class, Forgejo, or an identity provider. The public install path is the [quickstart](../../docs/quickstart.md). Published image digests are in [alpha-values.yaml](../../docs/examples/alpha-values.yaml).

Supply digest-pinned `controllerImage` and `workspaceImage`, plus the Forgejo origin, owner, public origin, and OIDC client. Create Secret `controller-secrets` outside the chart, with `forgejo-token` and `oidc-client-secret`. `proxyImage` defaults to `controllerImage`.

Keep `config.NETWORK_PROVIDER` at `cilium` for the qualified profile. `network-policy` is an unvalidated experiment. The `0.1.0-alpha.1` images do not contain that provider or workspace-image replacement.

To connect an agent runtime's handoff intake, set `config.HANDOFF_URL` and `config.HANDOFF_TOKEN_FILE` together, for example `/control-secrets/handoff-token` with a `handoff-token` key of at least 32 bytes in the existing Secret. The intake must hold the same token. Optionally set `config.HANDOFF_SANDBOX` to the existing sandbox that runs the intake, so the Sandboxes page lists its projects under that sandbox. See [handoffs and Orca projects](../../docs/console.md#handoffs-and-orca-projects).

## Lifecycle Events

The controller publishes each workspace's lifecycle transitions as Kubernetes Events (`events.k8s.io/v1`, reporting controller `projects.agentwork.dev/agent-project-controller`) regarding the `ProjectWorkspace`: phase changes (action `ChangePhase`, reason = the Ready condition's reason, type `Warning` for `Blocked` and `CleanupPending`) and a new not-ready reason or message in the same phase (`UpdateCondition`). Audit records publish no Events: they are the durable history, which the console reads through its history store (audit ConfigMaps today). Read them with `kubectl events -n <namespace> --for projectworkspace/<name>`. The Role `workspace-events` lets the controller create and patch Events in the release namespace only.

Events expire (kube-apiserver `--event-ttl`, one hour by default), so they show recent activity only. The console's History reads the durable audit records. `status.audit` is deprecated: the controller no longer appends to it, and existing entries stay readable for one release.

## Admission policies

With `admissionPolicies.enabled: true` (the default), the chart installs `ValidatingAdmissionPolicy` objects and bindings (`admissionregistration.k8s.io/v1`, Kubernetes 1.30 or later) that refuse invalid `ProjectWorkspace` requests at the API server, before anything is stored. Each policy has `failurePolicy: Fail`. Each binding has `validationActions: [Deny]` and applies only in the release namespace. Policy and binding names start with `<namespace>-<release>`.

Deployment settings reach the policies through the ConfigMap `admission-params` in the release namespace (`paramKind: v1/ConfigMap`). The chart fills it from the values below. If the ConfigMap is missing, `parameterNotFoundAction: Deny` refuses every request. Edit the values, not the ConfigMap: the controller reads the same settings from its environment, and the two must agree.

| Key | From |
| --- | --- |
| `gitHost` | `config.GIT_HOST` |
| `repositoryOwner` | `config.FORGEJO_OWNER`, or `config.GITHUB_OWNER` with `GIT_HOST=github` |
| `additionalGitHubOwner` | `additionalGitHub.owner` when enabled |
| `reservedHTTPSHosts` | host name of `config.FORGEJO_ORIGIN`, or `github.com,api.github.com` |
| `lanAllowedCIDRs`, `lanDeniedCIDRs` | `config.LAN_GRANT_ALLOWED_CIDRS`, `config.LAN_GRANT_DENIED_CIDRS` |
| `providersEnabled` | `true` when `config.PROVIDER_LEASES_SECRET` is set |

The checks run on create and update. On update, a policy only checks the fields that changed, so narrowing a setting later never blocks unrelated edits or finalizer removal on an existing project. The controller still refuses that project when it reconciles.

| Policy | Refuses |
| --- | --- |
| `workspace-name` | On create, names that do not match `^[a-z][a-z0-9-]{0,39}$` (the console's rule). |
| `workspace-repositories` | A primary repository outside `repositoryOwner`. An enrolled Forgejo repository outside that owner, marked public, or on a GitHub-hosted controller. A private GitHub enrollment outside `additionalGitHubOwner`. The same repository enrolled twice, or enrolled again as the primary. |
| `workspace-https` | Rule hosts that are not exact lowercase DNS names (wildcards, IP literals, empty or overlong labels). Paths that are not literal prefixes: `*`, `?`, `#`, `%`, `\`, `..` or `//`. Rules for a Git host the controller manages (`reservedHTTPSHosts`, plus `github.com` when a GitHub repository is enrolled). |
| `workspace-lan-hosts` | Any LAN host while `lanAllowedCIDRs` is empty. Addresses outside every allowed range, inside the denylist (a bare address means `/32`), or in the built-in refused ranges (`0.0.0.0/8`, `127.0.0.0/8`, `169.254.0.0/16`, `224.0.0.0/4`, `240.0.0.0/4`). Network and broadcast addresses: `x.x.x.0` and `x.x.x.255`, plus the first and last address of an allowed `/25` to `/30`. Notes over 200 bytes or with control characters. |
| `workspace-providers` | `spec.providers` while no provider lease source is configured. |

The CRD schema already enforces the remaining field rules: the `desiredState` enum and terminal `Removed`, the immutable repository, HTTPS edits only while paused, HTTP methods, IPv4 address format, port range and uniqueness, and list sizes.

The controller keeps all of its checks as defence in depth, including those the policies repeat. Some rules depend on cluster state that admission cannot read, so only the controller enforces them: a LAN host must not be a node address, the network provider must support LAN and provider policies, the runtime and storage classes must exist, and the total proxy rule count must fit. It refuses such a project before granting anything. No admission webhook is needed.

Set `admissionPolicies.enabled: false` only on a cluster without the API. The controller's checks still apply.
