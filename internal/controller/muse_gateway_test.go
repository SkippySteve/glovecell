package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMuseGatewayCredentialRenewalRevocationAndRecovery(t *testing.T) {
	ctx := context.Background()
	r, w, _ := fixture(t)
	w.Spec.DesiredState = "Paused"
	w.Spec.HTTPS = []api.HTTPSRule{{Host: "api.meta.ai", Methods: []string{"GET", "POST"}, Paths: []string{"/v1/models", "/v1/responses"}}}
	w.Generation++
	if err := r.Update(ctx, w); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "subscription")
	r.MuseSubscriptionKeyFile = key
	ledger, err := r.state(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	for _, material := range []string{"fixture-subscription", "renewed-subscription", "", "recovered-subscription"} {
		if material == "" {
			if err := os.Remove(key); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(key, []byte(material), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.objects(ctx, w, ledger, nil); err != nil {
			t.Fatal(err)
		}
		var worker, gateway corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "git-identity"}, &worker); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace(w), Name: "proxy-ca"}, &gateway); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(worker.Data)
		if string(gateway.Data["muse-subscription-key"]) != material || material != "" && strings.Contains(string(raw), material) || len(worker.Data["muse-subscription-key"]) != 0 {
			t.Fatal("subscription grant leaked or lifecycle update failed")
		}
	}
}
