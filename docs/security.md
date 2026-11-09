# Security boundaries

## Trusted control plane

The controller holds a Forgejo administrative credential and cross-namespace Kubernetes permissions. A compromise of this component is more serious than compromise of one project environment. Run it separately from agents, limit administrative access to its namespace and protect Secret storage and backups. The dashboard never returns credential material.

Each workspace gets a unique non-admin Forgejo user and repository-scoped token. Only that token and username are mounted in the worker. Its administrative password stays in the control namespace. Inspection private keys are mounted in the trusted proxy, while workers receive only the public CA certificate. Agent containers have no Kubernetes token and use an operator-qualified runtime.

## Authorization

OIDC login requires a verified token and an explicit operator allowlist. Operators share one administration boundary. Email addresses and proxy-injected identity headers do not confer authority. Group changes at the identity provider take effect on the next login; existing application sessions expire within 15 minutes. Local sign-out does not terminate the provider's own session. Login transaction and application sessions live in memory, so rolling multi-replica OIDC operation is unsupported.

Project API mutations from browser credentials require the configured Origin and JSON content type. The optional existing-sandbox section accepts the backend's bounded POST forms after the same console authentication and Origin check, and retains its signed CSRF cookie and form token. See [console integration](console.md) for explicit delegation and the required trusted backend network boundary. Automation bearer credentials are independently configured and have operator-level authority. They are not OIDC machine tokens or project credentials.

Requested-by identity is copied into immutable audit records. Kubernetes clients can supply that annotation themselves, so it is attribution from the submitted request, not cryptographic proof of the Kubernetes caller. Use Kubernetes API audit logs for direct-client attribution. The controller's own broad RBAC and cluster administrators can delete audit objects; export or back them up if independent audit retention is required.

## Main protection

When main protection is absent, the controller creates a rule that forbids direct pushes and restricts reviewed merges to the configured owner. An existing `main` rule that falls short is tightened in place: the controller patches only the failing settings, each toward the stricter value (direct and force pushes off, rules applied to administrators, at least one approval, merges allowed only by the configured owner), and keeps every other setting. It never weakens a rule, and it reads the rule back before any grant. If Forgejo rejects or ignores the change, preparation stops with the remaining shortfall for operator review. Ambiguous wildcard rules that might overlap main require operator review. Qualification checks actual Git push, force-push and deletion denial and API merge denial.

Agents can perform operations supported by their Forgejo repository write permission. This is broader than the old broker's branch-ownership model. Protect additional release branches or tags explicitly if they matter. Main protection does not stop an agent from damaging its working branches, creating misleading PRs or disclosing source through permitted writes.

### GitHub

GitHub mode does not create or replace branch protection. Grant refuses the repository unless classic protection on `main` enforces rules for administrators, requires at least one approving review, restricts who can push, excludes this GitHub App from that allow list, and leaves force-push and branch deletion disabled. A missing rule or a ruleset-only repository is a refusal. The controller will not weaken an existing rule to make a grant succeed.

Workspace credentials are installation access tokens for one repository. The requested permissions are contents write, pull requests write, and metadata read. The controller drops a token whose response names a different repository, selects every repository, or carries any other permission, and it revokes that token before returning the error. Tokens expire in about one hour. The process reuses a stored token only while its recorded expiry is more than 20 minutes away and GitHub still accepts it. Otherwise it revokes the stored token and mints a replacement. Restarting the controller forgets those expiry records, so the next grant rotates the stored token.

GitHub does not list live installation tokens. A token that is issued and then lost before the ledger save, including a controller token held only in memory, stays valid until GitHub expires it. Removal confirms the stored workspace token and any token this process still remembers. It cannot confirm a token lost in a crash. The Forgejo host deletes orphaned personal tokens before issuing another; GitHub cannot do that.

## HTTPS enforcement

Cilium restricts workers to their proxy and necessary DNS resolution. The proxy allows exact configured hosts, HTTP methods and literal path prefixes. DNS policy permits necessary exact names rather than arbitrary queries. Upstream TLS certificates are verified normally. Private upstream addresses are prohibited except for the operator-configured Forgejo host. GitHub mode leaves that exception empty, so `github.com` and `api.github.com` must resolve to public addresses.

Project pods use an explicit CoreDNS service rather than inheriting the node-local resolver. This keeps their resolver selection consistent with endpoint-scoped DNS policy. Node-local interception and service routing still require dataplane qualification. The controller rejects unsupported resolver settings and does not grant unrestricted host DNS access.

The current implementation supports CONNECT on port 443. Negotiated HTTP/2 and HTTP/1.1 are both inspected with the same host, method and path rules. It rejects escaped paths, alternate authorities, protocol upgrades and requests larger than 128 MiB. WebSocket and certificate-pinned clients are not supported. There is no opaque tunnel fallback.

Request bodies are forwarded, not classified or filtered. Allowed API requests, Git pushes and other permitted writes can carry source code or secrets. HTTPS filtering does not provide a source-code confidentiality guarantee. Telemetry endpoints are also possible outbound data channels.

Policy creation and a Cilium CRD check do not prove enforcement. Broad cluster policies, host networking, node-local DNS and runtime differences must be checked in an actual cluster. Creating a pod before network enforcement is ready must not allow unfiltered bootstrap traffic. The release qualification requires an independent bypass test.

The experimental `network-policy` provider keeps that fail-closed boundary only for clusters whose CNI actually enforces standard NetworkPolicy. Workers have no DNS. Their proxy name is a host alias for an allocated ClusterIP, so a mutated alias or an unallocated Service blocks provisioning. Proxy egress is TCP 443 to IPv4 public space, with reserved, private, and declared cluster prefixes excluded, and to IPv6 global unicast `2000::/3`, with documentation and in-range cluster prefixes excluded. IPv4-mapped IPv6, unique-local, and link-local destinations stay outside that allow. CoreDNS and an optional gateway peer are separate rules. Gateway rules require `disableSNIFilter` because this API cannot match TLS names. A compromised inspecting proxy can still open those proxy-only destinations; worker pods cannot. Additive allow policies in the project namespace are rejected. Policies installed outside that namespace, service translation that lands on an unexpected address, and a CNI that ignores NetworkPolicy are outside the controller's view. The provider acknowledgement flag does not change any of those facts.

Each project has an inspection CA with a one-year lifetime. Leaf certificates renew in the running proxy. Set the annotation `projects.agentwork.dev/ca-generation` to the next integer to mint a new CA, update the worker trust bundle, and restart the proxy and worker. The private key stays in the control ledger and the proxy. The generation cannot skip or go backwards. Removal still requires a new project before access can be granted again.

## LAN host grants

LAN host grants are the only direct worker egress besides the inspecting proxy
and DNS for the proxy name. They are off unless the operator sets
`LAN_GRANT_ALLOWED_CIDRS`. A grant is one IPv4 address and an explicit list of
TCP ports, enforced by CiliumNetworkPolicy `lan-hosts` in the project
namespace: `toCIDR <address>/32` with those ports, selecting only that
project's worker pod. It has no ingress section, so it adds no inbound access,
and Cilium allow rules are additive, so the worker and proxy policies keep
every other restriction. Traffic to a granted host is not inspected, filtered
by protocol or recorded beyond Cilium's own flow logs. Anything an agent can do
over that TCP port, it can do; an SSH grant with a usable key is a shell on that
host. Grant the narrowest port and use host-side credentials that a project
should hold.

The allowed ranges must be inside `10.0.0.0/8`, `172.16.0.0/12` or
`192.168.0.0/16`, so a grant can never become a direct path to the internet
around the HTTPS proxy. Startup fails when the denylist is empty while grants
are enabled, and with the experimental NetworkPolicy provider, which has no
single-host rule here. The controller refuses node addresses, the denylist,
loopback, link-local and metadata, multicast, reserved and broadcast
addresses on every grant request and every reconcile. A grant that becomes
refused, for example after the operator narrows the ranges, blocks the project,
stops its worker and deletes the policy.

Only OIDC operators change grants; the automation bearer and static login
cannot. The worker has no Kubernetes credential, and project namespaces hold no
role that can write Cilium policies. The policy is labeled with its project
UID; the controller refuses to adopt or delete a `lan-hosts` policy owned by
another project and overwrites any edit to its own. Deployments should add an
admission policy so that only the controller's service account can write
Cilium policies in project namespaces, and only `/32` TCP rules inside the
allowed ranges. Kubernetes administrators and cluster-wide policies remain
outside this check.

Removing a grant deletes its rule. Cilium evaluates egress policy per packet
in the original direction, so established connections are expected to stop;
this has not been qualified on a cluster. Pause the project when a session must
end for certain. Every add and remove is an immutable audit record with the
operator, project, address, ports and time, and the controller records each
applied policy.

## Credentials the proxy injects

Some credentials never enter the worker. The worker sends a placeholder, and
the project proxy replaces it on the way out:

- the GitHub read grant for private repositories;
- the Muse subscription key.

The proxy names each credential with a URI, such as
`ate-secret://cell.agentwork.dev/tls/github-token`, and fetches it through a
provider interface (`internal/credentials`) on every request. A replaced or
revoked Secret therefore applies at once.

The URI syntax and the failure rules copy
[Agent Substrate's credential providers](https://github.com/agent-substrate/substrate/blob/main/docs/guides/egress-credential-injection.md):

| Situation | Proxy answers |
| --- | --- |
| Credential missing (`NotFound`) or not granted to this cell (`PermissionDenied`) | 403 |
| Backend unavailable, timed out, or no provider configured | 503 (retryable) |
| Credential empty, too long, or containing a control character | 503 |

Either way, the request never leaves without its credential.

The built-in provider reads the proxy's own Secret volume, which only the
proxy mounts. It serves only the listed keys, so a URI can't reach the CA key
or other files there. A backend such as OpenBao can implement the same
interface later (the reserved `openbao-broker` ProviderClass driver). It must
check that the calling cell may have the credential: `Request.Cell` names the
cell, and a shared backend refuses requests that leave it empty.

## Brokered subscription providers

`spec.providers` gives a project's worker short-lived, access-only leases for
Grok and Cursor; see [brokered providers](brokered-providers.md). Only OIDC
operators change the opt-in. The provider broker holds `pods/exec` on the
offline owner pods and writes one lease Secret in its own namespace; it has no
rights in project namespaces. The controller copies a lease only into the
projects that opted in, and CiliumNetworkPolicy `providers` lets only that
project's proxy reach the provider's hosts. The proxy admits a provider request
only with the project's current, unexpired grant and a matching bearer token,
and always refuses Cursor's account routes. Revocation removes the grant,
the policy rule and restarts the proxy while the worker keeps running. All
opted-in projects receive the same access token; a copied token stays usable
outside the sandbox until it expires, so grant providers only to projects
whose other egress you would trust with it. Deployments should add admission
policies that keep `providers` proxy-only TCP 443 to the reviewed hosts and
confine the broker's exec to the owner pods.

## Downloads into a sandbox

"Download into sandbox" is off unless `DOWNLOADS_ENABLED=true`. It never
moves bytes over the controller's network path. The worker fetches the file
through its own inspecting proxy, as the worker's user, and writes it itself.

- **One-time, exact grants.** For each URL of an approved redirect chain the
  controller adds a grant to the project proxy: the exact URL (host, escaped
  path and raw query), a mode (`probe` sends HEAD, `fetch` sends GET), the
  SHA-256 of a random 32-byte token, an expiry of 10 minutes and, for fetches,
  a byte limit and timeout. The first request that presents the token consumes
  the grant, whatever happens next. A request for another URL, method or host,
  with another token, or after expiry is refused and does not consume it.
  Grants live only in proxy memory: a proxy restart drops them.
- **The proxy builds the request.** A granted request has no body, and the
  proxy sends only its own `User-Agent`, `Accept` and `Accept-Encoding:
  identity` headers. Worker headers, cookies and the token never reach the
  destination, and no project credential (GitHub, Muse, provider tokens) is
  attached. Granted hosts resolve only to public addresses; the private-host
  exception for configured hosts does not apply. HTTP/2 grant requests are
  refused. Probes never return a body to the worker; a GET used when a server
  refuses HEAD is closed after its headers.
- **Delivery of grants.** The controller execs `agent-project-controller
  download-grant` in the proxy pod; it talks to `127.0.0.1:3129`, which only
  processes in that pod reach. The worker's token travels on the exec stream of
  `agent-project-controller fetch`, never in arguments or the environment.
  Exec stays inside the project's `apc-<uid>` namespace; downloads add no RBAC.
- **Network.** For the duration of one check or download, CiliumNetworkPolicy
  `download-<id>` lets the project's **proxy** pod resolve and reach the exact
  host names of the chain on port 443 (no patterns, no CIDRs). The worker
  policy is unchanged. The proxy still refuses every request without a live
  grant, so the policy alone opens nothing. Policies carry an expiry
  annotation; the controller deletes them when the operation ends and sweeps
  any left over once a minute.
- **Redirects.** A check follows at most 9 redirects (10 URLs) and refuses
  loops, https-to-http downgrades, IP literals, other ports and cluster or
  local names. The Location comes from the proxy's own record, never from the
  worker. A download follows exactly the approved chain; any difference stops
  it before the new URL is requested. Accepting a query-only change on the
  same host and path is an explicit operator choice per download.
- **Writing the file.** The helper writes only below `/workspace` (the home
  directory is `/workspace/home`). It opens every directory from that root
  with `O_NOFOLLOW` and checks it is a directory, so a symlink planted anywhere
  on the path fails the download rather than redirecting it. (gVisor follows a
  final symlink when `O_DIRECTORY|O_NOFOLLOW` are combined, so the helper does
  not use `O_DIRECTORY`.) It refuses an existing symlink, device, FIFO, socket
  or directory at the destination, and an existing file unless overwrite was
  ticked. It writes a temporary file in the same directory, enforces the size
  limit (proxy and helper both count), verifies the optional SHA-256, and
  publishes with a hard link (or `renameat2(RENAME_NOREPLACE)`) so that
  without overwrite it never replaces a file, even one created mid-download.
  On any failure, mismatch or cancellation the temporary file is removed.
- **Who and records.** Only a signed-in OIDC operator can check, start or
  cancel; the automation bearer and static login get 403. The console's
  same-origin and JSON checks apply. Each check, request and outcome is an
  immutable audit record (History) with the actor, the chain, the
  destination, size and SHA-256. Query strings are replaced by a SHA-256
  prefix in every record and log line; only the operator's own console view
  shows them, masked until "Show query strings" is ticked.

**Uploads** (`agent-project-controller put`) reuse the helper's path rules,
temporary file and atomic publish. The file comes from the operator's browser
through the console: it is never fetched by anything in the cluster, so no
grant, proxy rule or network policy is involved, and whatever credentials the
operator used to obtain it stay in the browser. The controller streams each
part straight into the helper's stdin through a 64 KiB buffer; nothing is
stored in controller memory or on its disk. The helper is told the exact size
up front (`--size`, checked against `--max-bytes`, and the controller refuses
anything above the cap before starting it) and keeps nothing unless exactly
that many bytes arrive: a short stream, a cancel, two idle minutes or
SIGTERM/SIGHUP/SIGINT remove the temporary file. A broken stdout no longer
kills the helper (it ignores SIGPIPE), so cleanup still runs. Raw parts are
accepted only as `PUT` with `application/octet-stream` and `X-Upload-Offset`
from a same-origin, signed-in OIDC operator; both headers force a CORS
preflight, so a cross-site page cannot send one. Records and logs carry the
file name, size, SHA-256, destination and outcome, never contents.

sandbox-control workspaces follow the same rules with sandbox-control as the
proxy: grants live in its egress proxy, bound to the workspace and its current
pod UID, and sandbox-control execs the helper (this binary, installed as
`/usr/local/bin/sandbox-download`) through the Kubernetes WebSocket exec API.
Its Role grants `pods/exec` only on each enrolled workspace pod by name, and an
admission policy allows its exec only for that pod's download container with
the command exactly `[/usr/local/bin/sandbox-download, fetch]` or the upload
shape `[/usr/local/bin/sandbox-download, put, --home=…, --dest=…, --size=N,
--max-bytes=N, --sha256=[hex], --overwrite=true|false]` (fixed order), stdin and no
TTY; attach and port-forward are refused. The helper accepts a home directory
outside `/workspace` (`/home/agent` there) and walks every directory from `/`
without following symlinks.

Limits: the agent in the sandbox can see the file, and the token while the
helper runs if it can read that process's memory. A sandbox process that
races the helper with a stolen token can at most fetch the same approved URL
with the proxy's fixed headers. The file is whatever the server sends;
the SHA-256 field is the integrity check.

## Removal and recovery

The controller persists the external identity before any grant. If it crashes after token creation but before saving the token value, the next attempt removes orphaned tokens before issuing another. Deleting the credential ledger manually breaks this recovery guarantee.

Removal requests suspend the Sandbox and terminate owned pods, revoke collaborator access, delete tokens and disable the Forgejo identity. An unavailable Forgejo instance leaves removal pending. Kubernetes resources and storage are retained until external revocation succeeds. The namespace is then deleted, and any matching persistent volume keeps cleanup pending until the storage provisioner removes it. Finalizers prevent normal object deletion from skipping this process.

GitHub removal deletes the stored installation token and reads the repository again with that token. HTTP 401 or 404 confirms revocation. HTTP 200 leaves removal pending. There is no GitHub user to disable. The repository, its branches, and its pull requests stay. An installation token that was never written to the ledger can remain usable for up to about an hour.

After verified cleanup the controller removes stored token, password and private CA material, keeping a small revoked tombstone. Disabled Forgejo users preserve PR attribution and are intentionally retained. Immutable audit records have separate retention. Removing a project does not delete the original Forgejo repository or its branches and PRs.

Pause stops the worker and inspecting proxy but preserves repository credentials and files. HTTPS access changes require all their pods to be gone and the project to remain paused until the controller records the new policy. It is not revocation. Removal is terminal; regranting access requires a new workspace and identity. Requests are asynchronous: only an observed `Removed` state confirms this controller's cleanup checks finished.

Volume and namespace deletion are infrastructure checks, not proof of physical secure erasure. Snapshots, backups, external resources and information already copied elsewhere are outside automatic cleanup. Restrict these separately and use encryption or storage-specific erasure where required.

## Reporting a vulnerability

Report a vulnerability privately to steve@aimingupward.com. Do not include credentials, tokens or a proof-of-concept exploit in a public issue. The same contact is in the repository `SECURITY.md`. GitHub private vulnerability reporting applies when this commit is mirrored there. The Git mirror does not carry Forgejo issues or pull requests.

The [DNS and direct-IP qualification procedure](network-qualification.md) includes
an executable probe matrix with independent positive controls. Passing its IPv4
probes alone does not establish isolation: correlate denials with the dataplane
and prove effective FQDN lease expiry before accepting APC #107.
