# Projected cluster reader

The optional Go `reader` command publishes reviewed Kubernetes fields and numeric
Prometheus samples. It does not forward workload documents, MCP tools or upstream
response headers. Run it on the trusted side with its own read-only Kubernetes
ServiceAccount and Grafana Viewer credential. Workers receive neither credential.

The Kubernetes endpoint permits GET/HEAD on reviewed workload, storage, network,
metrics and Flux resources. It returns resource identifiers, replica counts,
known readiness conditions and numeric resource usage. Environment values,
commands, images, annotations, labels, managed fields, inline Helm values,
URLs, events' messages, status messages and arbitrary custom fields are omitted.
Secrets, ConfigMaps, logs, exec, port forwarding, watches and mutations are denied.
Error responses contain fixed messages. Oversized or malformed responses fail
closed before any upstream bytes reach the caller.

The MCP endpoint exposes only `list_datasources` and instant `query_prometheus`.
Datasource configuration and names are omitted. Metric output contains numeric
sample timestamps and values without labels, exemplars, warnings or error text.
Log, trace and dashboard content is unsupported and denied. This is a deliberate
field projection, not a promise to detect credentials disguised as identifiers
or numeric measurements.

Configure the trusted controller with `READER_WORKSPACE_UID`,
`READER_HTTPS_HOST` and `READER_UPSTREAM`. The host must be an exact reserved
`.invalid` hostname. The upstream must be an explicit internal `.svc` or
`.svc.cluster.local` HTTP service on port 8080. This route is bound to one
workspace UID. It adds only GET/HEAD `/k8s` and POST `/mcp` to that project's
inspecting proxy. It does not grant workers direct reader access or change
ordinary private-address denial in the proxy's HTTPS transport.

Reader deployment settings are `READER_KUBERNETES_URL`, `READER_GRAFANA_URL`
and `READER_GRAFANA_TOKEN_FILE`. The Kubernetes URL defaults to its authenticated
in-cluster HTTPS endpoint. All credentials stay in the trusted reader's mounts.
Only the project's proxy may connect to the reader. The proxy may resolve and
reach that one internal service; workers retain their existing proxy-only rule.
Worker clients use the inspection CA and an explicit HTTPS proxy. Remove any old
HTTP gateway exception before resuming a project.

Use the normal Paused policy transition when enabling or changing this route.
A failed transition leaves the project paused. Qualify reads, method/path denial,
direct worker connection denial and synthetic content projection in a disposable
namespace before rollout. Unit tests never grant project access or deploy it.
