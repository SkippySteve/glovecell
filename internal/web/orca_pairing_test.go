package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/hubspoke"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestOrcaPairingResponsesNeverPermitCaching(t *testing.T) {
	for _, tc := range []struct {
		name, auth             string
		enabled, worker, offer bool
		status                 int
	}{
		{"offer", "oidc", true, true, true, 200},
		{"unavailable", "oidc", true, true, false, 409},
		{"no-worker", "oidc", true, false, true, 409},
		{"automation", "bearer", true, true, true, 403},
		{"static", "basic", true, true, true, 403},
		{"disabled", "oidc", false, true, true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = api.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			workspace := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "cell", Namespace: "control", UID: types.UID("uid-1")}, Spec: api.WorkspaceSpec{OrchestratorClassName: "orca"}}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workspace)
			if tc.worker {
				builder = builder.WithObjects(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "apc-uid1", Labels: map[string]string{api.OwnerLabel: "uid-1", "projects.agentwork.dev/role": "worker"}}})
			}
			calls := 0
			secret := "orca://pair/fixture-secret"
			s := &Server{Client: builder.Build(), Namespace: "control", OrcaPairing: &OrcaPairing{HubSpoke: tc.enabled, Domain: "orca.example.invalid", OfferFrom: func(context.Context, *api.ProjectWorkspace, *corev1.Pod) (hubspoke.Offer, error) {
				calls++
				if !tc.offer {
					return hubspoke.Offer{}, errors.New(secret)
				}
				return hubspoke.Offer{URL: secret, Scope: "runtime"}, nil
			}}}
			req := httptest.NewRequest("POST", "/api/v1/workspaces/cell/orca-pairing", nil)
			req.SetPathValue("name", "cell")
			req = req.WithContext(context.WithValue(req.Context(), authKey{}, tc.auth))
			rec := httptest.NewRecorder()
			s.mintOrcaPairing(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
				t.Fatal("pairing response may be cached")
			}
			if tc.status == 200 && !strings.Contains(rec.Body.String(), secret) {
				t.Fatal("missing offer")
			}
			if tc.status != 200 && strings.Contains(rec.Body.String(), secret) {
				t.Fatal("error exposed credential")
			}
			if (tc.auth != "oidc" || !tc.enabled || !tc.worker) && calls != 0 {
				t.Fatal("mint executed without prerequisites")
			}
		})
	}
}
