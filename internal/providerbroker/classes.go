package providerbroker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ProviderClass resolution (api/v1alpha1/providerclass_types.go).
//
// A project's spec.providers[] entry names a provider and, optionally, a
// ProviderClass. The controller resolves it here, on the trusted side, to a
// Class: which driver supplies the lease, from where, and within which
// bounds. Anything it cannot resolve exactly fails closed: the provider stays
// disabled for that project and the project is blocked.
//
// A grant without a class resolves to the provider's default class (the one
// annotated api.DefaultClassAnnotation=true). When none exists, or the
// ProviderClass API is not installed, it resolves to the built-in default,
// which is exactly the behaviour before classes existed: the provider-broker
// driver on the controller's PROVIDER_LEASES_SECRET with a one-minute minimum.

const (
	// DriverProviderBroker reads the lease Secret the provider broker writes
	// in lease mode (leases.go).
	DriverProviderBroker = "provider-broker"
	// ParameterLeasesSecret optionally names that Secret as <namespace>/<name>.
	// It must equal the controller's configured lease source: a class cannot
	// point the controller at any other Secret.
	ParameterLeasesSecret = "leasesSecret"
	// DefaultMinLease is the remaining lifetime a lease needs to be handed to
	// a project when the class does not say otherwise.
	DefaultMinLease = time.Minute
	maxMinLease     = time.Hour
	maxMaxLease     = 24 * time.Hour
)

// ReservedProviderDrivers are named in the API but not implemented. A class
// that uses one is refused until its driver exists.
var ReservedProviderDrivers = []string{"openbao-broker", "api-key-secret"}

// Class is a resolved ProviderClass. It never holds credential material.
type Class struct {
	// Name is the ProviderClass name, or "" for the built-in default.
	Name     string
	Provider string
	Driver   string
	// Leases is the Secret the provider-broker driver reads.
	Leases types.NamespacedName
	// MinDuration and MaxDuration bound the leases handed to projects;
	// MaxDuration 0 keeps the broker's expiry.
	MinDuration, MaxDuration time.Duration
}

// Bound applies the class's lease bounds at now. It reports false when the
// lease must be withheld. It never extends a lease.
func (c Class) Bound(access Access, now time.Time) (Access, bool) {
	min := c.MinDuration
	if min < DefaultMinLease {
		min = DefaultMinLease
	}
	if math.IsNaN(access.Expires) || math.IsInf(access.Expires, 0) || access.Expires <= float64(now.Add(min).Unix()) {
		return Access{}, false
	}
	if c.MaxDuration > 0 {
		access.Expires = math.Min(access.Expires, float64(now.Add(c.MaxDuration).Unix()))
	}
	return access, true
}

// ClassResolver resolves ProviderGrants to Classes.
type ClassResolver struct {
	Client client.Reader
	// Leases is the controller's configured lease source
	// (PROVIDER_LEASES_SECRET). Nil turns brokered providers off.
	Leases *types.NamespacedName
}

// apiMissing reports that the ProviderClass kind is not served (or not in
// the client's scheme): an install that predates classes.
func apiMissing(err error) bool {
	return meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err)
}

func (r *ClassResolver) builtin(provider string) (Class, error) {
	if r.Leases == nil {
		return Class{}, errors.New("brokered providers are not configured on this controller")
	}
	if !slices.Contains(LeaseProviders, provider) {
		return Class{}, fmt.Errorf("the built-in provider class cannot serve %q", provider)
	}
	return Class{Provider: provider, Driver: DriverProviderBroker, Leases: *r.Leases, MinDuration: DefaultMinLease}, nil
}

// Resolve returns the class for one grant: className when set, otherwise the
// provider's default class.
func (r *ClassResolver) Resolve(ctx context.Context, provider, className string) (Class, error) {
	if className != "" {
		var pc api.ProviderClass
		err := r.Client.Get(ctx, types.NamespacedName{Name: className}, &pc)
		switch {
		case apiMissing(err):
			return Class{}, fmt.Errorf("ProviderClass %q requested, but the ProviderClass API is not installed", className)
		case apierrors.IsNotFound(err):
			return Class{}, fmt.Errorf("ProviderClass %q not found", className)
		case err != nil:
			return Class{}, fmt.Errorf("ProviderClass %q unavailable", className)
		case !pc.DeletionTimestamp.IsZero():
			return Class{}, fmt.Errorf("ProviderClass %q is being deleted", className)
		}
		if pc.Spec.Provider != provider {
			return Class{}, fmt.Errorf("ProviderClass %q serves %q, not %q", className, pc.Spec.Provider, provider)
		}
		return ValidateClass(&pc, r.Leases)
	}
	var list api.ProviderClassList
	if err := r.Client.List(ctx, &list); apiMissing(err) {
		return r.builtin(provider)
	} else if err != nil {
		return Class{}, errors.New("provider classes unavailable")
	}
	var defaults []*api.ProviderClass
	for i := range list.Items {
		pc := &list.Items[i]
		if pc.Annotations[api.DefaultClassAnnotation] == "true" && pc.Spec.Provider == provider && pc.DeletionTimestamp.IsZero() {
			defaults = append(defaults, pc)
		}
	}
	switch len(defaults) {
	case 0:
		return r.builtin(provider)
	case 1:
		return ValidateClass(defaults[0], r.Leases)
	default:
		return Class{}, fmt.Errorf("more than one default ProviderClass for %q; name one in providerClassName or remove a default", provider)
	}
}

// ValidateClass checks a ProviderClass against the drivers this build
// implements and the controller's lease source. Unknown or reserved drivers,
// unknown parameters and out-of-range bounds fail closed.
func ValidateClass(pc *api.ProviderClass, leases *types.NamespacedName) (Class, error) {
	c := Class{Name: pc.Name, Provider: pc.Spec.Provider, Driver: pc.Spec.Driver, MinDuration: DefaultMinLease}
	switch {
	case pc.Spec.Driver == DriverProviderBroker:
	case slices.Contains(ReservedProviderDrivers, pc.Spec.Driver):
		return Class{}, fmt.Errorf("driver %q is reserved and not supported by this controller yet", pc.Spec.Driver)
	default:
		return Class{}, fmt.Errorf("unknown provider driver %q", pc.Spec.Driver)
	}
	if !slices.Contains(LeaseProviders, pc.Spec.Provider) {
		return Class{}, fmt.Errorf("driver %s cannot serve provider %q", DriverProviderBroker, pc.Spec.Provider)
	}
	if leases == nil {
		return Class{}, errors.New("brokered providers are not configured on this controller")
	}
	c.Leases = *leases
	for key, value := range pc.Spec.Parameters {
		if key != ParameterLeasesSecret {
			return Class{}, fmt.Errorf("unknown %s parameter %q", DriverProviderBroker, key)
		}
		ns, name, ok := strings.Cut(value, "/")
		if !ok || len(validation.IsDNS1123Label(ns)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
			return Class{}, fmt.Errorf("%s must be <namespace>/<name>", ParameterLeasesSecret)
		}
		if (types.NamespacedName{Namespace: ns, Name: name}) != *leases {
			return Class{}, fmt.Errorf("%s must be the controller's configured lease source", ParameterLeasesSecret)
		}
	}
	if b := pc.Spec.Lease; b != nil {
		if b.MinDuration != nil {
			c.MinDuration = b.MinDuration.Duration
			if c.MinDuration < DefaultMinLease || c.MinDuration > maxMinLease {
				return Class{}, errors.New("lease.minDuration must be between 1m and 1h")
			}
		}
		if b.MaxDuration != nil {
			c.MaxDuration = b.MaxDuration.Duration
			if c.MaxDuration <= c.MinDuration || c.MaxDuration > maxMaxLease {
				return Class{}, errors.New("lease.maxDuration must exceed minDuration and be at most 24h")
			}
		}
	}
	return c, nil
}
