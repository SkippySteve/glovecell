package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/auditlog"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/bootstrap"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/cellrelay"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/controller"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forge"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubbroker"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubhost"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/hubspoke"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/orchestrator"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/preflight"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/projects"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/providerbroker"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/reader"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/t3"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/telemetry"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/web"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var version = "dev"
var commit = "unknown"

func env(k, fallback string) string {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	return v
}

// databaseFromEnv reads the controller database settings (D13 item 2,
// docs/database.md). Without DATABASE_URL_FILE it returns nil: history stays
// in audit ConfigMaps and approvals and access requests are refused.
func databaseFromEnv(ctx context.Context) (*store.Postgres, store.Retention, error) {
	urlFile, caFile := os.Getenv("DATABASE_URL_FILE"), os.Getenv("DATABASE_CA_FILE")
	retention := store.Retention{History: 365 * 24 * time.Hour, DecidedRequests: 365 * 24 * time.Hour, ProxySummaries: 30 * 24 * time.Hour}
	if urlFile == "" {
		if caFile != "" || os.Getenv("DATABASE_MAX_CONNS") != "" {
			return nil, retention, errors.New("DATABASE_CA_FILE and DATABASE_MAX_CONNS need DATABASE_URL_FILE")
		}
		return nil, retention, nil
	}
	cfg, err := store.ConfigFromFiles(urlFile, caFile)
	if err != nil {
		return nil, retention, err
	}
	if v := os.Getenv("DATABASE_MAX_CONNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 || n > 100 {
			return nil, retention, errors.New("DATABASE_MAX_CONNS must be 2 to 100")
		}
		cfg.MaxConns = int32(n)
	}
	for key, dst := range map[string]*time.Duration{"HISTORY_RETENTION": &retention.History, "DECIDED_REQUEST_RETENTION": &retention.DecidedRequests, "PROXY_SUMMARY_RETENTION": &retention.ProxySummaries} {
		if v := os.Getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 || d > 0 && d < 24*time.Hour {
				return nil, retention, fmt.Errorf("%s must be 0 (keep) or a duration of at least 24h", key)
			}
			*dst = d
		}
	}
	db, err := store.Open(ctx, cfg)
	return db, retention, err
}

// Vendor telemetry is enabled by default; malformed settings never broaden it.
func vendorTelemetryFromEnv() (bool, error) {
	switch env("VENDOR_TELEMETRY_ENABLED", "true") {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("VENDOR_TELEMETRY_ENABLED must be true or false")
	}
}

func auditAssignmentFeedFromEnv() (bool, error) {
	switch env("AUDIT_ASSIGNMENT_FEED_ENABLED", "false") {
	case "true":
		return true, nil
	case "false", "":
		return false, nil
	default:
		return false, errors.New("AUDIT_ASSIGNMENT_FEED_ENABLED must be true or false")
	}
}

func profile(prefix string) controller.ResourceProfile {
	return controller.ResourceProfile{CPURequest: os.Getenv(prefix + "_CPU_REQUEST"), MemoryRequest: os.Getenv(prefix + "_MEMORY_REQUEST"), CPULimit: os.Getenv(prefix + "_CPU_LIMIT"), MemoryLimit: os.Getenv(prefix + "_MEMORY_LIMIT")}
}
func secret(k string) (string, error) {
	p := os.Getenv(k)
	if p == "" {
		return "", fmt.Errorf("%s file path required", k)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return "", fmt.Errorf("unable to read %s", k)
	}
	return strings.TrimSpace(string(b)), nil
}

// handoffsFromEnv configures the optional handoff intake. The intake requires
// the console's bearer token, so a URL without a readable token fails startup.
func handoffsFromEnv() (*web.Handoffs, error) {
	intake := os.Getenv("HANDOFF_URL")
	if intake == "" {
		for _, k := range []string{"HANDOFF_TOKEN_FILE", "HANDOFF_SANDBOX"} {
			if os.Getenv(k) != "" {
				return nil, errors.New(k + " requires HANDOFF_URL")
			}
		}
		return nil, nil
	}
	token, e := secret("HANDOFF_TOKEN_FILE")
	if e != nil {
		return nil, fmt.Errorf("HANDOFF_URL requires HANDOFF_TOKEN_FILE: %w", e)
	}
	h := &web.Handoffs{URL: intake, Token: token, Sandbox: os.Getenv("HANDOFF_SANDBOX")}
	if e = h.Validate(); e != nil {
		return nil, e
	}
	return h, nil
}

// lanFromEnv reads the optional home-LAN grant setting. Grants need a network
// provider that can express a single-host TCP allow; otherwise startup fails.
func lanFromEnv(provider network.Provider) (network.LANConfig, error) {
	lan, err := network.ParseLANConfig(os.Getenv("LAN_GRANT_ALLOWED_CIDRS"), os.Getenv("LAN_GRANT_DENIED_CIDRS"))
	if err != nil {
		return network.LANConfig{}, err
	}
	if _, ok := provider.(network.LANPolicy); lan.Enabled() && !ok {
		return network.LANConfig{}, errors.New("LAN_GRANT_ALLOWED_CIDRS requires NETWORK_PROVIDER=cilium")
	}
	return lan, nil
}

// providerLeasesFromEnv reads the optional brokered provider lease source,
// PROVIDER_LEASES_SECRET=<namespace>/<name>: the Secret the provider broker
// writes in lease mode. Unset keeps spec.providers off.
func providerLeasesFromEnv(provider network.Provider) (*types.NamespacedName, error) {
	return leaseSecretFromEnv("PROVIDER_LEASES_SECRET", provider)
}

// githubLeaseFromEnv reads GITHUB_LEASE_SECRET=<namespace>/<name>: the lease
// Secret the GitHub broker writes. Unset keeps spec.github off.
func githubLeaseFromEnv(provider network.Provider) (*types.NamespacedName, error) {
	return leaseSecretFromEnv("GITHUB_LEASE_SECRET", provider)
}

func leaseSecretFromEnv(variable string, provider network.Provider) (*types.NamespacedName, error) {
	raw := os.Getenv(variable)
	if raw == "" {
		return nil, nil
	}
	ns, name, ok := strings.Cut(raw, "/")
	if !ok || len(validation.IsDNS1123Label(ns)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
		return nil, errors.New(variable + " must be <namespace>/<secret name>")
	}
	if _, ok := provider.(network.ProviderPolicy); !ok {
		return nil, errors.New(variable + " requires NETWORK_PROVIDER=cilium")
	}
	return &types.NamespacedName{Namespace: ns, Name: name}, nil
}

// downloadsFromEnv reads the optional "Download into sandbox" setting. It is
// off unless DOWNLOADS_ENABLED=true, and needs the Cilium provider, whose
// per-download proxy policy names exact hosts.
func downloadsFromEnv(provider network.Provider, config *rest.Config) (*web.Downloads, error) {
	if os.Getenv("DOWNLOADS_ENABLED") != "true" {
		if os.Getenv("DOWNLOAD_MAX_BYTES") != "" || os.Getenv("DOWNLOAD_TIMEOUT") != "" {
			return nil, errors.New("DOWNLOAD_MAX_BYTES and DOWNLOAD_TIMEOUT require DOWNLOADS_ENABLED=true")
		}
		return nil, nil
	}
	policy, ok := provider.(network.DownloadPolicy)
	if !ok {
		return nil, errors.New("DOWNLOADS_ENABLED requires NETWORK_PROVIDER=cilium")
	}
	d := &web.Downloads{Policy: policy, MaxBytes: download.DefaultMaxBytes, Timeout: download.DefaultTimeout}
	if v := os.Getenv("DOWNLOAD_MAX_BYTES"); v != "" {
		n, err := positiveID("DOWNLOAD_MAX_BYTES")
		if err != nil || n > download.MaxMaxBytes {
			return nil, fmt.Errorf("DOWNLOAD_MAX_BYTES must be a byte count up to %d", download.MaxMaxBytes)
		}
		d.MaxBytes = n
	}
	if v := os.Getenv("DOWNLOAD_TIMEOUT"); v != "" {
		t, err := time.ParseDuration(v)
		if err != nil || t < time.Minute || t > download.MaxTimeout {
			return nil, fmt.Errorf("DOWNLOAD_TIMEOUT must be a duration between 1m and %s", download.MaxTimeout)
		}
		d.Timeout = t
	}
	d.Exec = func(ctx context.Context, namespace, pod, container string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
		return connect.ExecInContainer(ctx, config, namespace, pod, container, command, stdin, stdout, stderr, false)
	}
	return d, nil
}
func positiveID(key string) (int64, error) {
	value := os.Getenv(key)
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, nil
}
func openGitHost() (forge.Forge, string, string, string, error) {
	switch env("GIT_HOST", "forgejo") {
	case "forgejo":
		if os.Getenv("FORGE_AGENT_URL") != "" {
			client, err := forgeAgentHost()
			if err != nil {
				return nil, "", "", "", err
			}
			return client, "forgejo", client.Origin, client.Owner, nil
		}
		token, err := secret("FORGEJO_TOKEN_FILE")
		if err != nil {
			return nil, "", "", "", err
		}
		client, err := forgejo.New(os.Getenv("FORGEJO_ORIGIN"), os.Getenv("FORGEJO_OWNER"), token)
		if err != nil {
			return nil, "", "", "", err
		}
		return client, "forgejo", client.Origin, client.Owner, nil
	case "github":
		if os.Getenv("FORGEJO_ORIGIN") != "" || os.Getenv("FORGEJO_OWNER") != "" || os.Getenv("FORGEJO_TOKEN_FILE") != "" {
			return nil, "", "", "", errors.New("GIT_HOST=github does not use Forgejo settings")
		}
		appID, err := positiveID("GITHUB_APP_ID")
		if err != nil {
			return nil, "", "", "", err
		}
		installationID, err := positiveID("GITHUB_APP_INSTALLATION_ID")
		if err != nil {
			return nil, "", "", "", err
		}
		path := os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE")
		if path == "" {
			return nil, "", "", "", errors.New("GITHUB_APP_PRIVATE_KEY_FILE is required")
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, "", "", "", errors.New("unable to read GITHUB_APP_PRIVATE_KEY_FILE")
		}
		client, err := githubhost.New(os.Getenv("GITHUB_OWNER"), appID, installationID, key)
		if err != nil {
			return nil, "", "", "", err
		}
		return client, "github", "https://github.com", client.Owner, nil
	default:
		return nil, "", "", "", errors.New("GIT_HOST must be forgejo or github")
	}
}
func run(ctx context.Context) error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "provider-broker":
			config, err := ctrl.GetConfig()
			if err != nil {
				return errors.New("trusted Kubernetes client unavailable")
			}
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				return err
			}
			if err := api.AddToScheme(scheme); err != nil {
				return err
			}
			c, err := client.New(config, client.Options{Scheme: scheme})
			if err != nil {
				return errors.New("trusted broker client unavailable")
			}
			defer telemetry.Start(ctx, "apc-provider-broker")()
			return providerbroker.Run(ctx, c, config)
		case "github-broker":
			config, err := ctrl.GetConfig()
			if err != nil {
				return errors.New("trusted Kubernetes client unavailable")
			}
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				return err
			}
			c, err := client.New(config, client.Options{Scheme: scheme})
			if err != nil {
				return errors.New("trusted broker client unavailable")
			}
			defer telemetry.Start(ctx, "apc-github-broker")()
			return githubbroker.Run(ctx, c, os.Args[2:])
		case "register-projects":
			return projects.Register(ctx)
		case "version", "--version":
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version, "commit": commit})
		case "t3":
			if len(os.Args) != 3 {
				return errors.New("usage: agent-project-controller t3 <workspace>")
			}
			return t3CLI(ctx, os.Args[2])
		case "t3-serve":
			if len(os.Args) != 3 {
				return errors.New("T3 launch instance required")
			}
			return t3.Serve(os.Args[2])
		case "t3-managed":
			if len(os.Args) != 3 {
				return errors.New("managed T3 launch instance required")
			}
			return t3.ManagedServe(os.Args[2])
		case "t3-stop":
			if len(os.Args) != 3 {
				return errors.New("T3 launch instance required")
			}
			return t3.Stop(os.Args[2])
		case "t3-relay":
			return t3.Relay()
		case "t3-codex":
			return t3.Codex(os.Args[2:])
		case "bootstrap":
			return bootstrap.Run(ctx)
		case "credential":
			return bootstrap.Credential()
		case "reader":
			return reader.Run(ctx)
		case "telemetry-relay":
			// Second container of each cell's proxy pod (decision GT A).
			return cellrelay.Run(ctx)
		case "proxy-ready":
			return proxy.ReadyCLI(ctx)
		case "proxy":
			// No OTLP push from inside a cell (decision GT A): the collector
			// will scrape the proxy and its telemetry relay instead.
			return proxy.Run(ctx)
		case "fetch":
			// Run by the controller inside a project worker for "Download into sandbox".
			return download.RunHelper(os.Stdin, os.Stdout)
		case "put":
			// Run by the controller inside a project worker for "Upload into sandbox";
			// the file arrives on stdin.
			return download.RunPut(os.Args[2:], os.Stdin, os.Stdout)
		case "download-grant":
			// Run by the controller inside the project proxy pod.
			return proxy.GrantCLI(ctx, os.Stdin, os.Stdout)
		case "preflight":
			return preflight.Run(ctx)
		case "connect":
			if len(os.Args) != 3 {
				return errors.New("usage: agent-project-controller connect <workspace>")
			}
			return connectCLI(ctx, os.Args[2], []string{"/bin/sh"})
		case "model":
			if len(os.Args) != 3 {
				return errors.New("usage: agent-project-controller model <workspace>")
			}
			return connectCLI(ctx, os.Args[2], connect.ModelCommand())
		case "exec":
			if len(os.Args) < 5 || os.Args[3] != "--" {
				return errors.New("usage: agent-project-controller exec <workspace> -- <command>")
			}
			return connectCLI(ctx, os.Args[2], os.Args[4:])
		case "register":
			return registerCLI(ctx, os.Args[2:])
		case "provider":
			return providerCLI(ctx, os.Args[2:])
		case "hubspoke":
			return hubspokeCLI(ctx, os.Args[2:])
		case "forge-agent":
			defer telemetry.Start(ctx, "apc-forge-agent")()
			return forgeAgentCLI(ctx)
		case "controller":
		default:
			return errors.New("unknown command")
		}
	}
	ns := env("CONTROL_NAMESPACE", "agent-project-controller")
	password := ""
	if os.Getenv("API_SECRET_FILE") != "" {
		var e error
		password, e = secret("API_SECRET_FILE")
		if e != nil {
			return e
		}
	}
	host, gitHost, origin, owner, e := openGitHost()
	if e != nil {
		return e
	}
	defer telemetry.Start(ctx, "agent-project-controller")()
	scheme := runtime.NewScheme()
	if e = clientgoscheme.AddToScheme(scheme); e != nil {
		return e
	}
	if e = api.AddToScheme(scheme); e != nil {
		return e
	}
	config := ctrl.GetConfigOrDie()
	// Secrets are never cached: a cache would start a cluster-wide Secret
	// informer, and the controller may read Secrets only in its own namespace
	// and in project namespaces (RoleBindings, not a ClusterRole).
	manager, e := ctrl.NewManager(config, ctrl.Options{Scheme: scheme, LeaderElection: true, LeaderElectionID: "agent-project-controller", LeaderElectionNamespace: ns, HealthProbeBindAddress: ":8081", Metrics: metricsserver.Options{BindAddress: "0"}, Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{&api.ProjectWorkspace{}: {Namespaces: map[string]cache.Config{ns: {}}}}}, Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&api.ProjectWorkspace{}, &corev1.Secret{}}}}})
	if e != nil {
		return e
	}
	direct, e := client.New(config, client.Options{Scheme: scheme})
	if e != nil {
		return e
	}
	networkProvider, e := network.FromEnvironment()
	if e != nil {
		return e
	}
	workerScheduling, e := controller.ParseScheduling(os.Getenv("WORKSPACE_SCHEDULING_JSON"))
	if e != nil {
		return fmt.Errorf("workspace scheduling: %w", e)
	}
	proxyScheduling, e := controller.ParseScheduling(os.Getenv("PROXY_SCHEDULING_JSON"))
	if e != nil {
		return fmt.Errorf("proxy scheduling: %w", e)
	}
	lan, e := lanFromEnv(networkProvider)
	if e != nil {
		return e
	}
	orcaHubSpoke := os.Getenv("HUBSPOKE_ORCA_ENABLED") == "true"
	auditAssignmentFeed, e := auditAssignmentFeedFromEnv()
	if e != nil {
		return e
	}
	reconciler := &controller.Reconciler{LAN: lan, Client: direct, Host: host, Network: networkProvider, Config: controller.Config{Namespace: ns, GitHost: gitHost, ForgejoOrigin: origin, Owner: owner, Image: os.Getenv("WORKSPACE_IMAGE"), ProxyImage: os.Getenv("PROXY_IMAGE"), RuntimeClass: os.Getenv("RUNTIME_CLASS"), StorageClass: os.Getenv("STORAGE_CLASS"), StorageSize: env("STORAGE_SIZE", "10Gi"), WorkerResources: profile("WORKSPACE"), ProxyResources: profile("PROXY"), WorkerScheduling: workerScheduling, ProxyScheduling: proxyScheduling, RegistrarNamespace: os.Getenv("REGISTRAR_NAMESPACE"), WorkerExecClusterRole: os.Getenv("WORKER_EXEC_CLUSTER_ROLE"), ProjectAccessClusterRole: os.Getenv("PROJECT_ACCESS_CLUSTER_ROLE"), ControllerServiceAccount: env("CONTROLLER_SERVICE_ACCOUNT", controller.DefaultControllerServiceAccount), OrcaHubSpoke: orcaHubSpoke, OrcaRuntimeImage: os.Getenv("ORCA_RUNTIME_IMAGE"), OrcaOrchestratorClass: env("HUBSPOKE_ORCHESTRATOR_CLASS", "orca"), OrcaSpokePublicDomain: strings.TrimSpace(os.Getenv("HUBSPOKE_SPOKE_DOMAIN")), OrcaSpokeGatewayName: strings.TrimSpace(os.Getenv("HUBSPOKE_SPOKE_GATEWAY_NAME")), OrcaSpokeGatewayNamespace: strings.TrimSpace(os.Getenv("HUBSPOKE_SPOKE_GATEWAY_NAMESPACE")), OrcaSpokeGatewaySection: strings.TrimSpace(os.Getenv("HUBSPOKE_SPOKE_GATEWAY_SECTION")), OrcaHubNamespace: env("HUBSPOKE_HUB_NAMESPACE", "agent-project-control-orca-hub"), HubLogsClusterRole: strings.TrimSpace(os.Getenv("HUBSPOKE_HUB_LOGS_CLUSTER_ROLE")), HubServiceAccount: strings.TrimSpace(os.Getenv("HUBSPOKE_HUB_SERVICE_ACCOUNT")), AuditAssignmentFeed: auditAssignmentFeed, AuditAssignmentFeedConfigMap: strings.TrimSpace(os.Getenv("AUDIT_ASSIGNMENT_FEED_CONFIGMAP"))}}
	if orcaHubSpoke && reconciler.Config.OrcaRuntimeImage == "" {
		return errors.New("HUBSPOKE_ORCA_ENABLED=true requires ORCA_RUNTIME_IMAGE")
	}
	telemetry, e := vendorTelemetryFromEnv()
	if e != nil {
		return e
	}
	reconciler.Config.VendorTelemetry = telemetry
	if os.Getenv("GITHUB_REPOSITORY_TOKEN_FILE") != "" {
		reconciler.GitHubToken, e = secret("GITHUB_REPOSITORY_TOKEN_FILE")
		if e != nil {
			return e
		}
		reconciler.Config.GitHubOwner = os.Getenv("ADDITIONAL_GITHUB_OWNER")
		if forgejo.ValidateRepository(reconciler.Config.GitHubOwner+"/repository", reconciler.Config.GitHubOwner) != nil || reconciler.Config.GitHubOwner == "" || reconciler.GitHubToken == "" {
			return errors.New("additional GitHub access requires an owner and trusted repository token")
		}
	} else if os.Getenv("ADDITIONAL_GITHUB_OWNER") != "" {
		return errors.New("additional GitHub owner requires a trusted repository token")
	}
	// Lifecycle Events (events.k8s.io/v1) regarding each workspace replace
	// the deprecated status.audit list (D12).
	clientset, e := kubernetes.NewForConfig(config)
	if e != nil {
		return e
	}
	broadcaster := events.NewBroadcaster(&events.EventSinkImpl{Interface: clientset.EventsV1()})
	broadcaster.StartRecordingToSink(ctx.Done())
	defer broadcaster.Shutdown()
	reconciler.Events = broadcaster.NewRecorder(scheme, controller.EventReportingController)
	reconciler.IndependentCodexWorkspaceUID = os.Getenv("INDEPENDENT_CODEX_WORKSPACE_UID")
	reconciler.MuseSubscriptionKeyFile = os.Getenv("MUSE_SUBSCRIPTION_KEY_FILE")
	if reconciler.ProviderLeases, e = providerLeasesFromEnv(networkProvider); e != nil {
		return e
	}
	if reconciler.Config.CellTokens, e = controller.ParseCellTokenProviders(os.Getenv("PROVIDER_CELL_TOKENS")); e != nil {
		return e
	}
	if reconciler.Config.CellTokens != nil && reconciler.ProviderLeases == nil {
		return errors.New("PROVIDER_CELL_TOKENS requires PROVIDER_LEASES_SECRET")
	}
	if reconciler.GitHubLease, e = githubLeaseFromEnv(networkProvider); e != nil {
		return e
	}
	if host := os.Getenv("READER_HTTPS_HOST"); host != "" {
		route := &proxy.ReaderRoute{Host: host, Upstream: os.Getenv("READER_UPSTREAM")}
		if err := route.Validate(); err != nil {
			return err
		}
		reconciler.ReaderWorkspaceUID = os.Getenv("READER_WORKSPACE_UID")
		if reconciler.ReaderWorkspaceUID == "" {
			return errors.New("reader workspace UID required")
		}
		reconciler.ReaderRoute = route
	}
	// Reference rules: the deployment's read-only list every project's proxy
	// gets (README "Reference rules"). Read at startup; an invalid list stops
	// the controller rather than run with part of it.
	if path := os.Getenv("REFERENCE_RULES_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reference rules: %w", err)
		}
		if reconciler.Config.ReferenceRules, reconciler.Config.ReferenceDigest, err = proxy.ReferenceRules(raw); err != nil {
			return fmt.Errorf("reference rules: %w", err)
		}
		slog.Info("reference rules loaded", "digest", reconciler.Config.ReferenceDigest, "rules", len(reconciler.Config.ReferenceRules))
	}
	if e = reconciler.Config.Validate(); e != nil {
		return e
	}
	// Operational records (D13 item 2). The pool connects lazily: the
	// controller starts and enforces while the database is down, and the
	// monitor applies migrations as soon as it is reachable.
	db, retention, e := databaseFromEnv(ctx)
	if e != nil {
		return fmt.Errorf("database: %w", e)
	}
	var monitor *store.Monitor
	var auditLog *auditlog.Log
	if db != nil {
		defer db.Close()
		auditLog = &auditlog.Log{Client: direct, Namespace: ns, Store: db}
		monitor = store.NewMonitor(db, db.Migrate)
		monitor.Retention = retention
		monitor.OnReady = func(ctx context.Context) {
			if _, err := auditLog.ImportLegacy(ctx); err != nil {
				slog.Warn("audit ConfigMap import incomplete; retrying", "error", err.Error())
			}
		}
		reconciler.AuditLog = auditLog
		go monitor.Run(ctx)
		go auditLog.Run(ctx, 30*time.Second)
	} else {
		slog.Warn("no database configured (DATABASE_URL_FILE): history stays in audit ConfigMaps, and approvals and access requests are refused; PostgreSQL is required (docs/database.md)")
	}
	// Orchestrator adapters an OrchestratorClass may name. Orca is either the
	// legacy control-host registrar (desktop mounts) or hub-and-spoke
	// (HUBSPOKE_ORCA_ENABLED=true): agents stay in gVisor spokes, the hub
	// enrolls them for phone relay. T3 joins when T3 Connect is configured below.
	orcaAdapter := orchestrator.Adapter(orchestrator.NewOrca(direct, ns))
	if os.Getenv("HUBSPOKE_ORCA_ENABLED") == "true" {
		orcaAdapter = hubspoke.NewOrcaAccess(direct, ns, true)
	} else if v := os.Getenv("HUBSPOKE_ORCA_ENABLED"); v != "" && v != "false" {
		return errors.New("HUBSPOKE_ORCA_ENABLED must be true or false")
	}
	if reconciler.Orchestrators, e = orchestrator.NewRegistry(orcaAdapter, orchestrator.NewCommand(direct, ns)); e != nil {
		return e
	}
	if v := os.Getenv("T3_CONNECT_ENABLED"); v != "" && v != "true" && v != "false" {
		return errors.New("T3_CONNECT_ENABLED must be true or false")
	}
	if os.Getenv("T3_CONNECT_ENABLED") == "true" {
		account := &t3.Account{Binary: env("T3_CONNECT_BINARY", t3.Binary), Directory: env("T3_CONNECT_DATA", "/var/lib/apc/t3-connect")}
		cloud := &t3.Cloud{URL: env("T3_CONNECT_RELAY_URL", t3.DefaultRelayURL), Tokens: account}
		if e = cloud.Validate(); e != nil {
			return e
		}
		access := &t3.Managed{Registry: &t3.Registry{Client: direct, Namespace: ns, Cloud: cloud}, Config: config, Connector: env("T3_CONNECT_CONNECTOR", "/opt/t3/cloudflared")}
		// Workspaces without a class keep using T3 Connect: it is the
		// default (translated "t3" class), and also the adapter for classes
		// naming projects.agentwork.dev/t3.
		reconciler.Access = access
		if e = reconciler.Orchestrators.Register(access); e != nil {
			return e
		}
		if e = manager.Add(access); e != nil {
			return e
		}
	}
	// Workspaces may name a ForgeClass; an empty name keeps the forge above.
	reconciler.Forges = &forge.Resolver{Client: direct, Namespace: ns, Default: &forge.Resolved{Driver: gitHost, Origin: origin, Owner: owner, Forge: host}}
	if e = reconciler.Setup(manager); e != nil {
		return e
	}
	if e = (&orchestrator.ClassReconciler{Client: direct, Registry: reconciler.Orchestrators}).Setup(manager); e != nil {
		return e
	}
	// Class status needs the ProviderClass and ForgeClass CRDs. Without them
	// the controller runs as before: bare provider names and the configured
	// forge keep working, and named classes fail closed.
	if controller.ClassesServed(manager.GetRESTMapper()) {
		classes := &controller.ClassReconciler{Client: direct, ProviderLeases: reconciler.ProviderLeases, Forges: reconciler.Forges}
		if e = classes.Setup(manager); e != nil {
			return e
		}
	} else {
		slog.Warn("ProviderClass and ForgeClass CRDs not installed; class status is off and named classes fail closed")
	}
	if e = manager.AddHealthzCheck("healthz", healthz.Ping); e != nil {
		return e
	}
	if e = manager.AddReadyzCheck("readyz", healthz.Ping); e != nil {
		return e
	}
	if monitor != nil {
		// Ready reflects database health. The chart's console Service
		// publishes not-ready addresses, so the console stays reachable to
		// explain an outage.
		if e = manager.AddReadyzCheck("database", monitor.Check); e != nil {
			return e
		}
	}
	server := &web.Server{T3Connect: reconciler.Access != nil, Client: direct, Host: host, Namespace: ns, Owner: owner, Password: password, Origin: os.Getenv("PUBLIC_ORIGIN"), GitHost: gitHost, GitOrigin: origin, AllowBasic: os.Getenv("ALLOW_STATIC_LOGIN") == "true"}
	server.GitHubOwner = reconciler.Config.GitHubOwner
	server.Audit = auditLog
	if orcaHubSpoke && strings.TrimSpace(os.Getenv("HUBSPOKE_SPOKE_DOMAIN")) != "" {
		server.OrcaPairing = &web.OrcaPairing{
			Rest:      config,
			Domain:    strings.TrimSpace(os.Getenv("HUBSPOKE_SPOKE_DOMAIN")),
			HubSpoke:  true,
			ClassName: env("HUBSPOKE_ORCHESTRATOR_CLASS", "orca"),
			Container: env("HUBSPOKE_WORKER_CONTAINER", "workspace"),
		}
	}
	if db != nil {
		server.Database = &web.Database{Store: db, Monitor: monitor}
	}
	server.Reference = web.Reference{Digest: reconciler.Config.ReferenceDigest, Rules: reconciler.Config.ReferenceRules}
	if reconciler.ProviderLeases != nil {
		server.Providers = &web.Providers{Status: reconciler.ProviderLeaseStatus}
		if brokerURL := os.Getenv("PROVIDER_BROKER_URL"); brokerURL != "" {
			if server.ProviderLogins, e = web.NewProviderLogins(brokerURL, os.Getenv("PROVIDER_BROKER_TOKEN_FILE")); e != nil {
				return e
			}
		}
	}
	server.GitHubBrokering = reconciler.GitHubLease != nil
	if lan.Enabled() {
		server.LAN = &web.LANGrants{Config: lan, Nodes: network.NodeAddresses}
	}
	if os.Getenv("SANDBOX_CONTROL_URL") != "" {
		server.SandboxControl = &web.SandboxControl{URL: os.Getenv("SANDBOX_CONTROL_URL"), Origin: os.Getenv("SANDBOX_CONTROL_ORIGIN"), Operator: os.Getenv("SANDBOX_CONTROL_OPERATOR")}
	} else if os.Getenv("SANDBOX_CONTROL_ORIGIN") != "" || os.Getenv("SANDBOX_CONTROL_OPERATOR") != "" {
		return errors.New("sandbox control integration requires URL, origin and delegated operator together")
	}
	if dir := os.Getenv("RUN_RECORDS_DIR"); dir != "" {
		server.RunRecords = &web.RunRecords{Dir: dir}
		// Optional read-only publishers, such as an Orca runtime, to copy run records from.
		if sources := web.CSV(os.Getenv("RUN_RECORDS_SOURCES")); len(sources) > 0 {
			puller := web.RunRecordPuller{Dir: dir, Sources: sources}
			if err := puller.Validate(); err != nil {
				return err
			}
			go puller.Run(ctx, time.Minute)
		}
	} else if os.Getenv("RUN_RECORDS_SOURCES") != "" {
		return errors.New("RUN_RECORDS_SOURCES requires RUN_RECORDS_DIR")
	}
	if server.Downloads, e = downloadsFromEnv(networkProvider, config); e != nil {
		return e
	}
	// Optional handoff intake of an agent runtime, such as the Orca sandbox.
	if server.Handoffs, e = handoffsFromEnv(); e != nil {
		return e
	}
	if os.Getenv("OIDC_ISSUER") != "" {
		clientSecret, err := secret("OIDC_CLIENT_SECRET_FILE")
		if err != nil {
			return err
		}
		server.OIDC, e = web.NewOIDC(ctx, web.OIDCConfig{Issuer: os.Getenv("OIDC_ISSUER"), ClientID: os.Getenv("OIDC_CLIENT_ID"), ClientSecret: clientSecret, Origin: server.Origin, GroupsClaim: env("OIDC_GROUPS_CLAIM", "groups"), Scopes: web.CSV(env("OIDC_SCOPES", "profile,groups")), AllowedGroups: web.CSV(os.Getenv("OIDC_ALLOWED_GROUPS")), AllowedSubjects: web.CSV(os.Getenv("OIDC_ALLOWED_SUBJECTS")),
			ApproverGroups: web.CSV(os.Getenv("OIDC_APPROVER_GROUPS")), ApproverGroupsClaim: os.Getenv("OIDC_APPROVER_GROUPS_CLAIM")})
		if e != nil {
			return e
		}
	}
	handler, e := server.Handler()
	if e != nil {
		return e
	}
	httpServer := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8080"), Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	failures := make(chan error, 2)
	go server.RunDownloadSweeper(ctx)
	go func() { failures <- manager.Start(ctx) }()
	go func() {
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		failures <- err
	}()
	select {
	case <-ctx.Done():
	case e = <-failures:
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(c)
	return e
}
func t3CLI(ctx context.Context, workspace string) error {
	config := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	return t3.Launch(ctx, c, config, env("CONTROL_NAMESPACE", "agent-project-controller"), workspace)
}

func hubspokeCLI(ctx context.Context, args []string) error {
	watch := false
	for _, a := range args {
		switch a {
		case "--watch":
			watch = true
		case "-h", "--help":
			return errors.New("usage: agent-project-controller hubspoke --watch")
		default:
			return fmt.Errorf("unknown hubspoke flag %q", a)
		}
	}
	if !watch {
		return errors.New("usage: agent-project-controller hubspoke --watch")
	}
	config := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	runtimeKind := env("HUBSPOKE_KIND", "orca")
	var (
		rt hubspoke.Runtime
		en hubspoke.Enroller
	)
	switch runtimeKind {
	case "orca":
		rt = hubspoke.OrcaRuntime{}
		en = &hubspoke.OrcaEnroller{
			Binary:      env("APC_ORCA_BINARY", registration.ResolveOrcaBinary()),
			ProjectPath: env("HUBSPOKE_PROJECT_PATH", "/workspace/repo"),
		}
	case "t3":
		rt, en = hubspoke.T3Runtime{}, hubspoke.T3Enroller{}
	default:
		return fmt.Errorf("unsupported HUBSPOKE_KIND %q", runtimeKind)
	}
	opts := hubspoke.WatchOptions{
		ControlNamespace: env("CONTROL_NAMESPACE", "agent-project-controller"),
		Kind:             runtimeKind,
		ProjectPath:      env("HUBSPOKE_PROJECT_PATH", "/workspace/repo"),
		ClassName:        os.Getenv("HUBSPOKE_ORCHESTRATOR_CLASS"),
		OfferFrom:        hubspoke.OfferFromLogs(config, rt, env("HUBSPOKE_WORKER_CONTAINER", "workspace")),
	}
	return hubspoke.RunWatch(ctx, c, en, rt, opts)
}

func registerCLI(ctx context.Context, args []string) error {
	watch := false
	for _, a := range args {
		switch a {
		case "--watch":
			watch = true
		case "-h", "--help":
			return errors.New("usage: agent-project-controller register --watch")
		default:
			return fmt.Errorf("unknown register flag %q", a)
		}
	}
	if !watch {
		return errors.New("usage: agent-project-controller register --watch")
	}
	config := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	t3Bin, _ := exec.LookPath("t3")
	opts := registration.Options{
		ControlNamespace: env("CONTROL_NAMESPACE", "agent-project-controller"),
		MountRoot:        env("APC_MOUNT_ROOT", registration.DefaultMountRoot()),
		T3Binary:         env("APC_T3_BINARY", t3Bin),
		OrcaBinary:       env("APC_ORCA_BINARY", registration.ResolveOrcaBinary()),
		Integrations:     registration.ParseIntegrations(os.Getenv("CLIENT_INTEGRATIONS")),
		Watch:            watch,
	}
	return registration.RunRegister(ctx, c, opts, func(namespace, pod string) (*registration.Remote, func() error, error) {
		return registration.StartKubectlHelper(ctx, config, namespace, pod)
	})
}

func connectCLI(ctx context.Context, workspace string, command []string) error {
	config := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	if e := clientgoscheme.AddToScheme(scheme); e != nil {
		return e
	}
	if e := api.AddToScheme(scheme); e != nil {
		return e
	}
	c, e := client.New(config, client.Options{Scheme: scheme})
	if e != nil {
		return e
	}
	_, pod, e := connect.Pod(ctx, c, env("CONTROL_NAMESPACE", "agent-project-controller"), workspace)
	if e != nil {
		return e
	}
	if len(command) == 1 && command[0] == "/bin/sh" {
		return connect.Run(ctx, c, config, env("CONTROL_NAMESPACE", "agent-project-controller"), workspace)
	}
	info, statErr := os.Stdin.Stat()
	tty := statErr == nil && info.Mode()&os.ModeCharDevice != 0
	if len(command) == 3 && command[0] == "python3" && command[1] == "-c" {
		fmt.Fprintf(os.Stderr, "Connecting to %s\n", workspace)
	} else {
		line, e := connect.ExecCommand(pod.Namespace, pod.Name, command)
		if e != nil {
			return e
		}
		fmt.Fprintf(os.Stderr, "%s\n", line)
	}
	return connect.Exec(ctx, config, pod.Namespace, pod.Name, command, os.Stdin, os.Stdout, os.Stderr, tty)
}
func main() {
	installLogger()
	if e := run(ctrl.SetupSignalHandler()); e != nil {
		slog.Error("process failed", "error", e)
		os.Exit(1)
	}
}

// installLogger makes every log line of this binary, including
// controller-runtime and client-go (klog) output, JSON on stderr with
// credentials redacted. LOG_LEVEL selects debug, info, warn or error.
func installLogger() {
	telemetry.Version = version
	logger := telemetry.NewLogger(os.Stderr, telemetry.ParseLevel(os.Getenv("LOG_LEVEL")))
	slog.SetDefault(logger)
	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))
	klog.SetSlogLogger(logger)
}

// providerCLI is `provider import <grok|cursor>`: copy the operator's own
// provider login from this machine to the provider broker (one-shot, with
// the operator's kubeconfig; nothing keeps running here).
func providerCLI(ctx context.Context, args []string) error {
	usage := errors.New("usage: agent-project-controller provider import <grok|cursor> [--file PATH] [--namespace NAMESPACE] [--replace-account]")
	if len(args) < 2 || args[0] != "import" {
		return usage
	}
	flags := flag.NewFlagSet("provider import", flag.ContinueOnError)
	file := flags.String("file", "", "login file (default: the provider CLI's own, ~/.grok/auth.json or ~/.config/cursor/auth.json)")
	namespace := flags.String("namespace", env("PROVIDER_NAMESPACE", env("CONTROL_NAMESPACE", "agent-project-controller")+"-providers"), "the provider broker's namespace (chart providerBroker.namespace)")
	replace := flags.Bool("replace-account", false, "allow a different Cursor account than the one already signed in")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
		return usage
	}
	config := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	if e := clientgoscheme.AddToScheme(scheme); e != nil {
		return e
	}
	c, e := client.New(config, client.Options{Scheme: scheme})
	if e != nil {
		return e
	}
	return providerbroker.Import(ctx, c, providerbroker.ImportOptions{Provider: args[1], File: *file, Namespace: *namespace, ReplaceAccount: *replace}, os.Stdout)
}
