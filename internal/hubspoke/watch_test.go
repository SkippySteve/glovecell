package hubspoke

import (
	"context"
	"errors"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProjectPaths(t *testing.T) {
	w := &api.ProjectWorkspace{Spec: api.WorkspaceSpec{
		Repositories: []api.RepositoryProject{{Name: "docs"}, {Name: "docs"}, {Name: " app "}},
	}}
	got := projectPaths(w, "/workspace/repo")
	want := []string{"/workspace/repo", "/workspace/projects/docs", "/workspace/projects/app"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

type stubEnroller struct {
	kind  string
	paths []string
	fail  string
}

func (s *stubEnroller) Kind() string { return s.kind }
func (s *stubEnroller) Enroll(context.Context, string, Offer) (Enrolled, error) {
	return Enrolled{}, errors.New("unexpected enroll")
}
func (s *stubEnroller) Connect(context.Context, string) error { return nil }
func (s *stubEnroller) RegisterProject(_ context.Context, _, path string) error {
	if path == s.fail {
		return errors.New("register failed")
	}
	s.paths = append(s.paths, path)
	return nil
}
func (s *stubEnroller) Revoke(context.Context, string) error { return nil }
func (s *stubEnroller) List(context.Context) ([]Enrolled, error) { return nil, nil }

func TestSyncProjectsRegistersExtras(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	uid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	w := &api.ProjectWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "control", UID: types.UID(uid)},
		Spec: api.WorkspaceSpec{
			DesiredState: "Running",
			Repositories: []api.RepositoryProject{{Name: "docs", Host: "forgejo", Repository: "steve/docs"}},
		},
	}
	enroller := &stubEnroller{kind: "orca"}
	opts := WatchOptions{ControlNamespace: "control", Kind: "orca", ProjectPath: "/workspace/repo"}
	rec := LedgerRecord{State: StateEnrolled, ID: "env-1", Name: "demo", Projects: []string{"/workspace/repo"}}
	if err := syncProjects(context.Background(), c, enroller, opts, w, rec); err != nil {
		t.Fatal(err)
	}
	if len(enroller.paths) != 1 || enroller.paths[0] != "/workspace/projects/docs" {
		t.Fatalf("registered %v", enroller.paths)
	}
	got, err := GetState(context.Background(), c, "control", uid, "orca")
	if err != nil {
		t.Fatal(err)
	}
	if joinProjects(got.Projects) != "/workspace/repo\n/workspace/projects/docs" {
		t.Fatalf("ledger projects %q", joinProjects(got.Projects))
	}
	// Second sync is a no-op.
	enroller.paths = nil
	if err := syncProjects(context.Background(), c, enroller, opts, w, got); err != nil {
		t.Fatal(err)
	}
	if len(enroller.paths) != 0 {
		t.Fatalf("unexpected re-register %v", enroller.paths)
	}
}

func TestLedgerProjectsRoundTrip(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	uid := "11111111-2222-3333-4444-555555555555"
	rec := LedgerRecord{State: StateEnrolled, ID: "e", Name: "n", Projects: []string{"/workspace/repo", "/workspace/projects/docs"}}
	if err := SetState(context.Background(), c, "ns", uid, "orca", rec); err != nil {
		t.Fatal(err)
	}
	got, err := GetState(context.Background(), c, "ns", uid, "orca")
	if err != nil {
		t.Fatal(err)
	}
	if joinProjects(got.Projects) != joinProjects(rec.Projects) {
		t.Fatalf("got %#v", got.Projects)
	}
}
