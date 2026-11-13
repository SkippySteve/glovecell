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

Each entry may name a `providerClassName`; without one it uses the
provider's default class, which matches the behaviour described here (see
[provider and forge classes](classes.md)). The controller refuses an unknown or
repeated name, an unknown or unsupported class, and any opt-in when the
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
- **The access token is shared, unless cell tokens are on.** Without
  `PROVIDER_CELL_TOKENS`, all opted-in projects receive the same
  provider access token, because there is one login per provider. A worker that
  copies the token can use it until it expires (Grok: its lease, within minutes
  of the owner's renewal; Cursor: the access JWT's own lifetime, which can be
  long). Inside the sandbox it is useless after revocation, because the proxy
  checks the current grant on every request; outside, it is only as contained as
  the project's other egress. Grant providers only to projects whose other
  HTTPS rules you would trust with that token. Rotating the owner's login (a
  new sign-in) invalidates a copied Cursor token. Cell tokens (below) remove
  the real token from workers.
- **Cell tokens (`PROVIDER_CELL_TOKENS=grok,cursor`, off by default).** The
  worker's `provider-access` then carries a token unique to its cell: an HMAC
  of the provider and the real token, keyed from the cell's own inspection CA
  key. A Cursor JWT keeps its header and claims and gets a new signature; Grok
  gets an opaque `apc-cell-…` token. Only the cell's proxy holds the real
  token (`proxy-ca/provider-access`, with `cellToken`); it accepts only that
  cell's token, refuses the real one from inside, and forwards the real token
  upstream. A copied cell token is useless at the provider and in any other
  cell, and changes with every real token. A cell whose key cannot be read
  gets the provider disabled, never the shared token. Turn it on only after
  every cell's proxy runs an image that knows `cellToken` (an older proxy
  refuses the unknown field, so the provider stops working in that cell
  until its proxy restarts on the new image), and qualify each provider's CLI
  with it on one disposable cell first: a CLI that verifies its token locally
  would refuse the stand-in.
- **The broker can run commands in the owner pods.** Scope its `pods/exec` to
  exactly those pods by name and add an admission policy that refuses its exec
  anywhere else (the reference deployment does both).

## Deployment

### With the Helm chart

```yaml
providerBroker:
  enabled: true
  providers: [grok, cursor]   # only the subscriptions you have
```

This adds one namespace (`<release namespace>-providers`, or
`providerBroker.namespace`) with:

- `owners-0`, the offline owner (a one-replica StatefulSet, so its name is
  fixed): the workspace image running `sleep`, no service account token, a
  Cilium policy that denies all traffic, and one retained volume per listed
  provider (`<provider>-identity`, kept on `helm uninstall`;
  `providerBroker.owner.existingClaims` adopts claims you already have);
- `provider-broker`, the controller image in lease mode, allowed to reach only
  the API server and the listed providers' token endpoints;
- three admission policies: the broker may exec only into `owners-0` and its
  `<provider>-login` pods (never attach or port-forward); the only pods it may
  create are those login pods with the chart's template (workspace image, no
  token, emptyDir only, no host access); and the controller may not create,
  change, exec or delete anything in this namespace. Its only rights here are
  `get` on `provider-leases` and calls to the sign-in API.

The chart sets the controller's `PROVIDER_LEASES_SECRET` itself (leave
`config.PROVIDER_LEASES_SECRET` empty) and creates default provider classes
only for the listed providers.

**Sign in** once per provider, and again whenever the broker log says a login
needs renewal. There are two ways; both end in the same checked install into
the owner (the broker validates the file, refuses a different Cursor account
unless you choose to replace it, clears any interrupted renewal first, writes
the file atomically and reads it back through the renewal path before saying done).

1. **Console (Connect).** Open any sandbox, **Credentials**, *Your
   subscription logins*, **Connect**. The broker starts a short-lived
   `<provider>-login` pod (the workspace image, no service account token,
   emptyDir volumes only, no login volume, network only to that provider's
   sign-in hosts), runs the provider's own sign-in (`grok login
   --device-auth`, `cursor-agent login`) and the console shows its link, plus
   Grok's code. Approve in your browser; the broker copies the new login from
   that pod into the owner and deletes the pod. One sign-in runs at a time and
   each ends after 15 minutes. Only a signed-in OIDC operator can start one or
   see its code. This gives Glovecell its own session, separate from any on
   your machine. `providerBroker.consoleSignIn: false` turns this path off.
2. **Copy from your machine.** If the provider's CLI is already signed in on
   your computer:

   ```sh
   agent-project-controller provider import grok     # ~/.grok/auth.json
   agent-project-controller provider import cursor   # ~/.config/cursor/auth.json
   # --file PATH, --namespace <release namespace>-providers, --replace-account
   ```

   It checks the file locally, hands it to the broker as a one-shot Secret
   `provider-import-<provider>` (with your own kubeconfig), waits for the
   broker to install it (normally within 30 seconds) and removes the Secret
   whether it worked or not. Nothing keeps running on your machine.
   **Caveat:** your machine and Glovecell then share one sign-in session. If
   the provider rotates its refresh token, whichever side renews first can sign
   the other out: your local CLI asks you to sign in again, or the broker log
   reports `credential owner renewal unavailable` and you import or Connect
   again. Connect avoids this.

The broker's sign-in API listens on 8443 inside the cluster. It accepts only
the controller's ServiceAccount, through a projected token with its own
audience checked by TokenReview (like the forge agent), and network policy
admits only the controller's pod. The controller relays
`GET/POST/DELETE /api/v1/provider-logins[/{provider}]` from the console and
logs who started or canceled each sign-in.

### By hand

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
