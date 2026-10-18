package web

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClientsEndpointReadsLedger(t *testing.T) {
	s, h, obj := accessFixture(t)
	req := httptest.NewRequest("GET", "/api/v1/workspaces/project/clients", nil)
	req.Header.Set("Authorization", "Bearer "+s.Password)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("empty ledger: %d %s", rec.Code, rec.Body.String())
	}
	var empty struct {
		Clients []any             `json:"clients"`
		Owners  map[string]string `json:"owners"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || len(empty.Clients) != 0 {
		t.Fatalf("%v %+v", err, empty)
	}

	ledger := registration.NewLedger()
	_ = ledger.SetClient("t3", registration.StateRegistered, "7dc24d95-2241-4e16-92f4-b8eb0f6004ce", "/mnt/demo")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registration.ConfigMapName(string(obj.UID)),
			Namespace: "control",
			Labels:    map[string]string{api.OwnerLabel: string(obj.UID)},
		},
		Data: registration.EncodeConfigMapData(ledger),
	}
	if err := s.Client.Create(t.Context(), cm); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var got struct {
		Clients []map[string]string `json:"clients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 1 || got.Clients[0]["kind"] != "t3" || got.Clients[0]["state"] != "registered" {
		t.Fatalf("%+v", got.Clients)
	}
}
