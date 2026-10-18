package t3

// This opt-in qualification creates disposable, network-disabled containers
// and bare local Git repositories. It never grants project Git access or
// deploys to a cluster. The normal test suite does not contact T3 Connect.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"os/exec"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
	"time"
)

func TestManagedConnectTwoDisposableSandboxes(t *testing.T) {
	if os.Getenv("APC_T3_CONNECT_QUALIFY") != "true" {
		t.Skip("opt-in T3 Connect qualification requires an authorized controller account and disposable workspace image")
	}
	image := os.Getenv("APC_T3_CONNECT_WORKSPACE_IMAGE")
	directory := os.Getenv("APC_T3_CONNECT_ACCOUNT_DIR")
	connector := os.Getenv("APC_T3_CONNECT_CONNECTOR")
	if image == "" || directory == "" || connector == "" {
		t.Fatal("qualification configuration missing")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	cloud := &Cloud{URL: DefaultRelayURL, Tokens: &Account{Directory: directory, Binary: os.Getenv("APC_T3_CONNECT_BINARY")}}
	m := &Managed{Registry: &Registry{Client: c, Namespace: "disposable-control", Cloud: cloud}, Connector: connector}
	m.Exec = func(ctx context.Context, pod *corev1.Pod, args []string, in io.Reader, out io.Writer) error {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", pod.Name}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, io.Discard
		return cmd.Run()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	defer func() { cancel(); <-done }()
	var projects []*api.ProjectWorkspace
	for _, suffix := range []string{"a", "b"} {
		random := make([]byte, 16)
		_, _ = rand.Read(random)
		random[6] = (random[6] & 15) | 64
		random[8] = (random[8] & 63) | 128
		id := hex.EncodeToString(random)
		uuid := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
		name := "apc-t3-qual-" + id[:10] + "-" + suffix
		cmd := exec.Command("docker", "run", "-d", "--name", name, "--hostname", name, "--network", "none", "--tmpfs", "/workspace:uid=1000,gid=1000,mode=0700", "--entrypoint", "/bin/bash", image, "-c", "mkdir -p /workspace/repo; git -C /workspace/repo init -q; exec sleep infinity")
		if e := cmd.Run(); e != nil {
			t.Fatal("disposable sandbox creation failed", e)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
		w := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "disposable-control", UID: types.UID(uuid)}, Spec: api.WorkspaceSpec{DesiredState: "Running"}}
		if e := c.Create(ctx, w); e != nil {
			t.Fatal(e)
		}
		no := false
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workerNamespace(w), UID: types.UID(uuid + "-worker"), Labels: map[string]string{api.OwnerLabel: uuid, "projects.agentwork.dev/role": "worker"}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
		if e := c.Create(ctx, pod); e != nil {
			t.Fatal(e)
		}
		projects = append(projects, w)
	}
	defer func() {
		for _, w := range projects {
			removeCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			var e error
			for attempt := 0; attempt < 5; attempt++ {
				e = m.Remove(removeCtx, w)
				if e == nil {
					break
				}
				t.Log("revocation will retry", w.Name, e)
				time.Sleep(2 * time.Second)
			}
			cancel()
			if e != nil {
				t.Errorf("disposable environment revocation failed: %v", e)
			} else {
				t.Log("remote deregistration verified", w.Name)
			}
		}
	}()
	for _, w := range projects {
		_ = m.Ensure(ctx, w)
	}
	for _, w := range projects {
		deadline := time.Now().Add(6 * time.Minute)
		var e error
		lastError := ""
		for time.Now().Before(deadline) {
			e = m.Ensure(ctx, w)
			if e != nil && e.Error() != lastError {
				t.Log("registration:", e)
				lastError = e.Error()
			}
			if e == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if e != nil {
			t.Fatal("automatic registration or remote identity check failed", e)
		}
		if e = exec.Command("docker", "exec", w.Name, "test", "!", "-e", Data+"/userdata/secrets/cloud-cli-oauth-token.bin").Run(); e != nil {
			t.Fatal("owner credential found in sandbox")
		}
		t.Log("automatic registration and remote identity verified", w.Name)
	}
	catalog, e := cloud.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range projects {
		found := false
		for _, env := range catalog {
			if env.ID == string(w.UID) && env.Label == w.Name {
				found = true
			}
		}
		if !found {
			t.Fatal("project absent or mislabeled in shared account catalog", w.Name)
		}
	}
	// A controller restart retains the ledger, project identity, and device state.
	first := projects[0]
	if e = m.Suspend(ctx, first); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		e = m.Ensure(ctx, first)
		if e == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if e != nil {
		t.Fatal("resume did not reconnect the original environment", e)
	}
	ledger, e := m.Registry.Ledger(ctx, first)
	if e != nil || strings.TrimSpace(string(ledger.Data["environment-id"])) != string(first.UID) {
		t.Fatal("resume lost stable environment identity", e)
	}
	t.Log("two environments selected through one account; pause/resume preserved identity")
}
