package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeRun(t *testing.T, dir, name, id, collected string) {
	t.Helper()
	body := `{"version":1,"run":{"id":"` + id + `","objective":"o-` + id + `","host":"ns/pod","createdAt":"2026-10-18T00:00:00Z","collectedAt":"` + collected + `"},"tasks":[{},{}],"gates":[],"agentReports":[]}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunRecordsServeBoundedMatchingRecords(t *testing.T) {
	dir := t.TempDir()
	writeRun(t, dir, "run_old.json", "run_old", "2026-10-17T10:00:00Z")
	writeRun(t, dir, "run_new.json", "run_new", "2026-10-18T10:00:00Z")
	writeRun(t, dir, "run_liar.json", "run_other", "2026-10-18T11:00:00Z")
	os.WriteFile(filepath.Join(dir, "run_big.json"), []byte(`{"version":1,"run":{"id":"run_big"},"pad":"`+strings.Repeat("x", maxRunRecord)+`"}`), 0o600)
	outside := t.TempDir()
	writeRun(t, outside, "secret.json", "secret", "2026-10-18T12:00:00Z")
	os.Symlink(filepath.Join(outside, "secret.json"), filepath.Join(dir, "run_link.json"))

	s, _, _ := accessFixture(t)
	s.RunRecords = &RunRecords{Dir: dir}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer "+s.Password)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/api/v1/runs", false); rec.Code != 401 {
		t.Fatal("run list served without authentication", rec.Code)
	}
	rec := get("/api/v1/runs", true)
	var runs []struct {
		ID    string `json:"id"`
		Tasks int    `json:"tasks"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &runs) != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if len(runs) != 2 || runs[0].ID != "run_new" || runs[1].ID != "run_old" || runs[0].Tasks != 2 {
		t.Fatal("list must hold only valid records, newest first", runs)
	}
	if rec := get("/api/v1/runs/run_new", true); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"o-run_new"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, id := range []string{"run_other", "run_liar", "run_big", "run_link", "..%2Fsecret", "missing"} {
		if rec := get("/api/v1/runs/"+id, true); rec.Code != 404 {
			t.Fatal("served", id, rec.Code)
		}
	}
}

func TestRunRecordsAreOptional(t *testing.T) {
	s, h, _ := accessFixture(t)
	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer "+s.Password)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal("run routes exist without a configured directory", rec.Code)
	}
	s.RunRecords = &RunRecords{Dir: "relative/path"}
	if _, err := s.Handler(); err == nil {
		t.Fatal("relative run record directory accepted")
	}
}

func TestBrowserRuns(t *testing.T) {
	if os.Getenv("APC_BROWSER_TEST") != "true" {
		t.Skip("set APC_BROWSER_TEST=true with node, Playwright and Chromium installed")
	}
	dir := t.TempDir()
	fixtures, _ := filepath.Glob("testdata/runs/*.json")
	for _, f := range fixtures {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, filepath.Base(f)), raw, 0o600)
	}
	// Passes the server's header checks but claims a check result from an agent, which the schema forbids.
	os.WriteFile(filepath.Join(dir, "run_forged.json"), []byte(`{"version":1,"run":{"id":"run_forged","objective":"forged","host":"ns/pod","createdAt":"2026-10-18T00:00:00Z","collectedAt":"2026-10-18T00:00:00Z"},"tasks":[{"id":"t","title":"t","spec":"s","status":"completed","checks":[{"name":"all","status":"passed","source":"agent"}]}]}`), 0o600)
	s, _, _ := accessFixture(t)
	server := httptest.NewUnstartedServer(nil)
	s.Origin = "https://" + server.Listener.Addr().String()
	s.RunRecords = &RunRecords{Dir: dir}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = h
	server.StartTLS()
	defer server.Close()
	cmd := exec.Command("node", "../../scripts/qualify-runs.cjs")
	cmd.Env = append(os.Environ(), "APC_TEST_WEB_ORIGIN="+s.Origin, "APC_TEST_WEB_PASSWORD="+s.Password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser qualification failed: %v\n%s", err, out)
	}
	t.Log(string(out))
}
