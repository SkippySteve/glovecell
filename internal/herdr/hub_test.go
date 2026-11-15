package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeCore struct {
	machines []Machine
	failAdd  map[string]bool
	calls    []string
}

func (f *fakeCore) List(context.Context) ([]Machine, error) { return slices.Clone(f.machines), nil }

func (f *fakeCore) Add(_ context.Context, name, via string) error {
	f.calls = append(f.calls, "add "+name)
	if f.failAdd[name] {
		return errors.New("unreachable")
	}
	f.machines = append(f.machines, Machine{Name: name, Target: via, Via: true})
	return nil
}

func (f *fakeCore) Remove(_ context.Context, name string) error {
	f.calls = append(f.calls, "rm "+name)
	f.machines = slices.DeleteFunc(f.machines, func(m Machine) bool { return m.Name == name })
	return nil
}

func workspace(name, class string, phase api.Phase) *api.ProjectWorkspace {
	return &api.ProjectWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "control"},
		Spec:       api.WorkspaceSpec{Repository: "operator/" + name, OrchestratorClassName: class},
		Status:     api.WorkspaceStatus{Phase: phase},
	}
}

func enroller(t *testing.T, core Core, objs ...client.Object) *Enroller {
	t.Helper()
	s := runtime.NewScheme()
	if err := api.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return &Enroller{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(), Namespace: "control", Class: "herdr", Core: core}
}

func TestSyncEnrollsReadyHerdrWorkspacesOnly(t *testing.T) {
	core := &fakeCore{machines: []Machine{
		{Name: "gone", Target: MachineViaCommand("gone"), Via: true},
		{Name: "paused", Target: MachineViaCommand("paused"), Via: true},
		{Name: "vps", Target: "vps"},
		{Name: "handmade", Target: "fly ssh console", Via: true},
	}}
	e := enroller(t, core,
		workspace("cell-a", "herdr", api.PhaseReady),
		workspace("cell-b", "herdr", api.PhaseReady),
		workspace("paused", "herdr", "Paused"),
		workspace("orca-cell", "orca", api.PhaseReady),
		workspace("t7", "herdr", api.PhaseReady),
		workspace(strings.Repeat("x", 33), "herdr", api.PhaseReady),
	)
	if err := e.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range core.machines {
		names = append(names, m.Name)
	}
	slices.Sort(names)
	if want := []string{"cell-a", "cell-b", "handmade", "vps"}; !slices.Equal(names, want) {
		t.Fatalf("machines %v, want %v", names, want)
	}
	core.calls = nil
	if err := e.Sync(context.Background()); err != nil || len(core.calls) != 0 {
		t.Fatalf("second sync not idempotent: %v %v", core.calls, err)
	}
}

func TestSyncLeavesHandAddedNameAlone(t *testing.T) {
	core := &fakeCore{machines: []Machine{{Name: "cell-a", Target: "steve@10.0.0.7"}}}
	e := enroller(t, core, workspace("cell-a", "herdr", api.PhaseReady))
	if err := e.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(core.calls) != 0 {
		t.Fatalf("touched a hand-added machine: %v", core.calls)
	}
}

func TestSyncContinuesPastOneFailure(t *testing.T) {
	core := &fakeCore{failAdd: map[string]bool{"cell-a": true}}
	e := enroller(t, core, workspace("cell-a", "herdr", api.PhaseReady), workspace("cell-b", "herdr", api.PhaseReady))
	err := e.Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "add cell-a") {
		t.Fatalf("error %v", err)
	}
	if len(core.machines) != 1 || core.machines[0].Name != "cell-b" {
		t.Fatalf("machines %v", core.machines)
	}
}

func TestValidMachineName(t *testing.T) {
	for name, want := range map[string]bool{
		"glovecell-qualification-20261104": true,
		"scrapecast":                       true,
		"t7":                               false,
		"t1b":                              false,
		"tools":                            true,
		"-x":                               false,
		strings.Repeat("a", 33):            false,
	} {
		if got := ValidMachineName(name); got != want {
			t.Errorf("%q: got %v", name, got)
		}
	}
}

// Against the pinned herdr-core binary: HERDR_CORE_BIN=/path/to/herdr-core.
func TestCLICoreAgainstHerdrCore(t *testing.T) {
	bin := os.Getenv("HERDR_CORE_BIN")
	if bin == "" {
		t.Skip("set HERDR_CORE_BIN to a herdr-core binary")
	}
	// Unix socket paths are short (107 bytes); t.TempDir is too deep.
	dir, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	core := CLICore{Binary: bin, Env: []string{"HOME=" + filepath.Join(dir, "home"), "XDG_RUNTIME_DIR=" + filepath.Join(dir, "run"), "PATH=/usr/bin:/bin"}}
	ctx := context.Background()
	if _, err := core.run(ctx, "server", "start"); err != nil {
		t.Fatal(err)
	}
	defer core.run(ctx, "server", "stop") //nolint:errcheck
	if m, err := core.List(ctx); err != nil || len(m) != 0 {
		t.Fatalf("list: %v %v", m, err)
	}
	if err := core.Add(ctx, "cell-a", "false"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("add through a dead pipe: %v", err)
	}
	if err := core.Remove(ctx, "cell-a"); err == nil || !strings.Contains(err.Error(), "machine_unknown") {
		t.Fatalf("remove unknown: %v", err)
	}
}
