package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RunRecords serves factory run records from an operator-configured directory,
// one <run-id>.json per orchestration Run. Records are bounded and must name
// their own run here; the console validates the full schema before rendering.
type RunRecords struct{ Dir string }

var runID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

const (
	maxRunRecord = 2 << 20
	maxRuns      = 200
)

type runHeader struct {
	Version int `json:"version"`
	Run     struct {
		ID          string `json:"id"`
		Objective   string `json:"objective"`
		Host        string `json:"host"`
		CreatedAt   string `json:"createdAt"`
		CollectedAt string `json:"collectedAt"`
	} `json:"run"`
	Tasks []json.RawMessage `json:"tasks"`
	// Kept raw so a malformed gate list cannot drop the record from the list.
	Gates json.RawMessage `json:"gates"`
}

func (c RunRecords) Validate() error {
	info, err := os.Stat(c.Dir)
	if !filepath.IsAbs(c.Dir) || err != nil || !info.IsDir() {
		return errors.New("run records require an existing absolute directory")
	}
	return nil
}

// read opens the record inside the directory root, so an id can never reach another path.
func (c RunRecords) read(id string) ([]byte, runHeader, error) {
	var header runHeader
	if !runID.MatchString(id) {
		return nil, header, os.ErrNotExist
	}
	root, err := os.OpenRoot(c.Dir)
	if err != nil {
		return nil, header, err
	}
	defer root.Close()
	f, err := root.Open(id + ".json")
	if err != nil {
		return nil, header, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxRunRecord+1))
	if err != nil {
		return nil, header, err
	}
	header, err = validRunRecord(raw, id)
	return raw, header, err
}

// validRunRecord applies the bounds every served or pulled record must meet.
func validRunRecord(raw []byte, id string) (runHeader, error) {
	var header runHeader
	if len(raw) > maxRunRecord {
		return header, errors.New("run record exceeds the size bound")
	}
	if err := json.Unmarshal(raw, &header); err != nil || header.Version != 1 || header.Run.ID != id {
		return header, errors.New("run record is invalid or names another run")
	}
	return header, nil
}

// runSummary is one row of the run list. Status, counts, times and the first
// failure are derived from the record's tasks the same way the console's
// report does; the console validates the full record before showing a report.
type runSummary struct {
	ID           string      `json:"id"`
	Objective    string      `json:"objective"`
	Host         string      `json:"host"`
	CreatedAt    string      `json:"createdAt,omitempty"`
	CollectedAt  string      `json:"collectedAt"`
	Tasks        int         `json:"tasks"`
	Status       string      `json:"status"`
	Failed       int         `json:"failed"`
	Succeeded    int         `json:"succeeded"`
	Running      int         `json:"running"`
	PendingGates int         `json:"pendingGates"`
	StartedAt    string      `json:"startedAt,omitempty"`
	EndedAt      string      `json:"endedAt,omitempty"`
	LastActivity string      `json:"lastActivity,omitempty"`
	Failure      *runFailure `json:"failure,omitempty"`
}

type runFailure struct {
	Task  string `json:"task"`
	Title string `json:"title"`
	Stage string `json:"stage,omitempty"`
	Error string `json:"error,omitempty"`
}

type runTaskFacts struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Attempts []struct {
		State       string `json:"state"`
		FailedStage string `json:"failedStage"`
		LastError   string `json:"lastError"`
		Outcome     string `json:"outcome"`
		StartedAt   string `json:"startedAt"`
		EndedAt     string `json:"endedAt"`
	} `json:"attempts"`
}

const maxSummaryText = 500

func clipText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// parseRunTime reads an RFC 3339 time; anything else is ignored.
func parseRunTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

func summarizeRun(h runHeader) runSummary {
	s := runSummary{ID: h.Run.ID, Objective: h.Run.Objective, Host: h.Run.Host, CreatedAt: h.Run.CreatedAt, CollectedAt: h.Run.CollectedAt, Tasks: len(h.Tasks)}
	var started, ended, last time.Time
	stamp := func(v string, earliest bool, into *time.Time) {
		if t, ok := parseRunTime(v); ok && (into.IsZero() || (earliest && t.Before(*into)) || (!earliest && t.After(*into))) {
			*into = t
		}
	}
	for _, raw := range h.Tasks {
		var task runTaskFacts
		// A task this summary cannot read counts as not started; the report
		// itself is validated in full before it is shown.
		if json.Unmarshal(raw, &task) != nil {
			continue
		}
		for _, a := range task.Attempts {
			stamp(a.StartedAt, true, &started)
			stamp(a.EndedAt, false, &ended)
			stamp(a.StartedAt, false, &last)
			stamp(a.EndedAt, false, &last)
		}
		// Mirrors taskState in web/src/factory/project.ts.
		n := len(task.Attempts)
		switch {
		case n > 0 && task.Attempts[n-1].Outcome == "succeeded":
			s.Succeeded++
		case (n > 0 && (task.Attempts[n-1].Outcome == "failed" || task.Attempts[n-1].State == "failed")) || task.Status == "failed":
			s.Failed++
			if s.Failure == nil {
				f := &runFailure{Task: clipText(task.ID, 128), Title: clipText(task.Title, 200)}
				if n > 0 {
					f.Stage = clipText(task.Attempts[n-1].FailedStage, 64)
					f.Error = clipText(task.Attempts[n-1].LastError, maxSummaryText)
				}
				s.Failure = f
			}
		case n > 0 && task.Attempts[n-1].EndedAt == "":
			s.Running++
		}
	}
	var gates []json.RawMessage
	json.Unmarshal(h.Gates, &gates)
	for _, raw := range gates {
		var gate struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &gate) == nil && gate.Status == "pending" {
			s.PendingGates++
		}
	}
	switch {
	case s.Failed > 0:
		s.Status = "failed"
	case s.PendingGates > 0:
		s.Status = "waiting"
	case s.Running > 0:
		s.Status = "running"
	case s.Tasks > 0 && s.Succeeded == s.Tasks:
		s.Status = "succeeded"
	default:
		s.Status = "pending"
	}
	if !started.IsZero() {
		s.StartedAt = started.UTC().Format(time.RFC3339)
	}
	if !ended.IsZero() && s.Running == 0 {
		s.EndedAt = ended.UTC().Format(time.RFC3339)
	}
	if !last.IsZero() {
		s.LastActivity = last.UTC().Format(time.RFC3339)
	}
	return s
}

func (c RunRecords) list(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		http.Error(w, "run records unavailable", 503)
		return
	}
	runs := []runSummary{}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		// Unreadable or invalid records are left out rather than failing the list.
		if _, h, err := c.read(id); err == nil {
			runs = append(runs, summarizeRun(h))
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CollectedAt > runs[j].CollectedAt })
	if len(runs) > maxRuns {
		runs = runs[:maxRuns]
	}
	writeJSON(w, runs)
}

func (c RunRecords) get(w http.ResponseWriter, r *http.Request) {
	raw, _, err := c.read(r.PathValue("id"))
	if err != nil {
		http.Error(w, "run record unavailable", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}
