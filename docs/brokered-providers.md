# Brokered subscription providers

A project sandbox can run the stock Grok (`grok`) and Cursor (`cursor-agent`)
command line tools against the operator's own subscription. The login never
enters the sandbox. Each project opts in per provider, the default is off, and
an operator can revoke one provider for one project while it runs.

GitHub has its own broker, where the credential never enters the sandbox at
all and the project proxy enforces a per-request policy: see
[GitHub brokering](design/github-brokering.md).

## Parts

```
offline owner pods (one per provider; login on a volume; no network, no token)
   ^ pods/exec: read the login, write it back after a refresh
provider broker (lease mode) --HTTPS--> the provider's token endpoint
   | writes one 5-minute access lease per provider
Secret <broker namespace>/<leases>          (the broker's only output)
   | get, on every reconcile
controller ---> per opted-in project, in its own namespace:
                  model-access/provider-access   -> worker /run/model (launchers)
                  proxy-ca/provider-access       -> project proxy (gateway check)
                  proxy-ca/brokered-providers    -> project proxy (opt-in list)
                  CiliumNetworkPolicy providers  -> proxy may reach the hosts
```

- **Owner pod.** One per provider. It runs `sleep`, mounts the provider's
  login on a volume, has no service account token and a deny-all network
  policy. Sign-in happens once, by the operator, in a temporary login pod that
  mounts the same volume.
- **Provider broker.** The `provider-broker` command with
  `PROVIDER_BROKER_LEASES` set ("lease mode"). Every 30 seconds it reads each
  owner's login through `pods/exec`, refreshes it at the provider when it is
  within 10 minutes of expiry (the journal makes an interrupted refresh
  recoverable without replaying it), and publishes only the access token, with
  an expiry capped at five minutes, to its lease Secret. It pins the Cursor
  account on first use. It needs `pods/exec` on the owner pods, `create` on
  Secrets and `get`/`patch` on its journal and lease Secret, all in its own
  namespace, and nothing in project namespaces.
- **Controller.** With `PROVIDER_LEASES_SECRET=<namespace>/<name>` it reads the
  leases on every reconcile (about every 10 seconds) and copies the lease of
  each provider in a project's `spec.providers`, and only those, into that
  project's Secrets and policy. A lease that is missing, malformed, carries
  refresh material or expires within a minute becomes `{"enabled": false}`.
- **Worker launchers.** `grok` and `cursor-agent` in the workspace image read
  `/run/model/provider-access` (see [provider launchers](provider-launchers.md)).
  They refuse sign-in, updates, credential overrides and alternate endpoints.
- **Project proxy.** A request to a provider host is admitted only when the
  provider is in `brokered-providers`, its grant is enabled and valid for over a
  minute, the method and path match the reviewed route, and the
  `Authorization` header carries the current lease token. Cursor's `/auth` and
  `/oauth` routes are always refused. The proxy rereads both files on every
  request.

## Opting in

The console's **Credentials** section of a project sandbox lists the offered
providers with **Enable** and **Revoke**. Only a signed-in OIDC operator can
change them; the automation bearer and static login get 403. The API is
`POST /api/v1/workspaces/{name}/providers {"resourceVersion", "name"}` and
`DELETE /api/v1/workspaces/{name}/providers/{provider} {"resourceVersion"}`.
`GET /api/v1/provider-leases` reports which providers currently have a usable
lease, never token material. Each change writes an immutable audit record.

`spec.providers` can also be set directly:

```yaml
spec:
  providers:
  - name: grok
  - name: cursor
```

The controller refuses an unknown or repeated name, and any opt-in when the
controller has no lease source or the network provider is not Cilium. A
refused opt-in blocks the project, like an invalid HTTPS rule, and withdraws
every brokered grant.

## Revocation

Removing a provider from `spec.providers` (console **Revoke**):

1. removes its entry from `model-access` and `proxy-ca` and the host from the
   `providers` policy on the next reconcile, normally within seconds;
2. restarts only the project's proxy, so an open provider connection cannot
   outlive the revocation; the new proxy reads the revoked state at start;
3. stops a running launcher within about a minute (Kubernetes updates the
   mounted Secret; the launcher checks every second).

The worker keeps running; its volume, Git identity and other providers stay.
The controller deletes a `model-access` Secret it created only for brokered
grants once the project opts out of every provider. An operator-created
`model-access` (for example a Codex grant) keeps its other keys.

Every other path also ends access within five minutes: stopping the broker,
deleting the lease Secret, or the owner losing its login lets the leases run
out. Pausing or removing the project stops the worker.

## What this does and does not protect

- **A project only receives what it opted into.** Grants live in the project's
  own namespace; no other project's worker or proxy mounts them, and workers
  have no Kubernetes token. A project that opted into Grok has no Cursor entry
  and its proxy cannot reach Cursor's hosts.
- **Refresh material never leaves the owner and broker.** The controller,
  proxy and launcher reject any grant with refresh fields.
- **The access token is shared.** All opted-in projects receive the same
  provider access token, because there is one login per provider. A worker that
  copies the token can use it until it expires (Grok: its lease, within minutes
  of the owner's renewal; Cursor: the access JWT's own lifetime, which can be
  long). Inside the sandbox it is useless after revocation, because the proxy
  checks the current grant on every request; outside, it is only as contained as
  the project's other egress. Grant providers only to projects whose other
  HTTPS rules you would trust with that token. Rotating the owner's login (a
  new sign-in) invalidates a copied Cursor token.
- **The broker can run commands in the owner pods.** Scope its `pods/exec` to
  exactly those pods by name and add an admission policy that refuses its exec
  anywhere else (the reference deployment does both).

## Deployment

`config/samples/provider-broker.yaml` is a generic example: namespace,
ServiceAccount, Roles, the broker Deployment and one owner pod. Replace the
image digests and owner names, sign each provider in once with a temporary
login pod that mounts the owner's volume, then set
`PROVIDER_LEASES_SECRET` on the controller and restart it. The controller's
service account needs `get` on the lease Secret. Older images ignore
`PROVIDER_LEASES_SECRET` and `spec.providers`, and a lease-mode broker
Deployment that still carries the single-workspace variables runs on an older
image without granting anything, so the configuration can be rolled out before
the image.

Lease mode replaces the single-workspace mode for Grok and Cursor: with a
lease source configured, the controller owns the `grok` and `cursor` entries of
every project's `provider-access` document and removes them from projects that
did not opt in.
