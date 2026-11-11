package providerbroker

import (
	"context"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var configured = types.NamespacedName{Namespace: "provider-broker", Name: "provider-leases"}

func providerClass(name, provider string, mutate ...func(*api.ProviderClass)) *api.ProviderClass {
	pc := &api.ProviderClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.ProviderClassSpec{Driver: DriverProviderBroker, Provider: provider}}
	for _, m := range mutate {
		m(pc)
	}
	return pc
}

func asDefault(pc *api.ProviderClass) {
	pc.Annotations = map[string]string{api.DefaultClassAnnotation: "true"}
}

func classResolver(t *testing.T, objects ...client.Object) *ClassResolver {
	t.Helper()
	s := runtime.NewScheme()
	if err := api.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	leases := configured
	return &ClassResolver{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build(), Leases: &leases}
}

func builtinFor(provider string) Class {
	return Class{Provider: provider, Driver: DriverProviderBroker, Leases: configured, MinDuration: time.Minute}
}

// A bare grant translates to exactly the behaviour before classes: the
// configured lease source and a one-minute minimum, with or without the API.
func TestBareGrantResolvesToBuiltinDefault(t *testing.T) {
	ctx := context.Background()
	got, err := classResolver(t).Resolve(ctx, "grok", "")
	if err != nil || got != builtinFor("grok") {
		t.Fatal(got, err)
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	leases := configured
	legacy := &ClassResolver{Client: fake.NewClientBuilder().WithScheme(s).Build(), Leases: &leases}
	if got, err = legacy.Resolve(ctx, "cursor", ""); err != nil || got != builtinFor("cursor") {
		t.Fatal("install without the ProviderClass API", got, err)
	}
	// A non-default class for the provider does not capture bare grants.
	if got, err = classResolver(t, providerClass("grok-short", "grok")).Resolve(ctx, "grok", ""); err != nil || got != builtinFor("grok") {
		t.Fatal(got, err)
	}
	legacy.Leases = nil
	if _, err = legacy.Resolve(ctx, "grok", ""); err == nil {
		t.Fatal("bare grant resolved without a lease source")
	}
	if _, err = classResolver(t).Resolve(ctx, "gemini", ""); err == nil {
		t.Fatal("built-in class served a provider the broker does not renew")
	}
}

// The chart's generated default classes resolve to the same Class as the
// built-in default, so installing them changes nothing.
func TestChartDefaultClassMatchesBuiltin(t *testing.T) {
	pc := providerClass("apc-grok", "grok", asDefault, func(pc *api.ProviderClass) {
		pc.Spec.Parameters = map[string]string{ParameterLeasesSecret: configured.String()}
	})
	got, err := classResolver(t, pc).Resolve(context.Background(), "grok", "")
	want := builtinFor("grok")
	want.Name = "apc-grok"
	if err != nil || got != want {
		t.Fatal(got, err)
	}
}

func TestNamedAndDefaultClassesResolve(t *testing.T) {
	ctx := context.Background()
	bounded := providerClass("grok-bounded", "grok", asDefault, func(pc *api.ProviderClass) {
		pc.Spec.Lease = &api.ProviderLeaseBounds{MinDuration: &metav1.Duration{Duration: 2 * time.Minute}, MaxDuration: &metav1.Duration{Duration: 3 * time.Minute}}
	})
	r := classResolver(t, bounded, providerClass("cursor-a", "cursor"))
	got, err := r.Resolve(ctx, "grok", "")
	if err != nil || got.Name != "grok-bounded" || got.MinDuration != 2*time.Minute || got.MaxDuration != 3*time.Minute {
		t.Fatal(got, err)
	}
	if got, err = r.Resolve(ctx, "cursor", "cursor-a"); err != nil || got.Name != "cursor-a" || got.Leases != configured {
		t.Fatal(got, err)
	}
}

func TestClassResolutionFailsClosed(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		objects   []client.Object
		provider  string
		className string
	}{
		"missing class":     {nil, "grok", "absent"},
		"provider mismatch": {[]client.Object{providerClass("c", "cursor")}, "grok", "c"},
		"reserved openbao":  {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) { pc.Spec.Driver = "openbao-broker" })}, "grok", "c"},
		"reserved api key":  {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) { pc.Spec.Driver = "api-key-secret" })}, "grok", "c"},
		"unknown driver":    {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) { pc.Spec.Driver = "magic" })}, "grok", "c"},
		"unserved provider": {[]client.Object{providerClass("c", "gemini")}, "gemini", "c"},
		"unknown parameter": {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) { pc.Spec.Parameters = map[string]string{"endpoint": "x"} })}, "grok", "c"},
		"foreign leases": {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) {
			pc.Spec.Parameters = map[string]string{ParameterLeasesSecret: "agent-project-controller/controller-secrets"}
		})}, "grok", "c"},
		"malformed leases": {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) { pc.Spec.Parameters = map[string]string{ParameterLeasesSecret: "leases"} })}, "grok", "c"},
		"min too short": {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) {
			pc.Spec.Lease = &api.ProviderLeaseBounds{MinDuration: &metav1.Duration{Duration: 30 * time.Second}}
		})}, "grok", "c"},
		"max under min": {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) {
			pc.Spec.Lease = &api.ProviderLeaseBounds{MinDuration: &metav1.Duration{Duration: 5 * time.Minute}, MaxDuration: &metav1.Duration{Duration: 2 * time.Minute}}
		})}, "grok", "c"},
		"max too long": {[]client.Object{providerClass("c", "grok", func(pc *api.ProviderClass) {
			pc.Spec.Lease = &api.ProviderLeaseBounds{MaxDuration: &metav1.Duration{Duration: 48 * time.Hour}}
		})}, "grok", "c"},
		"two defaults":    {[]client.Object{providerClass("a", "grok", asDefault), providerClass("b", "grok", asDefault)}, "grok", ""},
		"invalid default": {[]client.Object{providerClass("a", "grok", asDefault, func(pc *api.ProviderClass) { pc.Spec.Driver = "openbao-broker" })}, "grok", ""},
	}
	for name, tc := range cases {
		if got, err := classResolver(t, tc.objects...).Resolve(ctx, tc.provider, tc.className); err == nil {
			t.Errorf("%s: resolved %+v", name, got)
		}
	}
	s := runtime.NewScheme()
	leases := configured
	legacy := &ClassResolver{Client: fake.NewClientBuilder().WithScheme(s).Build(), Leases: &leases}
	if _, err := legacy.Resolve(ctx, "grok", "named"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatal("named class resolved without the ProviderClass API", err)
	}
	r := classResolver(t, providerClass("c", "grok"))
	r.Leases = nil
	if _, err := r.Resolve(ctx, "grok", "c"); err == nil {
		t.Fatal("class resolved on a controller without a lease source")
	}
}

func TestBoundNeverExtendsAndWithholdsShortLeases(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	lease := Access{Token: "lease", Expires: float64(now.Add(5 * time.Minute).Unix())}
	if got, ok := builtinFor("grok").Bound(lease, now); !ok || got != lease {
		t.Fatal("built-in bound changed a valid lease", got, ok)
	}
	if _, ok := builtinFor("grok").Bound(Access{Token: "lease", Expires: float64(now.Add(time.Minute).Unix())}, now); ok {
		t.Fatal("lease within a minute of expiry handed out")
	}
	capped := Class{MinDuration: time.Minute, MaxDuration: 2 * time.Minute}
	if got, ok := capped.Bound(lease, now); !ok || got.Expires != float64(now.Add(2*time.Minute).Unix()) {
		t.Fatal("maxDuration did not cap", got, ok)
	}
	long := Class{MinDuration: time.Minute, MaxDuration: time.Hour}
	if got, _ := long.Bound(lease, now); got.Expires != lease.Expires {
		t.Fatal("maxDuration extended a lease", got)
	}
	strict := Class{MinDuration: 10 * time.Minute}
	if _, ok := strict.Bound(lease, now); ok {
		t.Fatal("lease shorter than minDuration handed out")
	}
	if _, ok := (Class{}).Bound(Access{Token: "lease", Expires: float64(now.Add(30 * time.Second).Unix())}, now); ok {
		t.Fatal("zero class weakened the one-minute minimum")
	}
}
