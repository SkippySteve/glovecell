# Automatic T3 Connect environments

With T3 Connect enabled, the Go controller starts a stock T3 v0.0.45 server in each ready project sandbox and registers its environment with one T3 account. Sign in to that account once on each phone, browser or desktop. New projects appear in the account's environment catalog. A phone does not need a desktop relay.

Project files, Git worktrees, terminals and provider processes stay inside their project's sandbox. The trusted controller runs the tunnel connectors and holds the T3 OAuth credential and Kubernetes credentials. Workers receive their own environment credential and cloud verification key, without the account's OAuth access or refresh token. T3 remains bound to worker loopback; authenticated Kubernetes exec carries HTTP and WebSocket streams. No worker ingress or Kubernetes credential is added, and project egress policy stays in force.

The [single-page diagrams](sandbox-workflow.html) show the network and workflow. This integration uses stock server APIs rather than modified T3 app builds. T3 Connect is a separate account login from the controller dashboard's OIDC login.

## Controller setup

Build the `t3-controller` Docker target for the controller and the `workspace` target for project workers. Both contain pinned stock T3. The controller target also contains the pinned cloudflared release used by T3 v0.0.45. Continue using the smaller `runtime` target for inspecting proxies.

Provision an existing PVC in the control namespace for the controller's account state. Enable the chart settings:

```yaml
t3Connect:
  enabled: true
  existingClaim: t3-controller-account
  relayURL: https://relay.t3.codes
```

The chart mounts the claim only in the controller at `/var/lib/apc/t3-connect`. It also grants the controller `pods/exec`, adds writable temporary storage, and requires an HTTPS relay origin. Project storage is independent. Allow enough controller memory for the controller and one cloudflared process per active project.

Authorize the account using the stock CLI in the controller pod:

```sh
kubectl -n CONTROL_NAMESPACE exec -it deploy/controller -- \
  /opt/t3/t3 connect login --headless --base-dir /var/lib/apc/t3-connect
```

Open the printed sign-in URL on your device and confirm its code. This is the only controller account authorization needed. Keep credential files private; do not copy them into project volumes or include them in logs. The stock CLI handles credential refresh under its existing cross-process lease. If the grant is revoked, authorize the controller again. Projects stay pending while authorization is missing or unusable.

## Add and open a project

Add a repository in the controller as usual. After the inspected clone and sandbox are ready, the controller starts T3, obtains a project-signed link proof, records registration intent, and requests the environment link. It installs only environment-specific configuration in the worker, starts the trusted connector, and verifies that the remote HTTPS endpoint returns that project's identity. Only then does the controller report Ready.

Open `https://app.t3.codes`, or a stock T3 app, sign in to the controller's T3 account, and select the environment named for your project. The dashboard's Open T3 Connect link opens the hosted client. It does not claim to select a specific environment through an undocumented deep link. On desktop, disable the local environment in Settings → Connections if all work should run remotely.

T3 Connect creates managed HTTPS hostnames. New DNS records can take time to become visible to recursive resolvers. Registration or DNS failures remain visible as pending status and retry automatically. A saved link alone is not evidence that a server is reachable.

## Pause, resume and remove

Pause closes the controller's connector and suspends project processes while retaining project files, device state and the environment's registration. Resume restores the same environment identity and verifies its endpoint again.

The environment ID is the workspace's Kubernetes UID, stored atomically in project T3 state. The controller pins the T3 account and environment signing key in an owned control-namespace Secret. It refuses a changed identity or signing key. A legacy, manually launched T3 environment with a different ID needs explicit migration; it is not silently adopted.

Registration intent is durable before the external link request. Removal stops the connection, revokes the T3 environment and repository access, verifies absence in T3's account catalog, and then verifies owned namespace and storage cleanup. A failed DELETE response is not enough to infer failure or success: an authoritative catalog check under the pinned account must confirm absence. Failed revocation, a missing ledger after recorded intent, or a disabled registration manager with existing registration history blocks completion. Retries can recover from a successful external operation whose durable update was interrupted.

## Optional private controller access through Tailscale

An installed Tailscale Kubernetes operator can expose the controller privately:

```yaml
tailscaleIngress:
  enabled: true
  hostname: projects-private
```

Set the controller's `PUBLIC_ORIGIN` to that endpoint's HTTPS origin and register its `/auth/callback` URI with the identity provider. Tailnet ACLs restrict network access; dashboard OIDC still authorizes project operations. The chart does not install the operator or modify tailnet ACLs.

Tailscale is an optional private transport. It does not create T3 environment entries. T3 Connect supplies automatic account registration and normally works without Tailscale on the receiving device. Direct pairing through a private project gateway requires separate endpoint and pairing support; this controller Ingress does not expose workers directly.

## Qualification

Normal `make test`, `make vet` and `make chart-check` run without T3 account access or cluster deployment. Tests cover proof rejection, credential permissions, unsafe endpoints, missing ledgers, unverified revocation, disabled-manager removal, and interrupted durable updates.

The opt-in local qualification creates two disposable containers with networking disabled and fresh local Git repositories. It runs the actual controller connection code against stock T3, checks both environments in the same account catalog, verifies remote identity and absent owner credentials in workers, reconnects after a pause, and revokes both environments before deleting their containers:

```sh
APC_T3_CONNECT_QUALIFY=true \
APC_T3_CONNECT_WORKSPACE_IMAGE=your-disposable-workspace-image \
APC_T3_CONNECT_ACCOUNT_DIR=/private/controller-t3-account \
APC_T3_CONNECT_CONNECTOR=/path/to/verified/cloudflared \
APC_T3_CONNECT_BINARY=/path/to/stock/t3 \
go test -v -count=1 -timeout 20m ./internal/t3 \
  -run '^TestManagedConnectTwoDisposableSandboxes$'
```

This account directory must belong to the qualification operator. The test never deploys or grants access to an existing Git repository. Cluster qualification must use disposable projects and repositories.

The initial worker provider profile still enables Codex through its access-only adapter. Grok Build and Cursor credentials, GitLab repository authorization, and optional Forgejo imports are separate additions. This transport does not enable those features implicitly.

See [stock T3 remote access](https://github.com/pingdotgg/t3code/blob/v0.0.45/docs/user/remote-access.md) and [Tailscale operator ingress](https://tailscale.com/docs/kubernetes-operator/ingress).
