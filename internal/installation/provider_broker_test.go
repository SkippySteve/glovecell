package installation

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func providerValues(broker map[string]any) map[string]any {
	values := databaseValues()
	values["providerBroker"] = broker
	return values
}

// Off by default and inert; on, it installs the broker and one offline owner
// for exactly the listed providers and points the controller at the leases.
func TestChartProviderBroker(t *testing.T) {
	requireHelm(t)
	_, off, err := renderChart(t, databaseValues())
	if err != nil {
		t.Fatal(err)
	}
	if find(off, "Deployment", "provider-broker") != nil || find(off, "StatefulSet", "owners") != nil {
		t.Fatal("provider broker rendered while off")
	}
	if v := dig(controllerConfig(t, off), "data", "PROVIDER_LEASES_SECRET"); v != "" {
		t.Fatalf("lease source set while off: %v", v)
	}

	_, on, err := renderChart(t, providerValues(map[string]any{"enabled": true, "providers": []string{"grok"}}))
	if err != nil {
		t.Fatal(err)
	}
	ns := "test-control-providers"
	if v := dig(controllerConfig(t, on), "data", "PROVIDER_LEASES_SECRET"); v != ns+"/provider-leases" {
		t.Fatalf("controller lease source %v", v)
	}
	if v := dig(find(on, "ConfigMap", "admission-params"), "data", "providersEnabled"); v != "true" {
		t.Fatalf("spec.providers still refused by admission: %v", v)
	}
	if pc := find(on, "ProviderClass", "test-control-qualification-grok"); pc == nil || dig(pc, "spec", "parameters", "leasesSecret") != ns+"/provider-leases" {
		t.Fatalf("grok default class %v", pc)
	}
	if find(on, "ProviderClass", "test-control-qualification-cursor") != nil {
		t.Fatal("class rendered for a provider that is not listed")
	}

	owner := find(on, "StatefulSet", "owners")
	if owner == nil || dig(owner, "metadata", "namespace") != ns || dig(owner, "spec", "replicas") != 1.0 {
		t.Fatalf("owner %v", owner)
	}
	spec := dig(owner, "spec", "template", "spec").(map[string]any)
	if spec["automountServiceAccountToken"] != false || spec["runtimeClassName"] != "gvisor" {
		t.Fatalf("owner pod spec %v", spec)
	}
	var claims []string
	for _, v := range spec["volumes"].([]any) {
		if c := dig(v, "persistentVolumeClaim", "claimName"); c != nil {
			claims = append(claims, c.(string))
		}
	}
	if !slices.Equal(claims, []string{"grok-identity"}) {
		t.Fatalf("owner mounts %v, want only grok-identity", claims)
	}
	if pvc := find(on, "PersistentVolumeClaim", "grok-identity"); pvc == nil || dig(pvc, "metadata", "annotations", "helm.sh/resource-policy") != "keep" {
		t.Fatalf("login claim must survive uninstall: %v", pvc)
	}
	if p := find(on, "CiliumNetworkPolicy", "credential-owner-offline"); p == nil || dig(p, "spec", "egressDeny") == nil {
		t.Fatal("owner is not denied all egress")
	}

	broker := find(on, "Deployment", "provider-broker")
	if broker == nil {
		t.Fatal("broker missing")
	}
	env := map[string]string{}
	for _, e := range dig(broker, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["env"].([]any) {
		m := e.(map[string]any)
		env[m["name"].(string)] = m["value"].(string)
	}
	if env["PROVIDER_BROKER_PROVIDERS"] != "grok" || env["GROK_OWNER_POD"] != "owners-0" || env["GROK_OWNER_NAMESPACE"] != ns || env["PROVIDER_BROKER_CONTROL_NAMESPACE"] != ns {
		t.Fatalf("broker env %v", env)
	}
	if _, ok := env["CURSOR_OAUTH_CLIENT_ID"]; ok {
		t.Fatal("cursor settings rendered without cursor")
	}
	role := find(on, "Role", "provider-broker")
	var execNames []any
	for _, r := range dig(role, "rules").([]any) {
		if slices.Contains(dig(r, "resources").([]any), any("pods/exec")) {
			execNames = dig(r, "resourceNames").([]any)
		}
	}
	if !slices.Equal(execNames, []any{"owners-0", "grok-login"}) {
		t.Fatalf("broker exec scope %v", execNames)
	}
	reader := find(on, "RoleBinding", "provider-leases-reader")
	if s := dig(reader, "subjects").([]any)[0]; dig(s, "name") != "controller" || dig(s, "namespace") != "test-control" {
		t.Fatalf("lease reader subject %v", s)
	}
	if find(on, "Pod", "") != nil {
		t.Fatal("a login pod was rendered by the chart; only the broker creates them during a sign-in")
	}

	// Console sign-in (on by default): the broker serves the controller only,
	// may create only its login pods, and the controller gets its token.
	_, console, err := renderChart(t, providerValues(map[string]any{"enabled": true, "providers": []string{"grok", "cursor"}}))
	if err != nil {
		t.Fatal(err)
	}
	cenv, cvols := controllerPod(t, console)
	if cenv["PROVIDER_BROKER_URL"] != "http://provider-broker."+ns+".svc:8443" || cenv["PROVIDER_BROKER_TOKEN_FILE"] != "/var/run/apc/provider-broker/token" {
		t.Fatalf("controller sign-in env %v", cenv)
	}
	if src := dig(cvols["provider-broker-token"], "projected", "sources").([]any); dig(src[0], "serviceAccountToken", "audience") != "agent-project-controller-provider-broker" {
		t.Fatalf("projected token %v", src)
	}
	benv := map[string]string{}
	for _, e := range dig(find(console, "Deployment", "provider-broker"), "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["env"].([]any) {
		benv[e.(map[string]any)["name"].(string)] = e.(map[string]any)["value"].(string)
	}
	if benv["PROVIDER_BROKER_CALLER"] != "system:serviceaccount:test-control:controller" {
		t.Fatalf("caller %q", benv["PROVIDER_BROKER_CALLER"])
	}
	var template map[string]any
	if err := json.Unmarshal([]byte(benv["PROVIDER_LOGIN_POD"]), &template); err != nil {
		t.Fatal(err)
	}
	for _, v := range template["volumes"].([]any) {
		if dig(v, "emptyDir") == nil {
			t.Fatalf("login pod template mounts %v", v)
		}
	}
	if template["automountServiceAccountToken"] != false || dig(template["containers"].([]any)[0], "image") != "registry.example.com/workspace@sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("login template %v", template)
	}
	for _, p := range []string{"grok", "cursor"} {
		if find(console, "CiliumNetworkPolicy", "credential-login-"+p) == nil {
			t.Fatalf("no sign-in network policy for %s", p)
		}
	}
	if find(console, "Service", "provider-broker") == nil || find(console, "NetworkPolicy", "provider-broker-api") == nil || find(console, "ClusterRoleBinding", "test-control-qualification-provider-broker-review") == nil {
		t.Fatal("sign-in API objects missing")
	}
	_, quiet, err := renderChart(t, providerValues(map[string]any{"enabled": true, "providers": []string{"grok"}, "consoleSignIn": false}))
	if err != nil {
		t.Fatal(err)
	}
	if find(quiet, "Service", "provider-broker") != nil || find(quiet, "ClusterRole", "test-control-qualification-provider-broker-review") != nil {
		t.Fatal("sign-in API rendered while off")
	}
	if env, _ := controllerPod(t, quiet); env["PROVIDER_BROKER_URL"] != "" {
		t.Fatal("controller pointed at a disabled sign-in API")
	}
	for _, r := range dig(find(quiet, "Role", "provider-broker"), "rules").([]any) {
		if slices.Contains(dig(r, "resources").([]any), any("pods")) {
			t.Fatal("broker may create pods with console sign-in off")
		}
	}

	for name, broker := range map[string]map[string]any{
		"no providers":       {"enabled": true},
		"unknown provider":   {"enabled": true, "providers": []string{"codex"}},
		"repeated provider":  {"enabled": true, "providers": []string{"grok", "grok"}},
		"removed login mode": {"enabled": true, "providers": []string{"grok"}, "login": "grok"},
		"controller ns":      {"enabled": true, "providers": []string{"grok"}, "namespace": "test-control"},
		"cursor no client":   {"enabled": true, "providers": []string{"cursor"}, "cursorOAuthClientID": ""},
	} {
		if _, _, err := renderChart(t, providerValues(broker)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	values := providerValues(map[string]any{"enabled": true, "providers": []string{"grok"}})
	values["admissionPolicies"] = map[string]any{"enabled": false}
	if _, _, err := renderChart(t, values); err == nil {
		t.Error("accepted without admission policies")
	}
	values = providerValues(map[string]any{"enabled": true, "providers": []string{"grok"}})
	values["config"].(map[string]any)["PROVIDER_LEASES_SECRET"] = "elsewhere/leases"
	if _, _, err := renderChart(t, values); err == nil {
		t.Error("accepted a second lease source")
	}
	values = providerValues(map[string]any{"enabled": true, "providers": []string{"grok"}})
	values["config"].(map[string]any)["NETWORK_PROVIDER"] = "network-policy"
	values["config"].(map[string]any)["NETWORK_POLICY_ENFORCEMENT_ACKNOWLEDGED"] = "true"
	values["config"].(map[string]any)["NETWORK_POLICY_CLUSTER_CIDRS_JSON"] = `["10.0.0.0/8"]`
	if _, _, err := renderChart(t, values); err == nil {
		t.Error("accepted without Cilium")
	}
}

// The boundary holds on admission alone: with cluster-admin, the controller
// still cannot change anything in the providers' namespace, and the broker
// cannot exec anywhere but the owner pod or attach to it.
func TestProviderBrokerAdmissionOnAPIServer(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set APC_HELM_TEST=true with Helm installed and KUBEBUILDER_ASSETS for a local API server")
	}
	values := admissionValues()
	values["providerBroker"] = map[string]any{"enabled": true, "providers": []string{"grok"}}
	delete(values["config"].(map[string]any), "PROVIDER_LEASES_SECRET")
	delete(values["config"].(map[string]any), "PROVIDER_CELL_TOKENS")
	var objs []client.Object
	for _, o := range renderAdmission(t, values) {
		if strings.HasSuffix(o.GetName(), "-providers-scope") || strings.HasSuffix(o.GetName(), "-provider-broker-exec") || strings.HasSuffix(o.GetName(), "-provider-broker-pods") {
			objs = append(objs, o)
		}
	}
	if len(objs) != 6 {
		t.Fatalf("provider policies and bindings not rendered: %d objects", len(objs))
	}
	providersNS := admissionNamespace + "-providers"

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, ns := range []string{admissionNamespace, providersNS, "elsewhere"} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sa := range []struct{ ns, name string }{{admissionNamespace, "controller"}, {providersNS, "provider-broker"}} {
		if err := admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: sa.name + "-everything"},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
			Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa.name, Namespace: sa.ns}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range objs {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("install %s: %v", o.GetName(), err)
		}
	}
	as := func(ns, name string) *rest.Config {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + ns + ":" + name, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}}
		return c
	}
	ctl, err := client.New(as(admissionNamespace, "controller"), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	pod := func(ns, name string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x"}}}}
	}
	for i := 0; ; i++ {
		err = ctl.Create(ctx, pod(providersNS, "probe"))
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			break
		}
		if err == nil {
			_ = admin.Delete(ctx, pod(providersNS, "probe"))
		}
		if ctx.Err() != nil {
			t.Fatal("policy never took effect")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err == nil || !strings.Contains(err.Error(), "providers' namespace") {
		t.Errorf("controller pod that could mount a login not refused: %v", err)
	}
	if err := ctl.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "steal", Namespace: providersNS},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "controller", Namespace: admissionNamespace}}}); err == nil || !strings.Contains(err.Error(), "providers' namespace") {
		t.Errorf("controller RoleBinding not refused: %v", err)
	}
	if err := ctl.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: providersNS}}); err == nil || !strings.Contains(err.Error(), "providers' namespace") {
		t.Errorf("controller namespace delete not refused: %v", err)
	}
	if err := ctl.Create(ctx, pod("elsewhere", "ok")); err != nil {
		t.Errorf("controller refused outside the providers' namespace: %v", err)
	}

	// Admission runs before the API server looks for the pod's node, so an
	// allowed exec fails later for another reason and a refused one names the policy.
	brokerClient, err := kubernetes.NewForConfig(as(providersNS, "provider-broker"))
	if err != nil {
		t.Fatal(err)
	}
	connect := func(ns, name, sub string) error {
		return brokerClient.CoreV1().RESTClient().Post().Namespace(ns).Resource("pods").Name(name).SubResource(sub).
			Param("command", "true").Param("stdout", "true").Do(ctx).Error()
	}
	refused := func(err error) bool { return err != nil && strings.Contains(err.Error(), "provider broker") }
	if err := connect(providersNS, "owners-0", "exec"); refused(err) {
		t.Errorf("exec into the owner refused: %v", err)
	}
	if err := connect(providersNS, "grok-login", "exec"); refused(err) {
		t.Errorf("exec into the login pod refused: %v", err)
	}
	for _, c := range []struct{ ns, name, sub string }{{providersNS, "other", "exec"}, {"elsewhere", "owners-0", "exec"}, {providersNS, "owners-0", "attach"}, {providersNS, "cursor-login", "exec"}} {
		if err := connect(c.ns, c.name, c.sub); !refused(err) {
			t.Errorf("%s %s/%s not refused: %v", c.sub, c.ns, c.name, err)
		}
	}

	// Pods: the broker may create only the login pod shape. A pod that could
	// mount a login volume, carry a token or run another image is refused.
	if err := admin.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "credential-owner", Namespace: providersNS}}); err != nil {
		t.Fatal(err)
	}
	broker, err := client.New(as(providersNS, "provider-broker"), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	no := false
	login := func(change func(*corev1.Pod)) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "grok-login", Namespace: providersNS, Labels: map[string]string{"app.kubernetes.io/name": "credential-login", "projects.agentwork.dev/login": "grok"}},
			Spec: corev1.PodSpec{ServiceAccountName: "credential-owner", AutomountServiceAccountToken: &no,
				Containers: []corev1.Container{{Name: "login", Image: "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64)}},
				Volumes:    []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}
		if change != nil {
			change(p)
		}
		return p
	}
	podRefused := func(err error) bool {
		return err != nil && (strings.Contains(err.Error(), "Login pods") || strings.Contains(err.Error(), "provider broker"))
	}
	yes := true
	for name, change := range map[string]func(*corev1.Pod){
		"login volume": func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "id", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "grok-identity"}}})
		},
		"other image":  func(p *corev1.Pod) { p.Spec.Containers[0].Image = "evil.example/tools:latest" },
		"token":        func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = &yes },
		"host network": func(p *corev1.Pod) { p.Spec.HostNetwork = true },
		"other name":   func(p *corev1.Pod) { p.Name = "helper" },
		"wrong label":  func(p *corev1.Pod) { p.Labels["projects.agentwork.dev/login"] = "cursor" },
		"two containers": func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, p.Spec.Containers[0])
			p.Spec.Containers[1].Name = "second"
		},
		"elsewhere": func(p *corev1.Pod) { p.Namespace = "elsewhere" },
	} {
		if err := broker.Create(ctx, login(change)); !podRefused(err) {
			t.Errorf("broker created a pod with %s: %v", name, err)
		}
	}
	if err := broker.Create(ctx, login(nil)); err != nil {
		t.Errorf("broker refused its login pod: %v", err)
	}
}
