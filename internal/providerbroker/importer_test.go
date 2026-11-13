package providerbroker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func importClient() client.Client {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func writeLogin(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// End to end against the broker's consumer: the CLI hands over the login and
// reports success once the broker has installed it and removed the Secret.
func TestImportHandsLoginToBroker(t *testing.T) {
	b, owner := enrollFixture(t)
	file := writeLogin(t, grokLogin(t, "from-laptop", b.now().Add(time.Hour)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			ownerMu.Lock()
			_ = b.TickLeases(ctx)
			ownerMu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
	}()
	var out bytes.Buffer
	if err := Import(ctx, b.Client, ImportOptions{Provider: "grok", File: file, Namespace: "broker", Poll: 10 * time.Millisecond}, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if owner.files["grok"]["https://auth.x.ai::client"].(map[string]any)["key"] != "from-laptop" {
		t.Fatal("login not installed")
	}
	if !strings.Contains(out.String(), "share one sign-in session") || strings.Contains(out.String(), "refresh-from-laptop") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestImportReportsRefusalAndCleansUp(t *testing.T) {
	b, _ := enrollFixture(t)
	exp := b.now().Add(time.Hour)
	if err := b.Install(context.Background(), "cursor", cursorLogin(t, "pinned", exp), false); err != nil {
		t.Fatal(err)
	}
	file := writeLogin(t, cursorLogin(t, "someone-else", exp))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			ownerMu.Lock()
			_ = b.TickLeases(ctx)
			ownerMu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
	}()
	err := Import(ctx, b.Client, ImportOptions{Provider: "cursor", File: file, Namespace: "broker", Poll: 10 * time.Millisecond}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "different Cursor account") {
		t.Fatalf("refusal not reported: %v", err)
	}
	if e := b.Client.Get(ctx, types.NamespacedName{Namespace: "broker", Name: ImportSecretName("cursor")}, &corev1.Secret{}); !apierrors.IsNotFound(e) {
		t.Fatal("refused import Secret left behind")
	}
	if err := Import(ctx, b.Client, ImportOptions{Provider: "cursor", File: file, Namespace: "broker", ReplaceAccount: true, Poll: 10 * time.Millisecond}, &bytes.Buffer{}); err != nil {
		t.Fatalf("replace: %v", err)
	}
}

func TestImportChecksLocallyAndTimesOut(t *testing.T) {
	c := importClient()
	ctx := context.Background()
	if err := Import(ctx, c, ImportOptions{Provider: "grok", File: filepath.Join(t.TempDir(), "missing"), Namespace: "broker"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "sign in with the grok CLI first") {
		t.Fatalf("missing file: %v", err)
	}
	if err := Import(ctx, c, ImportOptions{Provider: "grok", File: writeLogin(t, []byte(`{"x":{}}`)), Namespace: "broker"}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid login sent")
	}
	var list corev1.SecretList
	_ = c.List(ctx, &list)
	if len(list.Items) != 0 {
		t.Fatal("invalid login reached the cluster")
	}
	// No broker consumes it: time out and remove the Secret holding the login.
	file := writeLogin(t, grokLogin(t, "k", time.Now().Add(time.Hour)))
	if err := Import(ctx, c, ImportOptions{Provider: "grok", File: file, Namespace: "broker", Wait: 50 * time.Millisecond, Poll: 10 * time.Millisecond}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "did not pick up") {
		t.Fatalf("timeout: %v", err)
	}
	_ = c.List(ctx, &list)
	if len(list.Items) != 0 {
		t.Fatal("login left in the cluster after timeout")
	}
	if err := Import(ctx, c, ImportOptions{Provider: "codex", Namespace: "broker"}, &bytes.Buffer{}); err == nil {
		t.Fatal("unsupported provider")
	}
}
