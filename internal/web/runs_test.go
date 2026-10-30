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

func TestRunSummaryDerivesStatusDurationAndFailure(t *testing.T) {
	dir := t.TempDir()
	records := map[string]string{
		// Mirrors the 2026-10-17 trial: both tasks failed; the first failure is surfaced.
		"run_failed": `{"version":1,"run":{"id":"run_failed","objective":"o","host":"ns/pod","createdAt":"2026-10-17T13:33:48Z","collectedAt":"2026-10-18T14:00:00Z"},"tasks":[` +
			`{"id":"t1","title":"slugify","status":"failed","attempts":[{"dispatchId":"d1","agent":"codex","state":"failed","failedStage":"agent_readiness","lastError":"Agent startup blocked","startedAt":"2026-10-17T13:34:00Z","endedAt":"2026-10-17T13:40:00Z"}]},` +
			`{"id":"t2","title":"wordfreq","status":"failed","attempts":[{"dispatchId":"d2","agent":"claude","state":"failed","lastError":"` + strings.Repeat("x", 900) + `"}]}],"gates":[]}`,
		"run_waiting": `{"version":1,"run":{"id":"run_waiting","objective":"o","host":"ns/pod","createdAt":"2026-10-18T09:00:00Z","collectedAt":"2026-10-18T09:42:00Z"},"tasks":[` +
			`{"id":"t1","title":"a","status":"completed","attempts":[{"dispatchId":"d1","agent":"codex","state":"settled","outcome":"succeeded","startedAt":"2026-10-18T09:01:00Z","endedAt":"2026-10-18T09:12:00Z"}]},` +
			`{"id":"t2","title":"b","status":"blocked","attempts":[{"dispatchId":"d2","agent":"claude","state":"running","startedAt":"2026-10-18T09:30:00Z"}]}],` +
			`"gates":[{"id":"g","taskId":"t2","question":"q","options":["a"],"status":"pending"},{"id":"g2","taskId":"t2","question":"q","options":["a"],"status":"resolved"}]}`,
		"run_done": `{"version":1,"run":{"id":"run_done","objective":"o","host":"ns/pod","createdAt":"2026-10-18T09:00:00Z","collectedAt":"2026-10-18T10:00:00Z"},"tasks":[` +
			`{"id":"t1","title":"a","status":"completed","attempts":[{"dispatchId":"d1","agent":"codex","state":"settled","outcome":"succeeded","startedAt":"2026-10-18T09:01:00Z","endedAt":"2026-10-18T09:12:00Z"}]}]}`,
		// Tasks and gates the summary cannot read still list the run, as before.
		"run_odd": `{"version":1,"run":{"id":"run_odd","objective":"o","host":"ns/pod","createdAt":"x","collectedAt":"2026-10-18T08:00:00Z"},"tasks":[{"attempts":"nope"},{}],"gates":{"not":"a list"}}`,
	}
	for id, body := range records {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, _, _ := accessFixture(t)
	s.RunRecords = &RunRecords{Dir: dir}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer "+s.Password)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var runs []runSummary
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &runs) != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	byID := map[string]runSummary{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	if len(byID) != 4 {
		t.Fatal("every valid record must stay listed", runs)
	}
	f := byID["run_failed"]
	if f.Status != "failed" || f.Failed != 2 || f.Failure == nil || f.Failure.Task != "t1" || f.Failure.Stage != "agent_readiness" || f.Failure.Error != "Agent startup blocked" {
		t.Fatalf("failed run summary: %+v %+v", f, f.Failure)
	}
	if f.StartedAt != "2026-10-17T13:34:00Z" || f.EndedAt != "2026-10-17T13:40:00Z" {
		t.Fatalf("failed run times: %+v", f)
	}
	w := byID["run_waiting"]
	if w.Status != "waiting" || w.PendingGates != 1 || w.Running != 1 || w.Succeeded != 1 || w.EndedAt != "" || w.LastActivity != "2026-10-18T09:30:00Z" || w.Failure != nil {
		t.Fatalf("waiting run summary: %+v", w)
	}
	if d := byID["run_done"]; d.Status != "succeeded" || d.EndedAt != "2026-10-18T09:12:00Z" {
		t.Fatalf("succeeded run summary: %+v", d)
	}
	if o := byID["run_odd"]; o.Status != "pending" || o.Tasks != 2 || o.StartedAt != "" || o.PendingGates != 0 {
		t.Fatalf("unreadable tasks must not fail the list: %+v", o)
	}
	// Long failure text is clipped in the list; the full record still has it.
	failed2 := summarizeRun(runHeader{Tasks: []json.RawMessage{json.RawMessage(`{"id":"t","status":"failed","attempts":[{"lastError":"` + strings.Repeat("é", 900) + `"}]}`)}})
	if n := len([]rune(failed2.Failure.Error)); n != maxSummaryText {
		t.Fatal("failure text not clipped", n)
	}
}
