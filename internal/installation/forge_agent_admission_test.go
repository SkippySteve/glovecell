package installation

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The forge agent boundary must hold on its own: the controller's ClusterRole
// can write Deployments and RoleBindings anywhere, so the chart's policy has to
// refuse every change the controller attempts in the agent's namespace. Here
// the controller is given cluster-admin, so only admission can refuse it.
func TestForgeAgentScopeOnAPIServer(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set APC_HELM_TEST=true with Helm installed and KUBEBUILDER_ASSETS for a local API server")
	}
	values := admissionValues()
	values["forgeAgent"] = map[string]any{"enabled": true}
	var objs []client.Object
	for _, o := range renderAdmission(t, values) {
		if strings.HasSuffix(o.GetName(), "-forge-agent-scope") {
			objs = append(objs, o)
		}
	}
	if len(objs) != 2 {
		t.Fatalf("forge agent policy and binding not rendered: %d objects", len(objs))
	}
	agentNS := admissionNamespace + "-forge"
	controllerUser := "system:serviceaccount:" + admissionNamespace + ":controller"

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
	for _, ns := range []string{admissionNamespace, agentNS, "elsewhere"} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := admin.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "forge-agent", Namespace: agentNS}}); err != nil {
		t.Fatal(err)
	}
	if err := admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "controller-everything"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "controller", Namespace: admissionNamespace}}}); err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("install %s: %v", o.GetName(), err)
		}
	}
	asController := rest.CopyConfig(cfg)
	asController.Impersonate = rest.ImpersonationConfig{UserName: controllerUser, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + admissionNamespace, "system:authenticated"}}
	ctl, err := client.New(asController, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": "x"}
	deployment := func(ns, name string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x"}}}}}}
	}
	// Wait until the binding takes effect.
	for i := 0; ; i++ {
		err = ctl.Create(ctx, deployment(agentNS, "probe"))
		if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
			break
		}
		if err == nil {
			_ = admin.Delete(ctx, deployment(agentNS, "probe"))
		}
		if ctx.Err() != nil {
			t.Fatal("policy never took effect")
		}
		time.Sleep(200 * time.Millisecond)
	}
	denied := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "forge agent's namespace") {
			t.Errorf("%s: controller not refused: %v", name, err)
		}
	}
	// What the review named: patch the agent's Deployment, bind a Secret-reading
	// role in its namespace; plus tokens, pods, Secrets and the namespace itself.
	if err := admin.Create(ctx, deployment(agentNS, "forge-agent")); err != nil {
		t.Fatal(err)
	}
	d := deployment(agentNS, "forge-agent")
	denied("patch agent Deployment", ctl.Patch(ctx, d, client.RawPatch("application/strategic-merge-patch+json", []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"c","image":"evil"}]}}}}`))))
	denied("delete agent Deployment", ctl.Delete(ctx, deployment(agentNS, "forge-agent")))
	denied("bind project access in agent namespace", ctl.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "steal", Namespace: agentNS},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "project-access"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "controller", Namespace: admissionNamespace}}}))
	denied("create Secret", ctl.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: agentNS}}))
	denied("create pod", ctl.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: agentNS}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x"}}}}))
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "forge-agent", Namespace: agentNS}}
	denied("mint agent token", ctl.SubResource("token").Create(ctx, sa, &authTokenRequest))
	ns := &corev1.Namespace{}
	if err := admin.Get(ctx, client.ObjectKey{Name: agentNS}, ns); err != nil {
		t.Fatal(err)
	}
	ns.Labels = map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}
	denied("relabel agent namespace", ctl.Update(ctx, ns))
	denied("delete agent namespace", ctl.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentNS}}))

	// Elsewhere the controller is unaffected, and other users are unaffected here.
	if err := ctl.Create(ctx, deployment("elsewhere", "fine")); err != nil {
		t.Errorf("controller refused outside the agent namespace: %v", err)
	}
	if err := admin.Create(ctx, deployment(agentNS, "operator")); err != nil {
		t.Errorf("operator refused in the agent namespace: %v", err)
	}
}

var authTokenRequest = authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: []string{"agent-project-controller-forge-agent"}}}
