package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeIntake answers like the Orca handoff intake and keeps what it was sent.
type fakeIntake struct {
	mu       sync.Mutex
	received []handoffRequest
	status   int
}

func (f *fakeIntake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "POST" && r.URL.Path == "/handoffs":
		var req handoffRequest
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		f.received = append(f.received, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		io.WriteString(w, `{"name":"`+req.Name+`","status":"preparing"}`)
	case r.Method == "GET" && r.URL.Path == "/handoffs/cve-fixes":
		io.WriteString(w, `{"name":"cve-fixes","status":"started","handle":"term_1"}`)
	case r.Method == "GET" && r.URL.Path == "/handoffs":
		io.WriteString(w, `[{"name":"cve-fixes","status":"started"}]`)
	default:
		http.NotFound(w, r)
	}
}

func handoffFixture(t *testing.T, status int) (*Server, http.Handler, *fakeIntake) {
	t.Helper()
	intake := &fakeIntake{status: status}
	backend := httptest.NewServer(intake)
	t.Cleanup(backend.Close)
	s, _, _ := accessFixture(t)
	s.Handoffs = &Handoffs{URL: backend.URL}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return s, h, intake
}

func handoffCall(h http.Handler, password, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if password != "" {
		req.Header.Set("Authorization", "Bearer "+password)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const validHandoffBody = `{"repo":"steve/infra","name":"cve-fixes","agent":"claude","brief":"# Fix CVEs","data":{"snapshot.json":"{}"}}`

func TestHandoffForwardsWithTheConsoleActor(t *testing.T) {
	s, h, intake := handoffFixture(t, http.StatusAccepted)
	if rec := handoffCall(h, "", "POST", "/api/v1/handoffs", validHandoffBody); rec.Code != 401 {
		t.Fatal("handoff accepted without authentication", rec.Code)
	}
	rec := handoffCall(h, s.Password, "POST", "/api/v1/handoffs", validHandoffBody)
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"preparing"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if len(intake.received) != 1 || intake.received[0].Actor != "automation-token" || intake.received[0].Data["snapshot.json"] != "{}" {
		t.Fatalf("intake received %+v", intake.received)
	}
	if rec := handoffCall(h, s.Password, "GET", "/api/v1/handoffs/cve-fixes", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"term_1"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec := handoffCall(h, s.Password, "GET", "/api/v1/handoffs", ""); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "[") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestHandoffRejectsCallerActorAndBadRequests(t *testing.T) {
	s, h, intake := handoffFixture(t, http.StatusAccepted)
	for _, body := range []string{
		`{"repo":"steve/infra","name":"x","agent":"claude","brief":"b","actor":"someone-else"}`,
		`{"repo":"steve","name":"x","agent":"claude","brief":"b"}`,
		`{"repo":"steve/infra","name":"Bad Name","agent":"claude","brief":"b"}`,
		`{"repo":"steve/infra","name":"x","agent":"grok","brief":"b"}`,
		`{"repo":"steve/infra","name":"x","agent":"claude","brief":""}`,
		`{"repo":"steve/infra","name":"x","agent":"claude","brief":"b","command":"rm"}`,
		`{"repo":"steve/infra","name":"x","agent":"claude","brief":"` + strings.Repeat("x", maxHandoffRequest) + `"}`,
	} {
		if rec := handoffCall(h, s.Password, "POST", "/api/v1/handoffs", body); rec.Code != 400 {
			t.Errorf("accepted %.80s: %d", body, rec.Code)
		}
	}
	if len(intake.received) != 0 {
		t.Fatal("invalid handoffs reached the intake")
	}
	req := httptest.NewRequest("POST", "/api/v1/handoffs", strings.NewReader(validHandoffBody))
	req.Header.Set("Authorization", "Bearer "+s.Password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("cross-origin handoff accepted", rec.Code)
	}
}

func TestHandoffRelaysConflictsAndHidesIntakeFailures(t *testing.T) {
	s, h, _ := handoffFixture(t, http.StatusConflict)
	if rec := handoffCall(h, s.Password, "POST", "/api/v1/handoffs", validHandoffBody); rec.Code != 409 {
		t.Fatal("conflict not relayed", rec.Code)
	}
	s, h, _ = handoffFixture(t, http.StatusInternalServerError)
	if rec := handoffCall(h, s.Password, "POST", "/api/v1/handoffs", validHandoffBody); rec.Code != 502 {
		t.Fatal("intake failure not reported as a bad gateway", rec.Code)
	}
	if rec := handoffCall(h, s.Password, "GET", "/api/v1/handoffs/..%2Fx", ""); rec.Code != 404 {
		t.Fatal("unsafe name forwarded", rec.Code)
	}
}

func TestHandoffIntakeMustBeAnOrigin(t *testing.T) {
	for _, u := range []string{"", "ftp://intake", "http://user:pass@intake", "http://intake/handoffs", "http://intake?x=1"} {
		if (Handoffs{URL: u}).Validate() == nil {
			t.Errorf("accepted %q", u)
		}
	}
	if (Handoffs{URL: "http://orca-handoff.agent-workspace-orca-pilot.svc.cluster.local:8791"}).Validate() != nil {
		t.Error("rejected the in-cluster intake")
	}
}
