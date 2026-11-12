package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnrollmentAPIAllowsReadyOrPausedAndKeepsOneWorkbench(t *testing.T) {
	for _, test := range []struct {
		desired, phase, version, project, githubOwner string
		status                                        int
	}{
		{"Running", "Preparing", "12", `{"name":"extra","host":"forgejo","repository":"steve/extra"}`, "", 409},
		{"Running", "Ready", "12", `{"name":"extra","host":"forgejo","repository":"steve/extra"}`, "", 202},
		{"Running", "Ready", "stale", `{"name":"extra","host":"forgejo","repository":"steve/extra"}`, "", 202},
		{"Paused", "Paused", "12", `{"name":"extra","host":"forgejo","repository":"other/extra"}`, "", 400},
		{"Paused", "Paused", "12", `{"name":"extra","host":"forgejo","repository":"steve/root"}`, "", 409},
		{"Paused", "Paused", "12", `{"name":"private","host":"github","repository":"owner/private"}`, "", 400},
		{"Paused", "Paused", "12", `{"name":"private","host":"github","repository":"other/private"}`, "owner", 400},
		{"Paused", "Paused", "12", `{"name":"extra","host":"forgejo","repository":"steve/extra"}`, "", 202},
		{"Paused", "Paused", "12", `{"name":"public","host":"github","repository":"upstream/library","public":true}`, "", 202},
	} {
		scheme := runtime.NewScheme()
		_ = api.AddToScheme(scheme)
		original := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "bench", Namespace: "control", UID: types.UID("existing-workbench"), ResourceVersion: "12", Generation: 1}, Spec: api.WorkspaceSpec{Repository: "steve/root", DesiredState: api.DesiredState(test.desired)}, Status: api.WorkspaceStatus{Phase: api.Phase(test.phase), ObservedGeneration: 1}}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(original).Build()
		password := strings.Repeat("a", 32)
		s := &Server{Client: c, Host: host{}, Namespace: "control", Owner: "steve", Password: password, Origin: "https://projects.example.com", AllowBasic: true, GitHubOwner: test.githubOwner}
		handler, err := s.Handler()
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/bench/repositories", strings.NewReader(`{"resourceVersion":"`+test.version+`","project":`+test.project+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+password)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("%+v: HTTP %d %s", test, rec.Code, rec.Body.String())
		}
		var list api.ProjectWorkspaceList
		if err := c.List(context.Background(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != 1 || list.Items[0].UID != original.UID || list.Items[0].Spec.Repository != "steve/root" {
			t.Fatal("enrollment created or replaced a workbench")
		}
		if test.status == 202 && len(list.Items[0].Spec.Repositories) != 1 {
			t.Fatal("enrollment not saved")
		}
	}
}
