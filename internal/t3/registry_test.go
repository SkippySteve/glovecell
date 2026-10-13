package t3

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"net/http/httptest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

const testProjectID = "f7e715fc-c958-43eb-ac33-fca86039a241"

func registryFixture(t *testing.T) (*Registry, *api.ProjectWorkspace, *bool) {
	t.Helper()
	ctx := context.Background()
	fail := false
	present := true
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "private detail", 503)
			return
		}
		if r.Method == "DELETE" {
			present = false
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
		if present {
			fmt.Fprintf(w, `{"environments":[{"environmentId":%q}]}`, testProjectID)
		} else {
			fmt.Fprint(w, `{"environments":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Namespace: "control", Name: "disposable", UID: types.UID(testProjectID)}, Spec: api.WorkspaceSpec{DesiredState: "Running"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(w).Build()
	r := &Registry{Client: c, Namespace: "control", Cloud: &Cloud{URL: server.URL, Tokens: fixedToken("header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"owner-user"}`)) + ".signature"), HTTP: server.Client()}}
	if _, e := r.Intent(ctx, w, testProjectID, "pinned-key", "owner-user"); e != nil {
		t.Fatal(e)
	}
	return r, w, &fail
}
func TestInterruptedRegistrationCanRevokeWithoutWorker(t *testing.T) {
	ctx := context.Background()
	r, w, fail := registryFixture(t)
	// The link POST could have succeeded while its response was lost. Intent is
	// enough to revoke the exact environment, without depending on worker storage.
	*fail = true
	if e := r.Remove(ctx, w); e == nil {
		t.Fatal("relay outage reported successful removal")
	}
	s, e := r.Ledger(ctx, w)
	if e != nil || string(s.Data["state"]) != "linking" {
		t.Fatal("interrupted ledger lost", e)
	}
	*fail = false
	if e = r.Remove(ctx, w); e != nil {
		t.Fatal(e)
	}
	s, _ = r.Ledger(ctx, w)
	if string(s.Data["state"]) != "revoked" {
		t.Fatal("revocation not durable")
	}
	if e = r.Remove(ctx, w); e != nil {
		t.Fatal("repeated revocation did not recover", e)
	}
}
func TestMissingOrChangedLedgerDeniesRemovalAndRebinding(t *testing.T) {
	ctx := context.Background()
	r, w, _ := registryFixture(t)
	s, _ := r.Ledger(ctx, w)
	if _, e := r.Intent(ctx, w, "other-project", "pinned-key", "owner-user"); e == nil {
		t.Fatal("another environment accepted")
	}
	if _, e := r.Intent(ctx, w, testProjectID, "replacement-key", "owner-user"); e == nil {
		t.Fatal("replacement signing key accepted")
	}
	old := s.DeepCopy()
	s.Labels[api.OwnerLabel] = "another-owner"
	if e := r.Client.Patch(ctx, s, client.MergeFrom(old)); e != nil {
		t.Fatal(e)
	}
	if e := r.Remove(ctx, w); e == nil {
		t.Fatal("foreign ledger adopted")
	}
	if e := r.Client.Delete(ctx, s); e != nil {
		t.Fatal(e)
	}
	if e := r.Remove(ctx, w); e == nil {
		t.Fatal("missing ledger skipped external revocation")
	}
}

type failedLedgerPatch struct{ client.Client }

func (c failedLedgerPatch) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return errors.New("interrupted durable update")
}
func TestInterruptedRevocationUpdateRecoversFromOriginalIntent(t *testing.T) {
	ctx := context.Background()
	r, w, _ := registryFixture(t)
	original := r.Client
	r.Client = failedLedgerPatch{original}
	if e := r.Remove(ctx, w); e == nil {
		t.Fatal("interrupted persistence reported completed")
	}
	r.Client = original
	if e := r.Remove(ctx, w); e != nil {
		t.Fatal("could not retry after already applied external revocation", e)
	}
}

func TestChangedAccountCannotClaimOriginalEnvironmentWasRevoked(t *testing.T) {
	ctx := context.Background()
	r, w, _ := registryFixture(t)
	r.Cloud.Tokens = fixedToken("header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"another-account"}`)) + ".signature")
	if e := r.Remove(ctx, w); e == nil {
		t.Fatal("another account's catalog used to report revocation")
	}
	ledger, e := r.Ledger(ctx, w)
	if e != nil || string(ledger.Data["state"]) != "linking" {
		t.Fatal("account mismatch changed original registration state", e)
	}
	if _, e := r.Intent(ctx, w, testProjectID, "pinned-key", "another-account"); e == nil {
		t.Fatal("project rebound to another account")
	}
}
