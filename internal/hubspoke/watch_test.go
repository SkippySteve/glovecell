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

func TestShouldRevokeKeepsPaused(t *testing.T) {
	paused := &api.ProjectWorkspace{
		Spec:   api.WorkspaceSpec{DesiredState: "Paused"},
		Status: api.WorkspaceStatus{Phase: "Paused"},
	}
	if shouldRevoke(paused) {
		t.Fatal("paused workspace must keep hubspoke enrollment")
	}
	running := &api.ProjectWorkspace{
		Spec:   api.WorkspaceSpec{DesiredState: "Running"},
		Status: api.WorkspaceStatus{Phase: "Ready"},
	}
	if shouldRevoke(running) {
		t.Fatal("running workspace must not revoke")
	}
	removed := &api.ProjectWorkspace{
		Spec:   api.WorkspaceSpec{DesiredState: "Removed"},
		Status: api.WorkspaceStatus{Phase: "Removing"},
	}
	if !shouldRevoke(removed) {
		t.Fatal("removed workspace must revoke")
	}
	deleting := &api.ProjectWorkspace{
		ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time}},
		Spec:       api.WorkspaceSpec{DesiredState: "Running"},
		Status:     api.WorkspaceStatus{Phase: "Ready"},
	}
	if !shouldRevoke(deleting) {
		t.Fatal("deleting workspace must revoke")
	}
}

func TestSyncOnceDoesNotRevokePaused(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	uid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	w := &api.ProjectWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "control", UID: types.UID(uid)},
		Spec:       api.WorkspaceSpec{DesiredState: "Paused", OrchestratorClassName: "orca"},
		Status:     api.WorkspaceStatus{Phase: "Paused"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(w).Build()
	if err := SetState(context.Background(), c, "control", uid, "orca", LedgerRecord{
		State: StateEnrolled, ID: "env-1", Name: "demo", Projects: []string{"/workspace/repo"},
	}); err != nil {
		t.Fatal(err)
	}
	enroller := &recordingEnroller{kind: "orca", list: []Enrolled{{ID: "env-1", Name: "demo"}}}
	opts := WatchOptions{
		ControlNamespace: "control",
		Kind:             "orca",
		ClassName:        "orca",
		ProjectPath:      "/workspace/repo",
		OfferFrom: func(context.Context, *api.ProjectWorkspace, *corev1.Pod) (Offer, error) {
			return Offer{}, errors.New("unexpected offer")
		},
	}
	if err := syncOnce(context.Background(), c, enroller, stubRuntime{}, opts); err != nil {
		t.Fatal(err)
	}
	if enroller.revoked != 0 {
		t.Fatalf("revoked %d times; pause must keep enrollment", enroller.revoked)
	}
	got, err := GetState(context.Background(), c, "control", uid, "orca")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateEnrolled || got.ID != "env-1" {
		t.Fatalf("ledger %#v", got)
	}
}

type recordingEnroller struct {
	kind    string
	list    []Enrolled
	revoked int
	paths   []string
}

func (s *recordingEnroller) Kind() string { return s.kind }
func (s *recordingEnroller) Enroll(context.Context, string, Offer) (Enrolled, error) {
	return Enrolled{}, errors.New("unexpected enroll")
}
func (s *recordingEnroller) Connect(context.Context, string) error { return nil }
func (s *recordingEnroller) RegisterProject(_ context.Context, _, path string) error {
	s.paths = append(s.paths, path)
	return nil
}
func (s *recordingEnroller) Revoke(context.Context, string) error {
	s.revoked++
	return nil
}
func (s *recordingEnroller) List(context.Context) ([]Enrolled, error) { return s.list, nil }

type stubRuntime struct{}

func (stubRuntime) Kind() string                     { return "orca" }
func (stubRuntime) ParseOffer([]byte) (Offer, error) { return Offer{}, errors.New("unused") }

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

// A project that fails to register (for example a clone still arriving after
// Resume) must not cost the spoke its hub id or the projects it already has:
// the next pass retries only the missing project and never enrolls again.
func TestSyncOnceKeepsEnrollmentWhenAProjectFails(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	uid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	w := &api.ProjectWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "control", UID: types.UID(uid)},
		Spec: api.WorkspaceSpec{
			DesiredState:          api.DesiredRunning,
			OrchestratorClassName: "orca",
			Repositories:          []api.RepositoryProject{{Name: "docs", Host: "forgejo", Repository: "steve/docs"}},
		},
		Status: api.WorkspaceStatus{Phase: api.PhaseReady},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(w).Build()
	ctx := context.Background()
	if err := SetState(ctx, c, "control", uid, "orca", LedgerRecord{
		State: StateEnrolled, ID: "env-1", Name: "demo", Projects: []string{"/workspace/repo"},
	}); err != nil {
		t.Fatal(err)
	}
	enroller := &stubEnroller{kind: "orca", fail: "/workspace/projects/docs"}
	opts := WatchOptions{
		ControlNamespace: "control", Kind: "orca", ClassName: "orca", ProjectPath: "/workspace/repo",
		OfferFrom: func(context.Context, *api.ProjectWorkspace, *corev1.Pod) (Offer, error) {
			return Offer{}, errors.New("unexpected offer: the spoke is already enrolled")
		},
	}
	if err := syncOnce(ctx, c, enroller, stubRuntime{}, opts); err != nil {
		t.Fatal(err)
	}
	got, err := GetState(ctx, c, "control", uid, "orca")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateEnrolled || got.ID != "env-1" || joinProjects(got.Projects) != "/workspace/repo" {
		t.Fatalf("after a failed project the ledger is %#v", got)
	}
	enroller.fail = ""
	if err := syncOnce(ctx, c, enroller, stubRuntime{}, opts); err != nil {
		t.Fatal(err)
	}
	if len(enroller.paths) != 1 || enroller.paths[0] != "/workspace/projects/docs" {
		t.Fatalf("registered %v; want only the project that failed", enroller.paths)
	}
	if got, _ = GetState(ctx, c, "control", uid, "orca"); got.State != StateEnrolled || joinProjects(got.Projects) != "/workspace/repo\n/workspace/projects/docs" {
		t.Fatalf("ledger %#v", got)
	}
}

func TestLedgerRecordTransitions(t *testing.T) {
	rec := LedgerRecord{Kind: "orca", State: StateEnrolled, ID: "env-1", Name: "demo", Projects: []string{"/workspace/repo"}}
	pending, err := rec.To(StatePending)
	if err != nil || pending.ID != "env-1" || len(pending.Projects) != 1 {
		t.Fatalf("enrolled -> pending: %#v, %v", pending, err)
	}
	pending.Projects[0] = "changed"
	if rec.Projects[0] != "/workspace/repo" {
		t.Fatal("To must not share the project slice")
	}
	for _, c := range []struct {
		from, to State
		ok       bool
	}{
		{"", StatePending, true},
		{StateAbsent, StateEnrolled, false},
		{StateAbsent, StateRevoked, false},
		{StatePending, StateEnrolled, true},
		{StateEnrolled, StateRevoked, true},
		{StateRevoked, StatePending, false},
		{StateRevoked, StateEnrolled, false},
		{StateEnrolled, "done", false},
	} {
		_, err := LedgerRecord{State: c.from}.To(c.to)
		if (err == nil) != c.ok {
			t.Errorf("%q -> %q: err %v, want ok=%v", c.from, c.to, err, c.ok)
		}
	}
}
