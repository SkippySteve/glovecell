package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/bootstrap"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/connect"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/controller"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubbroker"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubhost"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/preflight"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/projects"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/providerbroker"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/reader"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/t3"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/telemetry"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/web"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
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
func openGitHost() (forgejo.Host, string, string, string, error) {
	switch env("GIT_HOST", "forgejo") {
	case "forgejo":
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
		case "proxy":
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
	shutdown, e := telemetry.Setup(ctx, os.Getenv("OTEL_ENABLED") == "true")
	if e != nil {
		slog.Warn("optional telemetry unavailable; controller enforcement continues")
		shutdown = func(context.Context) error { return nil }
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = shutdown(c)
	}()
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
	reconciler := &controller.Reconciler{LAN: lan, Client: direct, Host: host, Network: networkProvider, Config: controller.Config{Namespace: ns, GitHost: gitHost, ForgejoOrigin: origin, Owner: owner, Image: os.Getenv("WORKSPACE_IMAGE"), ProxyImage: os.Getenv("PROXY_IMAGE"), RuntimeClass: os.Getenv("RUNTIME_CLASS"), StorageClass: os.Getenv("STORAGE_CLASS"), StorageSize: env("STORAGE_SIZE", "10Gi"), WorkerResources: profile("WORKSPACE"), ProxyResources: profile("PROXY"), WorkerScheduling: workerScheduling, ProxyScheduling: proxyScheduling, RegistrarNamespace: os.Getenv("REGISTRAR_NAMESPACE"), WorkerExecClusterRole: os.Getenv("WORKER_EXEC_CLUSTER_ROLE"), ProjectAccessClusterRole: os.Getenv("PROJECT_ACCESS_CLUSTER_ROLE"), ControllerServiceAccount: env("CONTROLLER_SERVICE_ACCOUNT", controller.DefaultControllerServiceAccount)}}
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
	reconciler.IndependentCodexWorkspaceUID = os.Getenv("INDEPENDENT_CODEX_WORKSPACE_UID")
	reconciler.MuseSubscriptionKeyFile = os.Getenv("MUSE_SUBSCRIPTION_KEY_FILE")
	if reconciler.ProviderLeases, e = providerLeasesFromEnv(networkProvider); e != nil {
		return e
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
		reconciler.Access = access
		if e = manager.Add(access); e != nil {
			return e
		}
	}
	if e = reconciler.Setup(manager); e != nil {
		return e
	}
	if e = manager.AddHealthzCheck("healthz", healthz.Ping); e != nil {
		return e
	}
	if e = manager.AddReadyzCheck("readyz", healthz.Ping); e != nil {
		return e
	}
	server := &web.Server{T3Connect: reconciler.Access != nil, Client: direct, Host: host, Namespace: ns, Owner: owner, Password: password, Origin: os.Getenv("PUBLIC_ORIGIN"), GitHost: gitHost, GitOrigin: origin, AllowBasic: os.Getenv("ALLOW_STATIC_LOGIN") == "true"}
	server.GitHubOwner = reconciler.Config.GitHubOwner
	server.Reference = web.Reference{Digest: reconciler.Config.ReferenceDigest, Rules: reconciler.Config.ReferenceRules}
	if reconciler.ProviderLeases != nil {
		server.Providers = &web.Providers{Status: reconciler.ProviderLeaseStatus}
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
		server.OIDC, e = web.NewOIDC(ctx, web.OIDCConfig{Issuer: os.Getenv("OIDC_ISSUER"), ClientID: os.Getenv("OIDC_CLIENT_ID"), ClientSecret: clientSecret, Origin: server.Origin, GroupsClaim: env("OIDC_GROUPS_CLAIM", "groups"), Scopes: web.CSV(env("OIDC_SCOPES", "profile,groups")), AllowedGroups: web.CSV(os.Getenv("OIDC_ALLOWED_GROUPS")), AllowedSubjects: web.CSV(os.Getenv("OIDC_ALLOWED_SUBJECTS"))})
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if e := run(ctrl.SetupSignalHandler()); e != nil {
		slog.Error("process failed", "error", e)
		os.Exit(1)
	}
}
