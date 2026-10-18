package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func record(id string) []byte {
	return []byte(`{"version":1,"run":{"id":"` + id + `","objective":"o","host":"ns/pod","createdAt":"2026-10-18T00:00:00Z","collectedAt":"2026-10-18T00:01:00Z"},"tasks":[],"gates":[],"agentReports":[]}`)
}
func hashOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestRunRecordPullerCopiesOnlyValidChangedRecords(t *testing.T) {
	served := map[string][]byte{
		"run_good": record("run_good"),
		"run_liar": record("run_other"),
		"run_hash": record("run_hash"),
		"run_big":  []byte(`{"version":1,"run":{"id":"run_big"},"pad":"` + strings.Repeat("x", maxRunRecord) + `"}`),
	}
	index := []map[string]string{}
	for id, body := range served {
		sum := hashOf(body)
		if id == "run_hash" {
			sum = hashOf([]byte("something else"))
		}
		index = append(index, map[string]string{"id": id, "sha256": sum})
	}
	index = append(index, map[string]string{"id": "../escape", "sha256": hashOf([]byte("x"))})
	var fetches atomic.Int32
	publisher := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("puller sent", r.Method)
		}
		if r.URL.Path == "/runs" {
			json.NewEncoder(w).Encode(index)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/runs/")
		fetches.Add(1)
		w.Write(served[id])
	}))
	defer publisher.Close()
	dir := t.TempDir()
	p := RunRecordPuller{Dir: dir, Sources: []string{publisher.URL + "/"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	err := p.Pull(context.Background())
	if err == nil || !strings.Contains(err.Error(), "run_liar") || !strings.Contains(err.Error(), "run_hash") || !strings.Contains(err.Error(), "run_big") {
		t.Fatal("invalid records were not reported", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "run_good.json" {
		t.Fatal("kept", entries)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "run_good.json")); string(b) != string(served["run_good"]) {
		t.Fatal("record changed in transit")
	}
	// The valid record is not fetched again while its hash is unchanged.
	before := fetches.Load()
	p.Pull(context.Background())
	if fetches.Load()-before != 3 {
		t.Fatal("refetched an unchanged record; fetches:", fetches.Load()-before)
	}
}

func TestRunRecordPullerRejectsUnsafeSources(t *testing.T) {
	dir := t.TempDir()
	for _, source := range []string{"file:///etc", "http://user:pw@host", "http://host/path", "http://host?x=1", "ftp://host", "host:8790"} {
		if (RunRecordPuller{Dir: dir, Sources: []string{source}}).Validate() == nil {
			t.Fatal("accepted source", source)
		}
	}
	if (RunRecordPuller{Dir: dir}).Validate() == nil || (RunRecordPuller{Dir: "relative", Sources: []string{"http://host"}}).Validate() == nil {
		t.Fatal("accepted an incomplete configuration")
	}
}
