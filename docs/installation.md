# Installation

Deploy first in a disposable qualification cluster. This initial implementation is not a qualified production replacement for an existing broker. The [quickstart](quickstart.md) is the public `0.1.0-alpha.1` install path.

## Prerequisites

- Kubernetes 1.36.2 is the qualified cluster. Local API-server tests use 1.34.1. Other versions need the same qualification.
- Upstream Agent Sandbox 1.0.4 with the `agents.x-k8s.io/v1beta1` Sandbox API. Templates, claims and pools are unnecessary.
- Cilium 1.19.5 with DNS/FQDN policy enforcement enabled and CoreDNS using the `k8s-app=kube-dns` label. Project pods use explicit CoreDNS addresses, not the node's default resolver. No cluster-wide rule may broaden project egress around the inspection proxy. The qualified gVisor profile sets `DNS_DIRECT_ENDPOINTS=true`.
- The qualified isolation runtime is gVisor, via a RuntimeClass such as `agent-gvisor`. Checking that the resource exists does not prove runtime isolation. Kata is not part of this qualification.
- Forgejo 15.0.9. Repository-scoped personal access tokens and protected `main` are required. Git over HTTPS on port 443 must use a server certificate trusted by the container. The qualified identity provider is Authentik 2026.5.6.
- One Forgejo owner, one operator group and one controller replica. This is not a multi-tenant hosted service.
- A storage class and provisioner with `Delete` reclaim behavior. The controller checks both the class and bound volume. The provisioner must actually delete its backing data. Snapshots and backups need a separate operator retention policy.
- An HTTPS dashboard origin and OIDC provider. Register a confidential client with the exact redirect URI `<PUBLIC_ORIGIN>/auth/callback`. Request group claims or explicitly allow issuer-local subject IDs.

Approved operators share one trust boundary and can manage every project under that owner.

## GitHub project host

GitHub can replace Forgejo as the place projects are stored. It is optional, it is not qualified, and it is not in the published `0.1.0-alpha.1` images or chart. A deployment that selects it must be built from the commit that adds `GIT_HOST=github` and deployed by that build's digest. `docs/examples/github-values.yaml` shows the Helm shape and repeats the published digests only so they are recognizable. Do not install those digests with `GIT_HOST=github`.

One controller process uses one host. `GIT_HOST` defaults to `forgejo`, which keeps an existing installation unchanged. `GIT_HOST=github` is a separate deployment. A workspace repository is still `owner/name`; it does not carry its own host. Dashboard login stays the operator OIDC provider. GitHub is not a substitute for that login. GitHub Enterprise Server is not supported.

Create a GitHub App, disable its webhook, and grant the installation Administration read, Contents read and write, Pull requests read and write, and Metadata read. Install it on the account named by `GITHUB_OWNER`, preferably with only the repositories operators may open. Record the App id and the installation id. Store the App private key, a PKCS#1 or PKCS#8 RSA PEM of at least 2048 bits, as Secret key `github-app-private-key`. The chart does not create that Secret and the key must not be committed.

```sh
kubectl create namespace agent-project-controller
kubectl -n agent-project-controller create secret generic controller-secrets \
  --from-file=github-app-private-key=/secure/path/github-app-private-key.pem \
  --from-file=oidc-client-secret=/secure/path/oidc-client-secret
```

Set `GITHUB_OWNER`, `GITHUB_APP_ID`, and `GITHUB_APP_INSTALLATION_ID`. Leave `FORGEJO_ORIGIN`, `FORGEJO_OWNER`, and the Forgejo token unset on that deployment. `PUBLIC_ORIGIN` and the OIDC client stay required.

Before the first grant, classic branch protection on `main` must enforce rules for administrators, require at least one approving review, restrict who can push, omit this GitHub App from that allow list, and leave force-push and branch deletion disabled. A missing rule or a ruleset-only repository is refused. The controller does not create or repair GitHub protection.

The controller keeps one installation token in memory for discovery and protection reads. Each workspace receives a separate token limited to that one repository, with contents write, pull requests write, and metadata read. A response that names another repository, selects every repository, or carries any other permission is discarded and revoked. The workspace clones `https://github.com/owner/name.git`. Its ledger username is the workspace identity; GitHub accepts that username when the password is the installation token. `github.com` and `api.github.com` are not private-host exceptions, so a private or reserved DNS answer is rejected.

Tokens expire in about one hour. A stored token is reused while its recorded expiry is more than 20 minutes away and GitHub still accepts it. Otherwise the controller revokes it and mints a replacement. A restart forgets those expiry records, so the next grant rotates the stored token. GitHub does not list live installation tokens. A token issued and then lost before the ledger save can remain valid until GitHub expires it. Removal confirms the stored token and any token this process still remembers. No live GitHub grant is part of this alpha.

## Experimental standard NetworkPolicy

`NETWORK_PROVIDER=network-policy` is an opt-in profile for clusters that do not run Cilium. It is not qualified. Do not describe it as a supported dataplane, and do not point it at a cluster whose enforcement has not been tested independently. The default remains `cilium`.

Standard NetworkPolicy has no DNS name filter. In this profile the worker has no DNS egress at all. The controller allocates a proxy ClusterIP and pins the hostname `proxy` in the worker pod with `hostAliases`. Worker egress is only that proxy on TCP 3128. The proxy may query CoreDNS and connect to public IPv4, and to IPv6 global unicast `2000::/3`, on TCP 443. Reserved, private, documentation, and operator-declared cluster ranges are excluded. IPv4-mapped IPv6 addresses are outside the IPv6 allow. HTTPS host, method, and path checks still happen inside the inspecting proxy.

The profile refuses to start unless `NETWORK_POLICY_ENFORCEMENT_ACKNOWLEDGED` is exactly `true` and `NETWORK_POLICY_CLUSTER_CIDRS_JSON` is a non-empty JSON array of canonical pod, service, and node prefixes. A default route is rejected. The acknowledgement records that an operator ran the procedure in [qualification](qualification.md). API objects do not prove that the installed CNI enforces the policy.

An explicit gateway must set `disableSNIFilter` to true. Native policy can limit the gateway peer and port, and it cannot limit TLS server names. A compromised proxy is the remaining HTTPS boundary: it can use its own public TCP 443 permission and any configured gateway. Workers still cannot. Extra NetworkPolicy objects in a project namespace are rejected because the API is additive. Policies outside the project namespace are not visible to this check.

Switching an existing project between `cilium` and `network-policy` is refused. The controller leaves the old proxy Service and policies in place and suspends the workspace. Remove and recreate the project after the dataplane procedure passes. See `docs/examples/network-policy-values.yaml` for the Helm shape. The sample Kustomize ClusterRole also grants NetworkPolicy and node read access so an overlay can select this profile. Remove those rules when the experimental profile is not installed.

## Controller permissions

The controller holds no Secret access and no namespace delete cluster-wide. It reads and writes Secrets in two places only:

- the control namespace, for its credential ledgers (`apc-<uid>`, `apcp-<hash>`, `t3-connect-<uid>`), through Role `controller-secrets`;
- each project namespace, for `git-identity`, `proxy-ca` and the operator's `model-access`, through RoleBinding `agent-project-controller-access`. The controller creates that binding for itself when it sets up the namespace. It points at the ClusterRole named by `PROJECT_ACCESS_CLUSTER_ROLE` (Secrets get/create/update/patch/delete, and get/delete on the namespace object; delete lets brokered provider access remove a `model-access` Secret it created). A RoleBinding in namespace `apc-<uid>` that grants `namespaces` applies to the namespace `apc-<uid>` itself, so the controller can delete its own project namespaces and no others.

Secrets are never cached, so the controller never starts a cluster-wide Secret watch. The chart and `config/deploy` install both roles and the `bind` permission for exactly that ClusterRole. With `PROJECT_ACCESS_CLUSTER_ROLE` empty the controller creates no binding, for installations that still grant these verbs cluster-wide. When you narrow an existing installation, install the new image first, wait until every project namespace has the `agent-project-controller-access` binding, and only then remove the cluster-wide verbs.

## Build and configure

Build and push the image to your registry. For release builds pass digest-pinned base references with `--build-arg GO_IMAGE=...` and `--build-arg RUNTIME_IMAGE=...`. Deploy the resulting image by its registry digest, not a mutable tag.

```sh
docker build -t registry.example.com/agent-project-controller:dev .
docker push registry.example.com/agent-project-controller:dev
```

The published alpha images can be used instead of that local build. Their digests are in [examples/alpha-values.yaml](examples/alpha-values.yaml) and on the [0.1.0-alpha.1 release](https://github.com/SkippySteve/agent-project-controller/releases/tag/0.1.0-alpha.1). Those images are commit `6d754c47f223edbe5bfff399405fb1450abb0c4b`. Workspace-image replacement, the experimental NetworkPolicy provider, and the optional GitHub project host landed on `main` after that commit. A deployment that needs any of those behaviors must build that later source and deploy the resulting digest.

Prepare a Kustomize overlay over `config/deploy`. Set the controller Deployment image and `WORKSPACE_IMAGE` to the built digest. Replace example hosts, Forgejo owner, runtime class, storage class and OIDC settings. Set `CLUSTER_DOMAIN` if it differs from `cluster.local`. `WORKSPACE_IMAGE` is empty in the sample, so unconfigured installations fail rather than launching an arbitrary image.

The controller discovers the address of `DNS_SERVICE_NAMESPACE` / `DNS_SERVICE_NAME`, default `kube-system` / `kube-dns`. The service must select CoreDNS pods labeled `k8s-app=kube-dns` and expose TCP and UDP port 53. Workers and proxies use `dnsPolicy: None`, that address and the configured cluster search domain. DNS policy still permits only the names required by each role. Invalid or missing resolver configuration blocks project grants.

Clusters can keep node-local DNS for other workloads. For this profile, project DNS traffic must reach CoreDNS through Cilium's DNS enforcement. Some node-local configurations intercept the kube-dns service address or use local redirect policies. In those clusters, configure a separate ClusterIP service selecting CoreDNS and qualify its routing. Do not replace the endpoint restriction with unrestricted node/host DNS access. Explicit pod DNS configuration alone does not prove that interception is absent.

Workspace resource defaults are a `500m` CPU and `512Mi` memory request with limits of `4` CPUs and `4Gi`. The clone init container uses the same profile. The HTTPS proxy has separate defaults of `100m` / `128Mi` requests and `1` CPU / `512Mi` limits. Configure these with `WORKSPACE_CPU_REQUEST`, `WORKSPACE_MEMORY_REQUEST`, `WORKSPACE_CPU_LIMIT`, `WORKSPACE_MEMORY_LIMIT` and the equivalent `PROXY_` variables. Values must be positive and requests must not exceed limits. Requests reserve scheduling capacity; limits bound use, so a memory-heavy build may still need a larger profile.

Resource profiles, resolver settings, and the selected network provider are immutable for already provisioned environments. Configuration changes apply to new projects. Existing projects report configuration drift and remain suspended rather than silently changing their security settings. Remove and recreate them after saving local work when changing these settings.

The workspace image digest is the one template field that can move with the controller. Replacement waits until the worker pod is gone, deletes only the Sandbox, and the next reconcile creates the controller's pinned image on the same volume and Git identity. A Sandbox image changed to any other value is not adopted. While a worker pod still exists, that mutation suspends the project and leaves the Sandbox in place. Other Sandbox differences, including placement, stay drift as well.

Create the control namespace, then supply a Secret named `controller-secrets` there. Use `--from-file` with protected files so credentials do not enter shell arguments or Git:

```sh
kubectl create namespace agent-project-controller
kubectl -n agent-project-controller create secret generic controller-secrets \
  --from-file=forgejo-token=/secure/path/forgejo-token \
  --from-file=oidc-client-secret=/secure/path/oidc-client-secret
```

The Forgejo credential must be an operator-controlled administrative account capable of creating and disabling workspace users, managing repository collaborators and branch protection, and reading users and repositories. Never reuse a worker credential here. Qualification uses an `all`-scoped token on a disposable administrator. A minimally scoped production credential must be verified against the installed Forgejo version before deployment. This administrative capability is a material trust boundary.

OIDC environment variables:

- `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET_FILE`.
- `OIDC_ALLOWED_GROUPS`, comma-separated exact group names, or `OIDC_ALLOWED_SUBJECTS`, comma-separated subject IDs. An empty authorization allowlist is rejected.
- `OIDC_GROUPS_CLAIM`, default `groups`. Supply a top-level array claim through your provider's mapper.
- `OIDC_SCOPES`, default `profile,groups`. Adjust scopes to the provider; `openid` is always included.
- `PUBLIC_ORIGIN`, the exact HTTPS origin without a trailing slash or path.

For automation, optionally mount an additional authentication secret and set `API_SECRET_FILE` to its path. Requests use `Authorization: Bearer <secret>`. The secret must contain at least 32 bytes. Static browser login additionally requires `ALLOW_STATIC_LOGIN=true`; it is disabled in the sample.

To hand tasks to an agent runtime and publish projects to it, set `HANDOFF_URL` to the runtime's handoff intake origin and `HANDOFF_TOKEN_FILE` to a mounted secret of at least 32 bytes that the intake also holds. The controller refuses to start with only one of the two. See [handoffs and Orca projects](console.md#handoffs-and-orca-projects).

Expose the ClusterIP Service through your existing TLS reverse proxy. Do not expose the project namespace or proxy directly. Standard OIDC verification handles application login; forwarded identity headers are ignored. The proxy must preserve browser Origin headers, and the application origin must match exactly.

Render your overlay, inspect its RBAC and apply it to the qualification cluster. The controller has cross-namespace resource permissions to provision projects, so its credential and service account must be treated as privileged infrastructure.

Before trusting a cluster, run `agent-project-controller preflight` with the same `RUNTIME_CLASS`, `STORAGE_CLASS`, `NETWORK_PROVIDER`, DNS, cluster CIDR, and `PROXY_GATEWAY_JSON` settings the controller will use. The Cilium profile checks the Agent Sandbox CRD, the CiliumNetworkPolicy CRD, and CoreDNS. The experimental NetworkPolicy profile reports `network-policy-dns` and checks that declared exclusions cover CoreDNS, the Kubernetes API address, node addresses, and pod CIDRs. Both profiles check the RuntimeClass and Delete reclaim on the storage class. A passing preflight is not the qualification in [qualification](qualification.md). Applying the CRD is not qualification either.

## LAN host grants

Operators can let one project's worker open TCP connections to single hosts on
their own network; see [LAN hosts](console.md#lan-hosts). The feature is off by
default. Two settings enable it:

- `LAN_GRANT_ALLOWED_CIDRS`: IPv4 prefixes, separated by commas or spaces, that
  grants may come from, for example `192.168.1.0/24` or a client VLAN. Each must
  be canonical and inside `10.0.0.0/8`, `172.16.0.0/12` or `192.168.0.0/16`.
  Empty turns grants off.
- `LAN_GRANT_DENIED_CIDRS`: prefixes or single addresses that are refused even
  inside the allowed ranges. Required when grants are on. List the node network
  and node addresses, the Kubernetes API VIP, LoadBalancer and service VIP
  pools, the pod and service CIDRs, and every router, firewall or gateway
  address on the allowed networks.

The controller also refuses node addresses it reads from Node objects, so its
ClusterRole needs `get` and `list` on `nodes`; the chart adds that rule when
`config.LAN_GRANT_ALLOWED_CIDRS` is set. Grants need `NETWORK_PROVIDER=cilium`;
startup fails otherwise. Apply the CRD with `spec.lanHosts` before the
controller image that uses it, or the API server prunes saved grants.

```yaml
config:
  LAN_GRANT_ALLOWED_CIDRS: 192.168.1.0/24
  LAN_GRANT_DENIED_CIDRS: 192.168.1.1,192.168.1.10,192.168.1.11,10.96.0.0/12,10.244.0.0/16
```

The settings are read at startup. Restart the controller after changing them.
Narrowing them blocks any project whose grant falls outside, stops its worker
and removes its policy until an operator removes that grant.

Pod egress usually leaves the node with the node's address. Allow that path on
the router or firewall between the nodes and each granted host: source the node
addresses, destination the host and the granted TCP ports. Keep that rule as
narrow as the grant; the controller does not configure network devices.

For defense in depth, add an admission policy that lets only the controller's
service account write Cilium policies in project namespaces (namespaces
labeled `projects.agentwork.dev/workspace-uid`), and that limits policy
`lan-hosts` to `/32` TCP egress inside the allowed ranges. The reusable chart
does not install one because it cannot know your ranges.

To roll back, remove every grant in the console, then clear
`LAN_GRANT_ALLOWED_CIDRS` and restart. Deleting the setting first blocks the
projects that still hold grants until those grants are removed. An older
controller ignores `spec.lanHosts` and does not delete existing `lan-hosts`
policies, so remove the grants before downgrading or delete those policies by
hand.

## Downloads

"Download into sandbox" lets an OIDC operator put a file from an https URL
into a project sandbox; see [console](console.md#download-into-sandbox) and
[security](security.md#downloads-into-a-sandbox). It is off by default.

- `DOWNLOADS_ENABLED`: `true` turns it on. It requires `NETWORK_PROVIDER=cilium`
  (startup and the chart fail otherwise).
- `DOWNLOAD_MAX_BYTES`: the per-file limit in bytes, at most 16 GiB. Empty
  means 2 GiB.
- `DOWNLOAD_TIMEOUT`: the time one fetch may take, from `1m` to `2h`. Empty
  means `30m`.

The controller needs `pods/exec` in project namespaces (the
`WORKER_EXEC_CLUSTER_ROLE` binding the chart already creates per project) and
its existing rights to write CiliumNetworkPolicies there. Both the proxy image
and the workspace image must contain this release's `agent-project-controller`
(the `download-grant` and `fetch` commands). A project running older images
reports that it "predates downloads" until it restarts on the new images.

For defense in depth, admit `download-*` Cilium policies in project namespaces
only when they select the project's proxy pod, allow only DNS and `toFQDNs`
`matchName` rules on port 443, and carry the expiry annotation.

To roll back, set `DOWNLOADS_ENABLED=false` and restart. In-flight downloads
stop (the helper removes its temporary file when the exec stream ends); the
proxy's grants expire within 10 minutes and the next controller deletes
leftover `download-*` policies only while the feature is on, so delete those by
label `projects.agentwork.dev/download` if you turn it off.

## Client limit

Inspection is an HTTP CONNECT to port 443. The proxy then inspects the negotiated HTTP/2 or HTTP/1.1 request with the same host, method and path rules. WebSocket upgrades and certificate-pinned clients stay blocked. There is no opaque tunnel. Allowing a registry host does not change that limit.

## Using a project

Sign in, select a repository and enter a project name. The dashboard can also select the reviewed package registries `go`, `npm`, `python` and `rust`. Each selection allows GET and HEAD for the whole host, including the redirect hosts below. It does not approve package versions, and lockfiles are not read. The resulting HTTPS rules can be changed from the sandbox's Internet section while the project is paused.

| Selection | Hosts |
| --- | --- |
| go | `proxy.golang.org`, `sum.golang.org` |
| npm | `registry.npmjs.org` |
| python | `pypi.org`, `files.pythonhosted.org` |
| rust | `index.crates.io`, `static.crates.io` |

A Ready project shows a `kubectl exec` command for the `workspace` container. `agent-project-controller connect <workspace>` runs that shell with the local kubeconfig. The worker does not open an SSH port or any other ingress. A local process can use `connect` or `exec`. Tools that need a preview listener are not attached to the worker. The command requires phase `Ready`, a recorded namespace and exactly one worker pod.

Initial cloning creates a dedicated `agent/apc-<workspace-uid>` local branch. Agents can push ordinary non-main branches and open PRs using the project token. No provider credential or unattended task is enabled automatically.

The repository is cloned into `/workspace/repo`. Home, caches and configuration live under the same project volume. Pause preserves that volume and the Git identity. On resume, bootstrap fetches remote updates without resetting the current branch or overwriting local edits. Agents use normal Git commands to incorporate fetched changes and push reviewed work.

Model credentials stay out of the image. Create a Secret named `model-access` in the project namespace when a tool needs them. The worker mounts it read-only at `/run/model` and tolerates its absence. The controller rejects that Secret when it contains a refresh token, and it never copies an owner refresh token into the image or the Secret. `agent-project-controller exec <workspace> -- <command>` runs one command in the workspace for a tool that can start a local process.

Add other HTTPS destinations through the dashboard, API or custom resource at project creation, or through a paused access update afterward. Extra host access is never inferred from package files. Package registries often redirect to additional hosts, which must be explicitly authorized. See [the combined console workflow](console.md) for editing access and including existing sandboxes.

## Optional telemetry

Set `OTEL_ENABLED=true` and `OTEL_EXPORTER_OTLP_ENDPOINT` to a trusted endpoint. Standard OTLP HTTP exporter environment variables configure authentication and TLS. No Collector, OpenTelemetry Operator or particular monitoring vendor is required. The control backend exports reconciliation traces and metrics. Structured process logs go to stderr; direct OTLP log export is not implemented.

Telemetry outages do not authorize access or prevent revocation. Workspace applications are not instrumented automatically. An optional workspace exporter needs a separate authorized network path and data policy.

## Upgrades and uninstall

Keep one controller replica. OIDC sessions are invalidated on restart. Credentials and requests persist in Kubernetes; history, approvals and access requests persist in PostgreSQL ([database](database.md)), so back up both. Back up control-plane state through an operator-controlled encrypted backup procedure. If the credential ledger is lost, the controller deliberately leaves cleanup incomplete rather than guessing at external access.

Before uninstalling, set every workspace's desired state to `Removed` and verify its status, disabled Forgejo account and deleted backing volume. Do not remove the CRD, force finalizers, uninstall the controller or prune its control namespace while active workspaces remain. Those actions can strand grants and data. The controller retains scrubbed credential tombstones, immutable audit ConfigMaps from before the database and the database's history (retention settings in [database](database.md#retention)); keeping them longer is an operator decision.

## Single-package installation

The alpha Helm chart in `charts/agent-project-controller` installs the Go
controller, embedded dashboard, API, ProjectWorkspace CRD and Kubernetes RBAC.
The dashboard is a client of the same documented API. A separate UI deployment
is optional future work, not an installation requirement.

Provide digest-pinned `controllerImage` and `workspaceImage`, and fill the
required Forgejo, public-origin and OIDC settings in `values.yaml`. Provision
`existingSecret` independently with `forgejo-token` and `oidc-client-secret`.
If `automationAPI.enabled` is true it also needs `api-secret`, at least 32
characters. The chart neither generates nor embeds these credentials.

```sh
helm upgrade --install project-controller ./charts/agent-project-controller \
  --namespace agent-project-controller --create-namespace --values operator-values.yaml
```

Install only one release per control namespace. Existing Agent Sandbox, Cilium,
a qualified runtime and a Delete-reclaim storage class are prerequisites.
Expose the controller Service through your existing HTTPS ingress or Gateway.
The chart does not replace the cluster's network, ingress or authentication
platform. Helm manages CRDs conservatively: apply reviewed CRD schema upgrades
separately before upgrading the controller. Uninstalling the chart is not a
workspace revocation workflow. Request and verify removal of every workspace
first so external repository access and project storage are cleaned up.

The coding image is optional and separately published so adopters can choose
other tools. Publish both images by digest. See [deployment profiles](deployment.md)
and `docs/examples/gvisor-values.yaml`. The qualified gVisor profile sets
`DNS_DIRECT_ENDPOINTS` to `true` and supplies structured `proxyGateway` values. The chart
default of `false` is not that profile. This remains an alpha package. The
pilot evidence is summarized in [qualification](qualification.md). Live
deployment pins for one operator stay in that operator's private
infrastructure repository and are not required to install the chart.

Configuration changes roll the controller automatically through a pod-template checksum.
After rotating an externally managed control Secret, restart the controller so
it reloads the new credentials. Use a dedicated control namespace. The controller
pod is hardened; project namespaces additionally enforce restricted Pod Security.

Dashboard logout invalidates the local session and opens a signed-out page.
It does not sign you out of the identity provider or your other applications.
Select Sign in to start a new dashboard session.

Before uninstalling the Helm release, request removal of every project and wait for `Removed`. Delete each completed ProjectWorkspace and wait for deletion while the controller is still running. Its cleanup finalizer needs the controller to verify revocation and storage removal. Uninstalling the controller first can leave requests and the namespace stuck in Terminating. Do not force namespace finalization or remove request finalizers without independently verifying external revocation and owned storage/process removal.

## Cluster configuration through Helm values

The chart is the shared renderer for portable installations and private pilot
manifests. [alpha-values.yaml](examples/alpha-values.yaml) pins the published
`0.1.0-alpha.1` images. The qualified [gVisor profile](examples/gvisor-values.yaml)
uses placeholder digests, and [custom-cluster-values.yaml](examples/custom-cluster-values.yaml)
shows scheduling and routing fields. Replace Forgejo, origin and OIDC settings,
then render for review:

```sh
helm template projects charts/agent-project-controller \
  --namespace agent-project-controller --include-crds --values my-cluster.yaml
```

Set `config.RUNTIME_CLASS`, `config.STORAGE_CLASS` and `config.STORAGE_SIZE` to
your provisioned runtime and Delete-reclaim storage. `config.CLUSTER_DOMAIN`,
`config.DNS_SERVICE_NAMESPACE`, `config.DNS_SERVICE_NAME` and
`config.DNS_DIRECT_ENDPOINTS` select the project DNS discovery strategy. The
CoreDNS pod label and TCP/UDP port requirements still apply. These values do
not install a runtime, provisioner or DNS service.

Use `controllerScheduling`, `workspaceScheduling` and `proxyScheduling` for
independent `nodeSelector`, `tolerations` and `affinity` settings. Workspace
placement applies to the Sandbox pod and its clone init container. RuntimeClass
scheduling constraints must also allow the selected nodes. Placement changes
for existing projects retain the controller's drift protection; save work and
follow the paused upgrade/recreation procedure rather than expecting a running
workspace to move automatically.

The controller defaults to `controllerDNS.policy: ClusterFirst`. For an explicit
resolver, set `policy: None` and supply `controllerDNS.config.nameservers`, with
optional searches and options. This controls only the controller pod. Projects
continue to use the validated DNS discovery configuration above.

`controllerImage`, `workspaceImage` and optional `proxyImage` accept complete
registry/repository references with immutable SHA-256 digests. The proxy image
defaults to the controller image. `controllerImagePullSecrets` accepts existing
Secret names in the control namespace for controller image pulls. Project
images must be accessible through node registry credentials or another
operator-managed registry configuration. The controller does not copy registry
credentials into project namespaces. Image pulls happen through the kubelet;
project HTTPS egress permissions do not grant registry pull authentication.

For an in-cluster Forgejo TLS backend, `proxyGateway.namespace`, `selector`,
`port` and optional `disableSNIFilter` define the inspecting proxy's explicit
backend exception. Leave `proxyGateway` empty if none is needed. The legacy
`config.PROXY_GATEWAY_JSON` remains accepted, but cannot be combined with the
structured setting. Keep SNI filtering enabled unless your qualified networking
requires the documented exception. Worker access to the backend remains denied.

Set `routing.enabled: true`, `routing.hostnames` and `routing.parentRefs` to
render an HTTPRoute for an existing Gateway. This does not install a Gateway or
configure its certificates. The Gateway must accept the route and cross-namespace
attachment. Align the hostname with `config.PUBLIC_ORIGIN` and the OIDC callback.
With routing disabled, expose the controller ClusterIP Service through your own
ingress. Set your Forgejo origin, owner, public origin and OIDC values under
`config`; there are no built-in private deployment hostnames.

Set `namespace.create: true` when the rendered manifest should create the
control namespace. `namespace.labels` defaults to restricted Pod Security
Admission. Helm users may create and label the namespace separately. Inspect
namespace ownership before adopting an existing installation.

Values validation rejects unknown placement fields, invalid gateway ports,
missing route destinations and `dnsPolicy: None` without a resolver. Runtime
configuration also rejects malformed placement JSON. These controls preserve
the isolation settings; configuring another cluster is not evidence that its
runtime or networking has passed qualification.

## Vendor telemetry

`VENDOR_TELEMETRY_ENABLED` defaults to `true`. Set it to exactly `false` on the
controller (Helm: `config.VENDOR_TELEMETRY_ENABLED: "false"`) to disable the
built-in vendor collection routes. Invalid values refuse startup. Reconciliation
updates existing project proxy policies and Cilium hosts and restarts their
proxies; workers and their volumes stay in place. The switch is carried in the
mounted proxy policy, so a broad per-cell rule cannot re-enable a recognized
collection route while it is disabled. A checkpoint on the policy ConfigMap
retries the proxy restart after interruption.

The fixed catalog allows **POST** only:

| Provider | Host | Path prefix |
| --- | --- | --- |
| OpenAI | `ab.chatgpt.com` | `/` (dedicated vendor analytics host) |
| Anthropic | `api.anthropic.com` | `/api/event_logging` (including versioned batch paths) |
| Anthropic operational events | `http-intake.logs.us5.datadoghq.com` | `/api/v2/logs` |
| Anthropic error reports | `browser-intake-us5-datadoghq.com` | `/api/v2/logs` |

Cursor and Grok continue using their existing brokered model hosts, live grants
and bearer-token checks. Other collection hosts remain subject to ordinary
operator rules. The catalog grants no model inference, account login, renewal,
updater, wildcard domain or direct-worker network access.
The proxy still inspects HTTPS and checks upstream TLS.

The two Datadog intake hosts are shared infrastructure. These rules do not
authenticate an Anthropic tenant or filter telemetry bodies.

This is permission to send data outside the cluster. Vendor clients and their
privacy/analytics settings determine the payload; the proxy does not prove it
contains no prompts, filenames or personal data. Allowing egress does not force
a client to collect data or override a client opt-out. Disabling the switch
removes the catalog allowance, rather than erasing data already received.

Sources checked 2026-11-04: [OpenAI metrics controls](https://learn.chatgpt.com/docs/config-file/config-advanced#metrics),
[Anthropic network requirements](https://code.claude.com/docs/en/network-config#network-access-requirements).
Anthropic's two Datadog log paths and versioned event path were also observed in
the installed vendor binary. Collection must still be qualified with the pinned
workspace clients; offline route tests alone do not show successful delivery.

## Go workspace toolchain

The workspace target copies the full pinned Go 1.27.3 distribution, including
`go` and `gofmt`, to `/usr/local/go` and puts its bin directory on PATH.
The image build runs `apc-check-go-toolchain` after switching to UID/GID 1000.
It formats Go source and compiles/runs a small standard-library test with module
and toolchain downloads disabled, using disposable writable caches. A missing,
incompatible or unusable compiler/formatter fails the workspace build.

After launching a cell, run `go version` and `apc-check-go-toolchain` in its
terminal. This verifies the actual mounted worker environment as well. Project
dependency downloads still require approved registry access; this check itself
needs no network. The small controller/voice runtime targets are separate from
the coding workspace and do not need a compiler.
