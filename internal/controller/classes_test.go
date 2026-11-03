package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forge"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/providerbroker"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func reconcileOnce(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) (*api.ProjectWorkspace, error) {
	t.Helper()
	key := types.NamespacedName{Namespace: "control", Name: w.Name}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	var current api.ProjectWorkspace
	if getErr := r.Get(context.Background(), key, &current); getErr != nil {
		t.Fatal(getErr)
	}
	return &current, err
}

func saveSpec(t *testing.T, r *Reconciler, w *api.ProjectWorkspace, mutate func(*api.WorkspaceSpec)) {
	t.Helper()
	var current api.ProjectWorkspace
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(w), &current); err != nil {
		t.Fatal(err)
	}
	mutate(&current.Spec)
	if err := r.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	*w = current
}

// ---- ProviderClass ----

// The chart's generated default class resolves to the same lease handling as
// a bare grant before classes existed: the project's documents are identical.
func TestBareProviderGrantUnchangedByDefaultClass(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t, "grok")
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	before := secretData(t, r, w, "model-access")["provider-access"]
	class := &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: "apc-grok", Annotations: map[string]string{api.DefaultClassAnnotation: "true"}}, Spec: api.ProviderClassSpec{Driver: providerbroker.DriverProviderBroker, Provider: "grok", Parameters: map[string]string{providerbroker.ParameterLeasesSecret: leaseKey.String()}}}
	if err := r.Create(ctx, class); err != nil {
		t.Fatal(err)
	}
	if err := r.validate(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if after := secretData(t, r, w, "model-access")["provider-access"]; string(after) != string(before) {
		t.Fatalf("default class changed the grant:\n%s\n%s", before, after)
	}
}

func TestUnknownOrUnsupportedProviderClassBlocks(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		class *api.ProviderClass
		want  string
	}{
		{"absent", nil, "not found"},
		{"openbao", &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: "openbao"}, Spec: api.ProviderClassSpec{Driver: "openbao-broker", Provider: "grok"}}, "not supported"},
		{"magic", &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: "magic"}, Spec: api.ProviderClassSpec{Driver: "magic", Provider: "grok"}}, "unknown provider driver"},
		{"cursor-only", &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: "cursor-only"}, Spec: api.ProviderClassSpec{Driver: providerbroker.DriverProviderBroker, Provider: "cursor"}}, "serves"},
	} {
		r, w := providerFixture(t)
		if tc.class != nil {
			if err := r.Create(ctx, tc.class); err != nil {
				t.Fatal(err)
			}
		}
		saveSpec(t, r, w, func(s *api.WorkspaceSpec) {
			s.Providers = []api.ProviderGrant{{Name: "grok", ProviderClassName: tc.name}}
		})
		if err := r.validate(ctx, w); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: validate = %v", tc.name, err)
		}
		current, err := reconcileOnce(t, r, w)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status.Phase != "Blocked" || secretData(t, r, w, "model-access") != nil || providerHostsOf(t, r, w) != nil {
			t.Fatalf("%s: phase %s, provider access granted", tc.name, current.Status.Phase)
		}
	}
}

func TestProviderClassBoundsApplyToProjectLease(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t)
	capped := &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: "grok-capped"}, Spec: api.ProviderClassSpec{Driver: providerbroker.DriverProviderBroker, Provider: "grok", Lease: &api.ProviderLeaseBounds{MaxDuration: &metav1.Duration{Duration: 2 * time.Minute}}}}
	strict := &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: "cursor-strict"}, Spec: api.ProviderClassSpec{Driver: providerbroker.DriverProviderBroker, Provider: "cursor", Lease: &api.ProviderLeaseBounds{MinDuration: &metav1.Duration{Duration: 10 * time.Minute}}}}
	for _, c := range []client.Object{capped, strict} {
		if err := r.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	w.Spec.Providers = []api.ProviderGrant{{Name: "grok", ProviderClassName: "grok-capped"}, {Name: "cursor", ProviderClassName: "cursor-strict"}}
	if err := r.validate(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	g := grantsOf(t, secretData(t, r, w, "model-access")["provider-access"])
	if !g["grok"].Enabled || g["grok"].Access.Expires > float64(time.Now().Add(2*time.Minute).Unix()) {
		t.Fatalf("maxDuration not applied: %#v", g["grok"].Access)
	}
	if g["cursor"].Enabled || g["cursor"].Access != nil {
		t.Fatal("a lease shorter than minDuration was handed out")
	}
}

// Revoking access through a class (here: the class is deleted) is a
// multi-step write: model-access, then proxy-ca, then the proxy restart. An
// interruption after the first step leaves the worker without the lease and
// the next reconciliation completes the rest.
func TestProviderClassWithdrawalRecoversAfterInterruption(t *testing.T) {
	ctx := context.Background()
	r, w := providerFixture(t)
	class := &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: "grok-a"}, Spec: api.ProviderClassSpec{Driver: providerbroker.DriverProviderBroker, Provider: "grok"}}
	if err := r.Create(ctx, class); err != nil {
		t.Fatal(err)
	}
	w.Spec.Providers = []api.ProviderGrant{{Name: "grok", ProviderClassName: "grok-a"}}
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal(err)
	}
	if g := grantsOf(t, secretData(t, r, w, "proxy-ca")["provider-access"]); !g["grok"].Enabled {
		t.Fatal("class grant not published to the proxy")
	}
	if err := r.Delete(ctx, class); err != nil {
		t.Fatal(err)
	}
	base := r.Client.(client.WithWatch)
	r.Client = interceptor.NewClient(base, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if obj.GetName() == "proxy-ca" {
			return errors.New("interrupted")
		}
		return c.Patch(ctx, obj, patch, opts...)
	}})
	if err := r.ensure(ctx, w); err == nil {
		t.Fatal("interrupted withdrawal reported success")
	}
	if g := grantsOf(t, secretData(t, r, w, "model-access")["provider-access"]); g["grok"].Enabled || g["grok"].Access != nil {
		t.Fatal("worker kept the lease after its class was withdrawn")
	}
	r.Client = base
	if err := r.ensure(ctx, w); err != nil {
		t.Fatal("recovery", err)
	}
	for _, name := range []string{"model-access", "proxy-ca"} {
		data := secretData(t, r, w, name)
		if strings.Contains(string(data["provider-access"]), "grok-lease") {
			t.Fatalf("%s kept the withdrawn lease", name)
		}
	}
	if err := r.validate(ctx, w); err == nil {
		t.Fatal("grant naming a deleted class still validates")
	}
}

// ---- ForgeClass ----

const classAdminToken = "synthetic-second-forge-admin-credential"

type forgeFixture struct {
	r          *Reconciler
	w          *api.ProjectWorkspace
	def, two   *host
	credential string
}

func newForgeFixture(t *testing.T) *forgeFixture {
	t.Helper()
	ctx := context.Background()
	r, w, def := fixture(t)
	f := &forgeFixture{r: r, w: w, def: def, two: &host{}}
	r.Forges = &forge.Resolver{Client: r.Client, Namespace: "control", Default: &forge.Resolved{Driver: "forgejo", Origin: r.Config.ForgejoOrigin, Owner: r.Config.Owner, Forge: def}, Drivers: map[string]forge.Driver{
		forge.DriverForgejo: func(spec api.ForgeClassSpec, credential string) (forge.Forge, string, string, error) {
			f.credential = credential
			return f.two, strings.TrimSuffix(spec.BaseURL, "/"), spec.Parameters["owner"], nil
		},
	}}
	if err := r.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "forge-two", Namespace: "control"}, Data: map[string][]byte{"token": []byte(classAdminToken)}}); err != nil {
		t.Fatal(err)
	}
	f.createClass(t, "forgejo")
	saveSpec(t, r, w, func(s *api.WorkspaceSpec) {
		s.Repository = "team/project"
		s.ForgeClassName = "two"
	})
	return f
}

func (f *forgeFixture) createClass(t *testing.T, driver string) {
	t.Helper()
	class := &api.ForgeClass{ObjectMeta: metav1.ObjectMeta{Name: "two"}, Spec: api.ForgeClassSpec{Driver: driver, BaseURL: "https://git.two.example", CredentialsSecretRef: api.SecretKeyReference{Name: "forge-two", Key: "token"}, Parameters: map[string]string{"owner": "team"}}}
	if err := f.r.Create(context.Background(), class); err != nil {
		t.Fatal(err)
	}
}

func (f *forgeFixture) deleteClass(t *testing.T) {
	t.Helper()
	if err := f.r.Delete(context.Background(), &api.ForgeClass{ObjectMeta: metav1.ObjectMeta{Name: "two"}}); err != nil {
		t.Fatal(err)
	}
}

// projectObjects returns every object the controller wrote for the project,
// serialized, so tests can look for leaked credentials.
func projectObjects(t *testing.T, r *Reconciler, w *api.ProjectWorkspace) string {
	t.Helper()
	ctx := context.Background()
	var out strings.Builder
	var secrets corev1.SecretList
	var maps corev1.ConfigMapList
	var deployments appsv1.DeploymentList
	for _, list := range []client.ObjectList{&secrets, &maps, &deployments} {
		if err := r.List(ctx, list, client.InNamespace(namespace(w))); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(list)
		out.Write(raw)
	}
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(sandboxGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "project"}, sandbox); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sandbox.Object)
	out.Write(raw)
	return out.String()
}

func TestForgeClassWorkspaceUsesItsForge(t *testing.T) {
	f := newForgeFixture(t)
	if _, err := reconcileOnce(t, f.r, f.w); err != nil {
		t.Fatal(err)
	}
	if f.two.grantCalls == 0 || f.def.grantCalls != 0 {
		t.Fatalf("grants: class forge %d, configured forge %d", f.two.grantCalls, f.def.grantCalls)
	}
	if f.credential != classAdminToken {
		t.Fatal("class credential not read from the controller namespace")
	}
	objects := projectObjects(t, f.r, f.w)
	if !strings.Contains(objects, "https://git.two.example") || !strings.Contains(objects, "git.two.example") {
		t.Fatal("worker origin or proxy rule does not use the class forge")
	}
	if strings.Contains(objects, "git.example.com") {
		t.Fatal("project still reaches the configured forge")
	}
	// Credentials never land in the sandbox: neither the class's
	// administrative credential nor a reference to its Secret.
	if strings.Contains(objects, classAdminToken) || strings.Contains(objects, "forge-two") {
		t.Fatal("forge class credential reached the project namespace")
	}
}

func TestForgeClassUnsupportedOrMissingFailsClosed(t *testing.T) {
	f := newForgeFixture(t)
	f.deleteClass(t)
	f.createClass(t, "github")
	current, err := reconcileOnce(t, f.r, f.w)
	if err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(current.Status.Conditions, "Ready")
	if current.Status.Phase != "Blocked" || ready == nil || !strings.Contains(ready.Message, "not supported") {
		t.Fatalf("phase %s, condition %+v", current.Status.Phase, ready)
	}
	if f.two.grantCalls != 0 || f.def.grantCalls != 0 {
		t.Fatal("unsupported forge class granted access")
	}
	f.deleteClass(t)
	if current, _ = reconcileOnce(t, f.r, f.w); current.Status.Phase != "Blocked" || f.def.grantCalls != 0 {
		t.Fatal("missing forge class fell back to the configured forge")
	}
	f.r.Forges = nil
	if current, _ = reconcileOnce(t, f.r, f.w); current.Status.Phase != "Blocked" || f.def.grantCalls != 0 {
		t.Fatal("controller without class resolution granted a named class")
	}
	// Nothing was ever granted, so removal completes without the forge.
	saveSpec(t, f.r, f.w, func(s *api.WorkspaceSpec) { s.DesiredState = "Removed" })
	for i := 0; i < 5 && current.Status.Phase != "Removed"; i++ {
		current, _ = reconcileOnce(t, f.r, f.w)
	}
	if current.Status.Phase != "Removed" || f.def.revokeCalls != 0 || f.two.revokeCalls != 0 {
		t.Fatalf("never-granted workspace: phase %s", current.Status.Phase)
	}
}

// Removal never reports completion without revoking on the forge that
// granted access, and never revokes on another forge.
func TestForgeClassRemovalWaitsForItsForge(t *testing.T) {
	f := newForgeFixture(t)
	if _, err := reconcileOnce(t, f.r, f.w); err != nil {
		t.Fatal(err)
	}
	f.deleteClass(t)
	saveSpec(t, f.r, f.w, func(s *api.WorkspaceSpec) { s.DesiredState = "Removed" })
	for i := 0; i < 2; i++ {
		current, err := reconcileOnce(t, f.r, f.w)
		if err == nil || current.Status.Phase != "Removing" {
			t.Fatalf("removal without its forge: phase %s, err %v", current.Status.Phase, err)
		}
	}
	if f.two.revokeCalls != 0 || f.def.revokeCalls != 0 {
		t.Fatal("revoked on the wrong forge")
	}
	f.createClass(t, "forgejo")
	var current *api.ProjectWorkspace
	for i := 0; i < 5; i++ {
		current, _ = reconcileOnce(t, f.r, f.w)
		if current.Status.Phase == "Removed" {
			break
		}
	}
	if current.Status.Phase != "Removed" || f.two.revokeCalls == 0 || f.def.revokeCalls != 0 {
		t.Fatalf("phase %s, class revokes %d, configured revokes %d", current.Status.Phase, f.two.revokeCalls, f.def.revokeCalls)
	}
}

func TestClassStatusAccepted(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	_ = api.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	two := &api.ForgeClass{ObjectMeta: metav1.ObjectMeta{Name: "two"}, Spec: api.ForgeClassSpec{Driver: "forgejo", BaseURL: "https://git.two.example", CredentialsSecretRef: api.SecretKeyReference{Name: "forge-two", Key: "token"}, Parameters: map[string]string{"owner": "team"}}}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&api.ForgeClass{}, &api.ProviderClass{}).WithObjects(two, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "forge-two", Namespace: "control"}, Data: map[string][]byte{"token": []byte(classAdminToken)}}).Build()
	classes := &ClassReconciler{Client: c, ProviderLeases: &leaseKey, Forges: &forge.Resolver{Client: c, Namespace: "control"}}
	if _, err := classes.reconcileForge(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "two"}}); err != nil {
		t.Fatal(err)
	}
	var fc api.ForgeClass
	if err := c.Get(ctx, types.NamespacedName{Name: "two"}, &fc); err != nil {
		t.Fatal(err)
	}
	if cond := meta.FindStatusCondition(fc.Status.Conditions, AcceptedCondition); cond == nil || cond.Status != metav1.ConditionTrue || fc.Status.ObservedGeneration != fc.Generation {
		t.Fatalf("forge class status %+v", fc.Status)
	}
	if strings.Contains(fmt.Sprint(fc.Status), classAdminToken) {
		t.Fatal("class status exposes the credential")
	}
	for _, pc := range []*api.ProviderClass{
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Annotations: map[string]string{api.DefaultClassAnnotation: "true"}}, Spec: api.ProviderClassSpec{Driver: providerbroker.DriverProviderBroker, Provider: "grok"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Annotations: map[string]string{api.DefaultClassAnnotation: "true"}}, Spec: api.ProviderClassSpec{Driver: providerbroker.DriverProviderBroker, Provider: "grok"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "c"}, Spec: api.ProviderClassSpec{Driver: "api-key-secret", Provider: "grok"}},
	} {
		if err := c.Create(ctx, pc); err != nil {
			t.Fatal(err)
		}
		if _, err := classes.reconcileProvider(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: pc.Name}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"b", "c"} {
		var pc api.ProviderClass
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &pc); err != nil {
			t.Fatal(err)
		}
		if cond := meta.FindStatusCondition(pc.Status.Conditions, AcceptedCondition); cond == nil || cond.Status != metav1.ConditionFalse {
			t.Fatalf("%s accepted: %+v", name, pc.Status)
		}
	}
}
