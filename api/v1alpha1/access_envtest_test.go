package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestAccessCRDValidation(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run a local Kubernetes API server")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	s := runtime.NewScheme()
	_ = AddToScheme(s)
	_ = corev1.AddToScheme(s)
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "control"}}); err != nil {
		t.Fatal(err)
	}

	// Profiles: read-only methods and known levels only.
	for name, spec := range map[string]AccessProfileSpec{
		"bad-level":     {Level: "wide"},
		"write-rule":    {Level: AccessLevelCustom, Rules: []EgressRule{{HTTPS: &HTTPSEgressRule{Hostnames: []string{"api.example.com"}, Methods: []string{"PUT"}, Paths: []string{"/"}}}}},
		"no-protocol":   {Level: AccessLevelCustom, Rules: []EgressRule{{}}},
		"other-port":    {Level: AccessLevelCustom, Rules: []EgressRule{{HTTPS: &HTTPSEgressRule{Hostnames: []string{"api.example.com"}, Ports: &EgressPorts{Numbers: []int32{8443}}, Methods: []string{"GET"}, Paths: []string{"/"}}}}},
		"deep-wildcard": {Level: AccessLevelCustom, Rules: []EgressRule{{HTTPS: &HTTPSEgressRule{Hostnames: []string{"*.*.example.com"}, Methods: []string{"GET"}, Paths: []string{"/"}}}}},
		"any-host":      {Level: AccessLevelCustom, Rules: []EgressRule{{HTTPS: &HTTPSEgressRule{Hostnames: []string{"*"}, Methods: []string{"GET"}, Paths: []string{"/"}}}}},
		"url-host":      {Level: AccessLevelCustom, Rules: []EgressRule{{HTTPS: &HTTPSEgressRule{Hostnames: []string{"https://api.example.com"}, Methods: []string{"GET"}, Paths: []string{"/"}}}}},
		"bad-grant":     {Level: AccessLevelCustom, AllowedGrants: []AccessGrantType{"shell"}},
	} {
		if err = c.Create(ctx, &AccessProfile{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}); !apierrors.IsInvalid(err) {
			t.Fatal(name, "accepted", err)
		}
	}
	docs := []EgressRule{{HTTPS: &HTTPSEgressRule{Hostnames: []string{"docs.example.com", "*.docs.example.org"}, Ports: &EgressPorts{Numbers: []int32{443}}, Methods: []string{"GET", "HEAD"}, Paths: []string{"/"}}}}
	if err = c.Create(ctx, &AccessProfile{ObjectMeta: metav1.ObjectMeta{Name: "docs"}, Spec: AccessProfileSpec{Level: AccessLevelCustom, Rules: docs}}); err != nil {
		t.Fatal("Substrate-shaped rule refused", err)
	}
	if err = c.Create(ctx, &AccessProfile{ObjectMeta: metav1.ObjectMeta{Name: "standard"}, Spec: AccessProfileSpec{Level: AccessLevelStandard, Registries: []string{"npm"},
		MaxGrantDuration: map[AccessGrantType]metav1.Duration{AccessGrantLAN: {Duration: time.Hour}}, AllowedGrants: []AccessGrantType{AccessGrantLAN}}}); err != nil {
		t.Fatal(err)
	}

	// Grants: the member must match the type, and cell and type never change.
	expires := metav1.NewTime(time.Now().Add(time.Hour))
	for name, spec := range map[string]AccessGrantSpec{
		"no-member":    {Cell: "project", Type: AccessGrantLAN, ExpiresAt: expires},
		"wrong-member": {Cell: "project", Type: AccessGrantLAN, Package: &PackageGrant{Ecosystem: "npm", Name: "x"}, ExpiresAt: expires},
		"two-members":  {Cell: "project", Type: AccessGrantLAN, LAN: &LANGrant{Address: "192.168.1.2", Ports: []int32{22}}, Internet: &InternetGrant{}, ExpiresAt: expires},
		"bad-address":  {Cell: "project", Type: AccessGrantLAN, LAN: &LANGrant{Address: "192.168.1.0/24", Ports: []int32{22}}, ExpiresAt: expires},
	} {
		if err = c.Create(ctx, &AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "control"}, Spec: spec}); !apierrors.IsInvalid(err) {
			t.Fatal(name, "accepted", err)
		}
	}
	g := &AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "control"}, Spec: AccessGrantSpec{Cell: "project", Type: AccessGrantLAN, LAN: &LANGrant{Address: "192.168.1.2", Ports: []int32{22}}, ExpiresAt: expires}}
	if err = c.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	g.Spec.Cell = "other"
	if err = c.Update(ctx, g); !apierrors.IsInvalid(err) {
		t.Fatal("cell change accepted", err)
	}

	// Cells name a profile.
	w := &ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control"}, Spec: WorkspaceSpec{Repository: "operator/repo", DesiredState: "Running", AccessProfileName: "standard"}}
	if err = c.Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = c.Get(ctx, client.ObjectKeyFromObject(w), w); err != nil || w.Spec.AccessProfileName != "standard" {
		t.Fatal("accessProfileName not stored", err)
	}
}
