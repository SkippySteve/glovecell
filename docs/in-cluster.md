# Glovecell runs in the cluster

Steve's rule (2026-11-10). It applies to every Glovecell component, now and in
future changes.

## The rule

1. **Everything Glovecell needs in order to work runs in the cluster**, installed
   by the Glovecell Helm chart (`charts/agent-project-controller`). Nothing else
   installs a Glovecell service: not a desktop script, not a second repository's
   manifests.
2. **An operator machine is only a client.** A browser, the Orca/T3 app and
   optional one-shot CLI commands that run with the operator's own identity. No
   Glovecell daemon, timer, `systemctl --user` unit, FUSE mount, port-forward or
   background `kubectl` runs on it, and it holds no Glovecell service
   credentials. If Glovecell stops working when a laptop is closed, the design is
   wrong.
3. **Credentials go where the blast radius is smallest.** A component that needs a
   credential the controller should not hold (a GitHub App key, provider logins,
   exec into cells) runs as **its own pod with its own ServiceAccount** and the
   narrowest RBAC that does the job, in the controller's namespace or a dedicated
   one, still installed by the chart. Never widen the controller to avoid a pod.
4. **New components are classified before they merge.** Every subcommand is listed
   in [components.json](components.json) with where it runs. Allowed classes:
   `chart` (a Deployment/StatefulSet in the chart), `controller-pod` (runs inside a
   pod the controller creates), `operator-cli` (one-shot, nothing depends on it
   running). The `cluster-exception` and `desktop-exception` lists may only shrink.

## The check

`internal/installation/incluster_test.go` (runs in `go test ./...`):

- every subcommand in `cmd/agent-project-controller/main.go` is in
  `components.json`, and every entry still exists;
- every `chart` component names a template that exists;
- every exception names its replacement and tracking, and no exception is added
  beyond the four recorded below;
- no doc, script or chart file tells an operator to install a desktop service
  (`systemctl --user`, `~/.config/systemd/user`, `kubectl port-forward`), except
  the files that document the existing exceptions until they are retired.

## Current exceptions and their migration

| Component | Today | Replacement |
| --- | --- | --- |
| `register --watch` (desktop registrar) | systemd user unit on Steve's desktop (installed 2026-11-08), running with the operator's **admin** kubeconfig; FUSE-mounts each Ready cell via `kubectl exec` and runs `orca repo add` | In-cluster `hubspoke` already enrolls each Ready cell and its `spec.repositories` with the Orca hub (APC #118/#119). The desktop Orca app connects to the hub as a client, like the phone. Retire the unit once that path works on the desktop (add before remove). APC #132 (extending the registrar) was closed for this reason |
| `hubspoke` (Orca hub) | `infra/agent-workplace/orca-hub` in the infra repo | Chart hub Deployment (`hubspoke.hub`, already templated off by default) as its own pod; the infra repo only sets values |
| `provider-broker` | `infra/agent-workplace/provider-broker` | Chart template, own Deployment and ServiceAccount, off by default |
| `github-broker` | `infra/agent-workplace/github-broker` | Chart template, own Deployment holding the GitHub App credential, off by default |

Related, outside this repository: the desktop's `workplace-otel-agents-forward`
unit (`kubectl port-forward` to the telemetry intake, also with the admin
kubeconfig) is the same pattern for the infra's agent telemetry and is tracked in
the infra repository's priorities.
