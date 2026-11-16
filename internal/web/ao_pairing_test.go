package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAOPairing(t *testing.T) {
	const code = "fixture-one-time-code"
	const password = "fixture-pairing-password"
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	status := `{"enabled":true,"loopbackOnly":false,"hostId":"host-1","password":"` + password + `","requireDeviceCredentials":true}`
	for _, tc := range []struct {
		name, auth, class, status string
		enabled, worker, execFail bool
		want                      int
	}{
		{"minted", "oidc", "ao", status, true, true, false, 200},
		{"automation", "bearer", "ao", status, true, true, false, 403},
		{"static", "basic", "ao", status, true, true, false, 403},
		{"disabled", "oidc", "ao", status, false, true, false, 404},
		{"other class", "oidc", "orca", status, true, true, false, 409},
		{"no worker", "oidc", "ao", status, true, false, false, 409},
		{"daemon down", "oidc", "ao", status, true, true, true, 409},
		{"password still accepted", "oidc", "ao", strings.Replace(status, `"requireDeviceCredentials":true`, `"requireDeviceCredentials":false`, 1), true, true, false, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = api.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			workspace := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "cell", Namespace: "control", UID: types.UID("uid-1")}, Spec: api.WorkspaceSpec{OrchestratorClassName: tc.class}}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workspace)
			if tc.worker {
				builder = builder.WithObjects(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "apc-uid1", Labels: map[string]string{api.OwnerLabel: "uid-1", "projects.agentwork.dev/role": "worker"}}})
			}
			c := builder.Build()
			var commands []string
			domain := ""
			if tc.enabled {
				domain = "ao.example.invalid"
			}
			s := &Server{Client: c, Namespace: "control", AOPairing: &AOPairing{Domain: domain, Now: func() time.Time { return now }, Exec: func(_ context.Context, pod *corev1.Pod, container string, command []string) ([]byte, error) {
				if pod.Name != "worker" || container != "workspace" {
					t.Fatalf("exec into %s/%s", pod.Name, container)
				}
				joined := strings.Join(command, " ")
				commands = append(commands, joined)
				if tc.execFail {
					return nil, errors.New(code)
				}
				if strings.Contains(joined, "pairing-code") {
					return []byte(`{"code":"` + code + `","expiresAt":"2026-11-15T12:10:00Z"}`), nil
				}
				return []byte(tc.status), nil
			}}}
			req := httptest.NewRequest("POST", "/api/v1/workspaces/cell/ao-pairing", nil)
			req.SetPathValue("name", "cell")
			ctx := context.WithValue(req.Context(), authKey{}, tc.auth)
			ctx = context.WithValue(ctx, actorKey{}, "operator")
			rec := httptest.NewRecorder()
			s.mintAOPairing(rec, req.WithContext(ctx))
			if rec.Code != tc.want {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
				t.Fatal("pairing response may be cached")
			}
			body := rec.Body.String()
			if strings.Contains(body, password) {
				t.Fatal("response exposes the pairing password")
			}
			if tc.want != 200 {
				if strings.Contains(body, code) {
					t.Fatal("error exposes a code")
				}
				if (tc.auth != "oidc" || !tc.enabled || !tc.worker || tc.class != "ao") && len(commands) != 0 {
					t.Fatal("exec ran without prerequisites", commands)
				}
				if tc.name == "password still accepted" && len(commands) != 1 {
					t.Fatal("minted a code while the password is accepted", commands)
				}
				return
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["code"] != code || got["address"] != "https://cell.ao.example.invalid" || !strings.HasPrefix(got["pairingUrl"].(string), "aomobile://pair#") {
				t.Fatalf("%v", got)
			}
			var audits corev1.ConfigMapList
			if err := c.List(context.Background(), &audits, client.InNamespace("control")); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, cm := range audits.Items {
				for _, v := range cm.Data {
					if strings.Contains(v, code) {
						t.Fatal("audit stores the code")
					}
				}
				if cm.Data["action"] == "ao-pairing-minted" && cm.Data["actor"] == "operator" {
					found = true
				}
			}
			if !found {
				t.Fatal("mint not audited")
			}
		})
	}
}
