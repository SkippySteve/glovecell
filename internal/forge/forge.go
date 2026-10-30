// Package forge puts the Git hosting services the controller grants access
// on (Forgejo, GitHub) behind one interface, resolved per workspace through a
// cluster-scoped ForgeClass (api/v1alpha1/forgeclass_types.go).
//
// A workspace without spec.forgeClassName uses the controller's configured
// forge (GIT_HOST and its settings), exactly as before classes existed. A
// named class is resolved on every reconciliation: an unknown, unsupported or
// unreadable class fails closed, and removal waits for it to resolve again
// rather than report completion without revoking.
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/githubhost"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Forge is what the controller needs from a Git host: list the owner's
// repositories, grant one workspace identity access to one repository and
// revoke it. Credentials passed in are the workspace's own; the
// administrative credential stays inside the implementation.
type Forge interface {
	Discover(context.Context) ([]forgejo.Repository, error)
	Grant(context.Context, string, forgejo.Credentials) (string, error)
	Revoke(context.Context, string, forgejo.Credentials) error
}

var (
	_ Forge = (*forgejo.Client)(nil)
	_ Forge = (*githubhost.Client)(nil)
)

// Driver names.
const (
	DriverForgejo = "forgejo"
	DriverGitHub  = "github"
	DriverGitLab  = "gitlab"
)

// ReservedDrivers are in the ForgeClass schema but refused as unsupported
// for now. GitHub remains available as the controller's configured forge
// (GIT_HOST=github), not as a class.
var ReservedDrivers = []string{DriverGitHub, DriverGitLab}

// Resolved is one forge ready for use. It holds no credential material
// outside Forge.
type Resolved struct {
	// Class is the ForgeClass name, or "" for the controller's configured forge.
	Class string
	// Driver is "forgejo" or "github"; it selects proxy rules and clone origin.
	Driver string
	// Origin is the forge's HTTPS origin.
	Origin string
	// Owner is the account whose repositories workspaces may use.
	Owner string
	Forge Forge
}

// Driver builds a Forge from a validated class spec and its credential.
type Driver func(spec api.ForgeClassSpec, credential string) (Forge, string, string, error)

// drivers are the class drivers this build implements.
var drivers = map[string]Driver{
	DriverForgejo: func(spec api.ForgeClassSpec, credential string) (Forge, string, string, error) {
		c, err := forgejo.New(spec.BaseURL, spec.Parameters["owner"], credential)
		if err != nil {
			return nil, "", "", err
		}
		return c, c.Origin, c.Owner, nil
	},
}

// parameters lists the keys each driver accepts.
var parameters = map[string][]string{DriverForgejo: {"owner"}}

// Resolver resolves forge class names.
type Resolver struct {
	Client client.Reader
	// Namespace is the controller's namespace; class credentials must be there.
	Namespace string
	// Default is the controller's configured forge, used for an empty name.
	Default *Resolved
	// Drivers overrides the built-in class drivers (tests); nil uses them.
	Drivers map[string]Driver
}

func (r *Resolver) driver(name string) (Driver, bool) {
	if r.Drivers != nil {
		d, ok := r.Drivers[name]
		return d, ok
	}
	d, ok := drivers[name]
	return d, ok
}

// ValidateSpec checks a class without reading its credential.
func ValidateSpec(spec api.ForgeClassSpec, namespace string) error {
	if _, ok := drivers[spec.Driver]; !ok {
		if slices.Contains(ReservedDrivers, spec.Driver) {
			return fmt.Errorf("forge driver %q is reserved and not supported yet", spec.Driver)
		}
		return fmt.Errorf("unknown forge driver %q", spec.Driver)
	}
	u, err := url.Parse(spec.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("baseURL must be an HTTPS origin")
	}
	ref := spec.CredentialsSecretRef
	if ref.Name == "" || ref.Key == "" {
		return errors.New("credentialsSecretRef needs a name and key")
	}
	if ref.Namespace != "" && ref.Namespace != namespace {
		return fmt.Errorf("credentialsSecretRef must be in the controller's namespace %q", namespace)
	}
	for key := range spec.Parameters {
		if !slices.Contains(parameters[spec.Driver], key) {
			return fmt.Errorf("unknown %s parameter %q", spec.Driver, key)
		}
	}
	if spec.Driver == DriverForgejo && strings.TrimSpace(spec.Parameters["owner"]) == "" {
		return errors.New("forgejo requires parameters.owner")
	}
	return nil
}

// Resolve returns the forge for a workspace's spec.forgeClassName.
func (r *Resolver) Resolve(ctx context.Context, name string) (*Resolved, error) {
	if name == "" {
		if r.Default == nil {
			return nil, errors.New("no forge configured")
		}
		return r.Default, nil
	}
	var fc api.ForgeClass
	err := r.Client.Get(ctx, types.NamespacedName{Name: name}, &fc)
	switch {
	case meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err):
		return nil, fmt.Errorf("ForgeClass %q requested, but the ForgeClass API is not installed", name)
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("ForgeClass %q not found", name)
	case err != nil:
		return nil, fmt.Errorf("ForgeClass %q unavailable", name)
	}
	if err := ValidateSpec(fc.Spec, r.Namespace); err != nil {
		return nil, fmt.Errorf("ForgeClass %q: %w", name, err)
	}
	credential, err := r.credential(ctx, fc.Spec.CredentialsSecretRef)
	if err != nil {
		return nil, fmt.Errorf("ForgeClass %q: %w", name, err)
	}
	build, ok := r.driver(fc.Spec.Driver)
	if !ok {
		return nil, fmt.Errorf("ForgeClass %q: forge driver %q is not available", name, fc.Spec.Driver)
	}
	f, origin, owner, err := build(fc.Spec, credential)
	if err != nil {
		return nil, fmt.Errorf("ForgeClass %q: %w", name, err)
	}
	return &Resolved{Class: name, Driver: fc.Spec.Driver, Origin: origin, Owner: owner, Forge: f}, nil
}

// credential reads the class's Secret key from the controller's namespace.
// Errors never include Secret contents.
func (r *Resolver) credential(ctx context.Context, ref api.SecretKeyReference) (string, error) {
	var s corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: ref.Name}, &s); err != nil {
		return "", errors.New("forge credentials unavailable")
	}
	value := strings.TrimSpace(string(s.Data[ref.Key]))
	if value == "" {
		return "", errors.New("forge credentials key missing or empty")
	}
	return value, nil
}
