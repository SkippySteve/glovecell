# Database

The controller keeps its operational records in PostgreSQL (decision D13 item 2). PostgreSQL is required.

| In PostgreSQL | Stays a custom resource |
| --- | --- |
| Exact request approvals | The cell (`ProjectWorkspace`) |
| Access requests, pending and decided | `AccessProfile`, `AccessGrant` |
| History: lifecycle, policy changes, grants, decisions | `OrchestratorClass`, `ProviderClass`, `ForgeClass` |
| Per-request proxy summaries | `Kit` |

The left column holds records that are numerous or grow without bound; as custom resources they would clutter etcd and `kubectl` output. The right column holds the few declarative objects that GitOps, admission policies and RBAC need to see. Kubernetes Events remain for `kubectl describe` only: they expire after about an hour (`--event-ttl`), so they are never the history.

## Installing

The chart's `database.mode` chooses where PostgreSQL comes from.

### `cloudnativepg` (default)

The chart renders a [CloudNativePG](https://cloudnative-pg.io) `Cluster` (`postgresql.cnpg.io/v1`). **Install the CloudNativePG operator first**: the chart doesn't install it, and without its CRDs the release fails. The operator then creates:

- the PostgreSQL pods and the services `<cluster>-rw`, `-ro` and `-r`;
- the Secret `<cluster>-app`, which holds the connection URI (`uri`) for the database owner;
- the Secret `<cluster>-ca`, which holds the CA (`ca.crt`) that signs the server certificate.

The controller mounts only `uri` from the first Secret and only `ca.crt` from the second; it never mounts the CA's private key.

| Value | Default | Meaning |
| --- | --- | --- |
| `database.cloudnativepg.name` | `<release>-db` | Cluster name; also the Secret name prefix. |
| `database.cloudnativepg.instances` | `1` | One instance is enough to start with. More instances add streaming replicas and turn on the operator's PodDisruptionBudget. |
| `database.cloudnativepg.imageName` | operator default | Digest-pinned PostgreSQL image. |
| `database.cloudnativepg.database`, `.owner` | `apc` | `bootstrap.initdb` database and owner. |
| `database.cloudnativepg.storage.size`, `.storageClass` | `5Gi`, cluster default | PVC size and class. |
| `database.cloudnativepg.resources` | 100m / 256Mi, limit 1Gi | PostgreSQL pod resources. |
| `database.cloudnativepg.monitoring.enablePodMonitor` | `false` | Needs the Prometheus operator's PodMonitor CRD. Also opens port 9187 in the NetworkPolicy. |
| `database.cloudnativepg.backup.*` | off | See [Backups](#backups). |
| `database.cloudnativepg.operator.namespace`, `.podLabels` | `cnpg-system`, `app.kubernetes.io/name: cloudnative-pg` | Where the operator runs, for the NetworkPolicy. |
| `database.cloudnativepg.extraIngressFrom` | `[]` | Further NetworkPolicy peers that may reach port 5432, for example a backup job. |

The Cluster refuses connections without TLS (`pg_hba: hostnossl all all all reject`) and has no superuser access. A NetworkPolicy named after the Cluster lets only these reach PostgreSQL:

- the controller's pods, on port 5432;
- the operator, on ports 5432 and 8000;
- the Cluster's own instances, on ports 5432 and 8000, for replication.

### `external`

Set `database.mode: external` and `database.external.existingSecret` to a Secret in the release namespace with a `postgres://` or `postgresql://` URI under `uriKey` (default `uri`). If the server's certificate isn't publicly trusted, put its CA in the same Secret and name the key in `caKey`. You manage that server's network policy and backups.

### Connection and TLS

| Variable (the chart sets it) | Meaning |
| --- | --- |
| `DATABASE_URL_FILE` | File holding the connection URI. |
| `DATABASE_CA_FILE` | CA bundle that is the only trusted root. If unset, the system roots are trusted. |
| `DATABASE_MAX_CONNS` | Pool size, 2 to 100 (default 10). |
| `HISTORY_RETENTION`, `DECIDED_REQUEST_RETENTION`, `PROXY_SUMMARY_RETENTION` | Retention, see below. |

The controller always connects with `sslmode=verify-full`. It refuses a URI that asks for any other `sslmode` or that names a different `sslrootcert`, and its errors never repeat the URI or password. CloudNativePG's server certificate covers the `<cluster>-rw.<namespace>` host name that its `uri` uses, so verify-full works without extra settings.

Without `DATABASE_URL_FILE` the controller still starts, for compatibility with older manifests. In that case:

- history stays in audit ConfigMaps, as before;
- approvals and access requests return 503 with "No database is configured";
- a warning is logged at start.

The chart always configures a database.

## Schema and migrations

Migrations are SQL files in `internal/store/migrations`, embedded in the binary and applied with [goose](https://github.com/pressly/goose). goose was picked over golang-migrate because it is the more active project and it provides:

- an embedded-filesystem `Provider`;
- a PostgreSQL session locker built on an advisory lock;
- per-version transactions.

The controller migrates the schema whenever the database is reachable, starting at boot, under the advisory lock `0x41504353`. Two replicas starting together therefore apply each version once. Applied versions are recorded in `apc_schema_migrations`. Never edit an applied migration; add the next numbered file instead. Every migration has a `Down` section, which is tested, but the controller only migrates up. An older controller running against a newer schema logs a warning and keeps working, because migrations only add.

| Table | Holds | Indexes for |
| --- | --- | --- |
| `audit_events` | History: one row per record. `record_key` is unique, which makes writes idempotent. `details` is JSONB. | A cell's history newest first; filters by action prefix, actor and time; retention |
| `access_requests` | Requests for one exact target (scheme, host, port, method, escaped path, query and body fingerprint), with state `pending`, `approved` or `denied` and the decision. At most one pending request per target; repeats count `observations`. | State and host filters; retention of decided requests |
| `approvals` | Exact approvals, with their source request, creator and revocation. At most one active approval per target. | The active set of a cell (what a proxy enforces); host filter |
| `proxy_requests` | Per-request proxy summaries: metadata only (no query values, bodies or credential values). Unique per cell and proxy request ID, so redelivery from the proxy outbox is idempotent. | Decision and host filters by time; retention |
| `imports` | One-time imports, such as the audit ConfigMap import. | |

Limits per cell:

- at most 4096 active approvals;
- at most 1024 pending requests;
- at most 1000 rows per list call.

Writes that would exceed a limit return 429.

### Retention

Retention runs hourly. A value of `0` keeps records forever; any other value must be at least 24 hours.

| Setting | Default | Removes |
| --- | --- | --- |
| `database.historyRetention` | `8760h` (365 days) | History records older than this. The ledger actions are never removed (`grant-requested`, `access-granted`, `orchestrator-bound`, `remove-requested`, `access-revoked`, `cleanup-verified`), because the controller reads them back as safety evidence. |
| `database.decidedRequestRetention` | `8760h` | Decided requests older than this, unless an approval still references them. |
| `database.proxySummaryRetention` | `720h` (30 days) | Proxy summaries older than this. |

Approvals and pending requests are never pruned.

## History and the audit ConfigMaps

Before PostgreSQL, every history record was an immutable `audit-*` ConfigMap in the control namespace. Now:

- **New records** go to PostgreSQL. The key is the name the ConfigMap would have had, so a retry never duplicates a record.
- **One-time import.** At the first start with a database, the controller copies every existing audit ConfigMap into PostgreSQL, keeping each record's original time. The import is idempotent: an interrupted import runs again on the next attempt without duplicates, and it is marked done (`imports.audit-configmaps-v1`) only after every record is stored. Until then, history reads merge in the ConfigMaps that haven't been imported yet. The ConfigMaps are kept afterwards, and the controller still reads them as safety evidence. Deleting them after the import is safe, but optional.
- **Database down.** The controller doesn't wait for the database to record lifecycle work, such as pausing or revoking access. While the database is down, a record is written as an immutable ConfigMap labelled `projects.agentwork.dev/audit-spool: "true"`. Within 30 seconds of the database coming back, the controller stores each spooled record and then deletes its ConfigMap.
- **Safety checks.** Some checks ask whether something ever happened, for example "was access ever granted, so is a missing credential ledger an error?". They read both the ConfigMaps and PostgreSQL. If the database can't be read, the check fails rather than answering "no", so removal waits instead of deleting storage unverified.

`GET /api/v1/workspaces/{name}/audit` takes these filters:

- `action`: an action prefix;
- `actor`;
- `since` and `before`, in RFC 3339;
- `limit`, at most 1000.

The response header `X-History-Source` is one of:

- `database`;
- `database+configmaps`: before the import finishes, or with records still spooled;
- `configmaps`: no database configured;
- `configmaps-fallback`: the database is down, so newer records may be missing.

## Approvals, requests and who may decide

| Route | Who |
| --- | --- |
| `GET /api/v1/database` | Any signed-in caller. Reports `configured`, `ready`, `reason`, a console `message`, and `approvers` (whether an approver group is configured). `GET /api/v1/session` also reports `database` and whether the caller is an `approver`. |
| `GET /api/v1/workspaces/{name}/access-requests?state=&host=&before=&limit=` | Any signed-in caller. |
| `POST /api/v1/workspaces/{name}/access-requests` with `{"scheme", "host", "port", "method", "path", "fingerprint", "package", "reason"}` | Any signed-in caller (operator or automation token). A pending request grants nothing. Returns 201 when new, or 200 when it counts one more observation of the same pending request. |
| `POST /api/v1/workspaces/{name}/access-requests/{id}/decision` with `{"decision": "approve" \| "deny", "reason"}` | Approvers only. |
| `GET /api/v1/workspaces/{name}/approvals[?include=revoked&host=&before=&limit=]` | Any signed-in caller. |
| `DELETE /api/v1/workspaces/{name}/approvals/{id}` with optional `{"reason"}` | Approvers only. |
| `GET /api/v1/workspaces/{name}/proxy-requests?decision=&host=&since=&before=&limit=` | Any signed-in caller. |

**Approvers.** The controller API decides who may approve. This replaces D12's RBAC on `accessrequests/status`.

- An approver is an identity that signed in to the console with OIDC and whose ID token claim `OIDC_APPROVER_GROUPS_CLAIM` (default: `OIDC_GROUPS_CLAIM`, which is `groups`) contains one of `OIDC_APPROVER_GROUPS`. The chart's default group is `agent-project-approvers`.
- Approvers must also be allowed to sign in (`OIDC_ALLOWED_GROUPS` or `OIDC_ALLOWED_SUBJECTS`).
- The group is read at sign-in and kept with the server-side session, which lasts at most 15 minutes. Removing someone from the group therefore ends their approval rights within 15 minutes.
- The automation bearer token and the static login never approve.
- With no approver group configured, nobody can approve, and the API says so.

**Recording.** Each decision and revocation is written in the same transaction as the change it describes, as a history record (`request-approved`, `request-denied` or `approval-revoked`). The record carries the approver, their approver group and the reason. Each one also produces an OTel span (`access.decision` or `access.revoke`), a counter (`apc.access.decisions{decision,outcome}`) and a structured log line. Refused attempts are logged as well.

Approving creates the exact approval, or reuses the active approval for the same target. A request can be decided only once; a second decision returns 409.

## When PostgreSQL is down

- **No new writes.** Requests, decisions and revocations can't be recorded, so they are refused with 503: "The database is unavailable, so new approvals and access requests can't be saved and are refused. Proxies keep enforcing the approvals they already have."
- **Proxies keep their cached approvals.** They enforce the approvals they already hold and expire windows and grants on their own clock (D13 item 9). The console's list of active approvals shows the last snapshot it read, marked `"stale": true` with `cachedAt`.
- **Use custom resources to cut access in an outage.** The access controls built on custom resources keep working, because they need no database: "Lock now", removing an `AccessGrant`, pausing or removing a project.
- **Lifecycle work continues**, with its records spooled as described above.
- **Readiness.** The controller's Ready condition (`/readyz`, check `database`) fails while the schema isn't migrated or the database can't be reached. The `apc.database.up` metric reports the same. The chart's console Service publishes not-ready addresses, so the console stays reachable and can explain the outage. If you deploy the controller from your own manifests, set `publishNotReadyAddresses: true` on its Service too.
- **Recovery is automatic**: migrations run when the database is reachable, then spooled records are moved in.

## Backups

**CloudNativePG mode.** Set `database.cloudnativepg.backup.enabled: true` to back up continuously (base backups plus WAL archiving), with a `ScheduledBackup` at `backup.schedule`. There are two methods:

- `method: plugin` (default): the [Barman Cloud plugin](https://cloudnative-pg.io/plugin-barman-cloud/). Install the plugin and an `ObjectStore` first, then set `backup.plugin.parameters.barmanObjectName`.
- `method: barmanObjectStore`: CloudNativePG's in-tree Barman Cloud support. Upstream has deprecated it in favour of the plugin. Set `backup.barmanObjectStore` (`destinationPath`, credentials) and `backup.retentionPolicy`.

Without an object store, a scheduled `pg_dump` over TLS to your existing backup path also works. Add its pod to `extraIngressFrom`.

Restore a CloudNativePG backup by bootstrapping a new Cluster from it (`bootstrap.recovery`), or restore a dump with `pg_restore` into an empty database. Then start the controller: it migrates the schema up if needed.

## Testing

Unit tests run against an in-process PostgreSQL ([fergusstrange/embedded-postgres](https://github.com/fergusstrange/embedded-postgres), which downloads PostgreSQL binaries from Maven Central on first use) and against an in-memory fake that passes the same contract tests (`internal/store/contract_test.go`).

| Setting | Effect |
| --- | --- |
| `go test -short` or `APC_POSTGRES_TEST=skip` | Skips the PostgreSQL tests. |
| `APC_TEST_DATABASE_URL=postgres://...` | Uses that server instead of the embedded one (each test creates its own database); needed when tests run as root, which PostgreSQL refuses. |
| `APC_CNPG_CRDS=<CloudNativePG release manifest>` with `kubeconform` installed | Validates the chart in both modes against the CloudNativePG CRD schemas, in strict mode. |
